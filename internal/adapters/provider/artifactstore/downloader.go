package artifactstore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Downloader 使用受控 HTTP 客户端流式下载外部制品。
type Downloader struct {
	client   *httpclient.Client
	store    *Store
	maxBytes int64
	now      func() time.Time
}

var _ ports.ArtifactFetcher = (*Downloader)(nil)

// NewDownloader 创建通用外部制品下载器。
func NewDownloader(client *httpclient.Client, store *Store, maxBytes int64) *Downloader {
	if maxBytes <= 0 {
		maxBytes = defaultMaxArtifactBytes
	}
	return &Downloader{
		client: client, store: store, maxBytes: maxBytes,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Fetch 下载、校验、计算摘要并原子保存制品。
func (d *Downloader) Fetch(ctx context.Context, artifact provenance.Artifact) (provenance.Artifact, error) {
	request, err := downloadRequest(artifact)
	if err != nil {
		return provenance.Artifact{}, err
	}
	response, err := d.client.Open(ctx, request)
	if err != nil {
		return provenance.Artifact{}, err
	}
	defer response.Body.Close()
	if err = validateResponseRevision(artifact.Provenance.SourceRevision, response.Header.Get("ETag")); err != nil {
		return provenance.Artifact{}, err
	}
	if err = d.validateLength(response.ContentLength); err != nil {
		return provenance.Artifact{}, err
	}
	artifact.Provenance.FetchedAt = d.now()
	artifact.Provenance.ProviderRequestID = requestID(response)
	if err = applyMediaType(&artifact, response.Header.Get("Content-Type")); err != nil {
		return provenance.Artifact{}, err
	}
	reader := bufio.NewReader(response.Body)
	if err = validateSignature(artifact.MediaType, reader); err != nil {
		return provenance.Artifact{}, err
	}
	stored, err := d.store.Save(ctx, artifact, reader)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("保存下载制品: %w", err)
	}
	return stored, nil
}

func downloadRequest(artifact provenance.Artifact) (httpclient.Request, error) {
	request := httpclient.Request{Method: http.MethodGet, URL: artifact.Reference}
	if artifact.Provenance.SourceRevision == "" {
		return request, nil
	}
	if !httpclient.IsStrongETag(artifact.Provenance.SourceRevision) {
		return httpclient.Request{}, fmt.Errorf("%w: 制品来源修订不是强 ETag", domain.ErrInvalidInput)
	}
	request.Headers = make(http.Header)
	request.Headers.Set("If-Match", artifact.Provenance.SourceRevision)
	return request, nil
}

func validateResponseRevision(expected, actual string) error {
	if expected == "" {
		return nil
	}
	if actual != expected {
		return fmt.Errorf("%w: 制品下载期间来源修订发生变化", domain.ErrProviderUnavailable)
	}
	return nil
}

func (d *Downloader) validateLength(length int64) error {
	if length == 0 {
		return fmt.Errorf("%w: 制品响应为空", domain.ErrInvalidInput)
	}
	if length > d.maxBytes {
		return fmt.Errorf("%w: 制品声明大小 %d 超过上限", domain.ErrInvalidInput, length)
	}
	return nil
}

func applyMediaType(artifact *provenance.Artifact, header string) error {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	actual, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("%w: 制品媒体类型无效", domain.ErrInvalidInput)
	}
	expected, _, err := mime.ParseMediaType(artifact.MediaType)
	if err != nil {
		return fmt.Errorf("%w: 预期媒体类型无效", domain.ErrInvalidInput)
	}
	if !compatibleMediaType(expected, actual) {
		return fmt.Errorf("%w: 预期 %s，供应商返回 %s", domain.ErrInvalidInput, expected, actual)
	}
	if actual != "application/octet-stream" {
		artifact.MediaType = actual
	}
	return nil
}

func compatibleMediaType(expected, actual string) bool {
	if expected == "application/octet-stream" || actual == "application/octet-stream" || expected == actual {
		return true
	}
	if expected != "image/tiff" {
		return false
	}
	return actual == "image/tif" || actual == "application/geotiff" || actual == "application/x-geotiff"
}

func validateSignature(mediaType string, reader *bufio.Reader) error {
	actual, _, err := mime.ParseMediaType(mediaType)
	if err != nil || !isTIFFMediaType(actual) {
		return err
	}
	prefix, err := reader.Peek(4)
	if err != nil {
		return fmt.Errorf("%w: TIFF 文件头不完整", domain.ErrInvalidInput)
	}
	littleEndian := []byte{'I', 'I', 42, 0}
	bigEndian := []byte{'M', 'M', 0, 42}
	if !bytes.Equal(prefix, littleEndian) && !bytes.Equal(prefix, bigEndian) {
		return fmt.Errorf("%w: 制品不是有效 TIFF 文件头", domain.ErrInvalidInput)
	}
	return nil
}

func isTIFFMediaType(value string) bool {
	return value == "image/tiff" || value == "image/tif" ||
		value == "application/geotiff" || value == "application/x-geotiff"
}

func requestID(response *http.Response) string {
	for _, name := range []string{"X-Request-ID", "X-Log-ID", "X-Amzn-RequestId"} {
		if value := response.Header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
