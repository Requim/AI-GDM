package gdal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func validateArtifactFile(ctx context.Context, config Config, artifact provenance.Artifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	if artifact.LocalPath == "" || artifact.SizeBytes <= 0 || len(artifact.Provenance.SHA256) != 64 {
		return fmt.Errorf("%w: 栅格路径、大小或校验和无效", domain.ErrInvalidInput)
	}
	resolved, info, err := secureArtifactPath(config.ArtifactRoot, artifact.LocalPath)
	if err != nil {
		return err
	}
	if info.Size() != artifact.SizeBytes || info.Size() > config.MaxInputBytes {
		return fmt.Errorf("%w: 栅格大小与来源元数据不一致", domain.ErrInvalidInput)
	}
	checksum, err := fileChecksum(ctx, resolved, config.MaxInputBytes)
	if err != nil {
		return err
	}
	if !strings.EqualFold(checksum, artifact.Provenance.SHA256) {
		return fmt.Errorf("%w: 栅格 SHA-256 与来源元数据不一致", domain.ErrInvalidInput)
	}
	return nil
}

func secureArtifactPath(root, path string) (string, os.FileInfo, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("检查 LHASA 栅格: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: LHASA 栅格不是常规文件", domain.ErrInvalidInput)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, fmt.Errorf("解析制品根目录: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("解析 LHASA 栅格路径: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("%w: LHASA 栅格不在受控制品目录", domain.ErrInvalidInput)
	}
	return resolvedPath, linkInfo, nil
}

func fileChecksum(ctx context.Context, path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 LHASA 栅格: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(contextFile{ctx: ctx, reader: file}, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("计算 LHASA 栅格校验和: %w", err)
	}
	if written > maxBytes {
		return "", fmt.Errorf("%w: LHASA 栅格超过大小上限", domain.ErrInvalidInput)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type contextFile struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextFile) Read(payload []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(payload)
	}
}
