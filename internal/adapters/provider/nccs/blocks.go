package nccs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func blockPath(directory string, index int64) string {
	return filepath.Join(directory, fmt.Sprintf("%06d.part", index))
}

func (f *Fetcher) block(ctx context.Context, artifact provenance.Artifact, directory string, index int64) error {
	start := index * f.config.ChunkBytes
	length := min(f.config.ChunkBytes, artifact.SizeBytes-start)
	path := blockPath(directory, index)
	if ok, err := verifiedBlock(path, length); err != nil || ok {
		return err
	}
	var last error
	for attempt := 0; attempt < f.config.Attempts; attempt++ {
		last = f.transfer(ctx, artifact, path, start, length)
		if last == nil {
			return stampBlock(path)
		}
		if !retryBlock(ctx, last) {
			return last
		}
		f.config.Logger.WarnContext(ctx, "NCCS 分段读取失败，保留断点", "block", index, "attempt", attempt+1, "error", last)
		if err := wait(ctx, time.Duration(attempt+1)*f.config.Backoff); err != nil {
			return err
		}
	}
	return last
}

func (f *Fetcher) transfer(ctx context.Context, artifact provenance.Artifact, path string, start, length int64) error {
	file, err := openPartial(path, length)
	if err != nil {
		return err
	}
	defer file.Close()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if offset == length {
		return ctx.Err()
	}
	headers := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", start+offset, start+length-1)},
		"If-Match": {artifact.Provenance.SourceRevision}, "Accept-Encoding": {"identity"}}
	response, err := f.client.Open(ctx, httpclient.Request{Method: http.MethodGet,
		URL: artifact.Reference, Headers: headers, MaxAttempts: 1, RedirectPolicy: httpclient.RedirectDeny})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = validateRange(response, artifact, start+offset, start+length-1); err != nil {
		return err
	}
	written, copyErr := io.CopyBuffer(file, io.LimitReader(response.Body, length-offset), make([]byte, 64<<10))
	syncErr := file.Sync()
	if copyErr != nil || syncErr != nil {
		return errors.Join(copyErr, syncErr)
	}
	if written != length-offset {
		return io.ErrUnexpectedEOF
	}
	return ctx.Err()
}

func validateRange(response *http.Response, artifact provenance.Artifact, start, end int64) error {
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, artifact.SizeBytes)
	encoding := response.Header.Get("Content-Encoding")
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != expected ||
		response.Header.Get("ETag") != artifact.Provenance.SourceRevision ||
		(encoding != "" && encoding != "identity") {
		return fmt.Errorf("%w: NCCS Range 或来源版本不一致", domain.ErrInvalidInput)
	}
	return nil
}

func openPartial(path string, length int64) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Size() > length) {
		return nil, fmt.Errorf("%w: NCCS 断点类型或长度异常", domain.ErrInvalidInput)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stampBlock(path string) error {
	digest, err := fileDigest(path)
	if err != nil {
		return err
	}
	temporary := path + ".sha256.tmp"
	if err = os.WriteFile(temporary, []byte(digest), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path+".sha256")
}

func verifiedBlock(path string, length int64) (bool, error) {
	stamp, err := os.ReadFile(path + ".sha256")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	digest, err := fileDigest(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != length || string(stamp) != digest {
		return false, fmt.Errorf("%w: NCCS 已完成分段校验失败", domain.ErrInvalidInput)
	}
	return true, nil
}

func appendVerifiedBlock(ctx context.Context, target io.Writer, path string, length int64) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ok, err := verifiedBlock(path, length); err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%w: NCCS 分段未完成", domain.ErrInvalidInput))
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.CopyN(target, file, length)
	return err
}

func retryBlock(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, domain.ErrInvalidInput) {
		return false
	}
	var provider *httpclient.ProviderError
	if errors.As(err, &provider) {
		return provider.Retryable
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
