package lhasa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// RevisionVerifier 校验组合制品的远端分片修订仍保持一致。
type RevisionVerifier interface {
	VerifyCurrent(ctx context.Context, artifact provenance.Artifact) error
}

// RasterMosaicker 将经过校验的同网格 TIFF 分片物化为一个栅格。
type RasterMosaicker interface {
	Mosaic(ctx context.Context, inputs []string, output string) error
}

// ArtifactSaver 原子保存最终组合制品并计算摘要。
type ArtifactSaver interface {
	Save(ctx context.Context, artifact provenance.Artifact, source io.Reader) (provenance.Artifact, error)
}

// FetcherConfig 控制分片下载、临时目录和最终制品大小。
type FetcherConfig struct {
	TemporaryDir string
	MaxPartBytes int64
	MaxBytes     int64
	MaxAttempts  int
	RetryBackoff time.Duration
	Logger       *slog.Logger
}

// TiledFetcher 逐片校验下载、拼接并原子保存 Earthdata 组合栅格。
type TiledFetcher struct {
	client       *httpclient.Client
	verifier     RevisionVerifier
	mosaicker    RasterMosaicker
	saver        ArtifactSaver
	temporaryDir string
	maxPartBytes int64
	maxBytes     int64
	maxAttempts  int
	retryBackoff time.Duration
	logger       *slog.Logger
	now          func() time.Time
}

var _ ports.ArtifactFetcher = (*TiledFetcher)(nil)

// NewTiledFetcher 创建具有前后修订校验的 Earthdata 分片获取器。
func NewTiledFetcher(client *httpclient.Client, verifier RevisionVerifier,
	mosaicker RasterMosaicker, saver ArtifactSaver, config FetcherConfig,
) (*TiledFetcher, error) {
	if config.MaxPartBytes <= 0 {
		config.MaxPartBytes = defaultMaxPart
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = defaultMaxBytes
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 2
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil || verifier == nil || mosaicker == nil || saver == nil ||
		config.MaxPartBytes < minimumTIFFBytes || config.MaxBytes < config.MaxPartBytes ||
		config.MaxAttempts > 5 {
		return nil, fmt.Errorf("%w: Earthdata 分片获取依赖或资源上限无效", domain.ErrInvalidInput)
	}
	return &TiledFetcher{
		client: client, verifier: verifier, mosaicker: mosaicker, saver: saver,
		temporaryDir: config.TemporaryDir, maxPartBytes: config.MaxPartBytes,
		maxBytes: config.MaxBytes, maxAttempts: config.MaxAttempts,
		retryBackoff: config.RetryBackoff, logger: config.Logger,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Fetch 获取同一组合修订的全部分片并只在整组成功后提交制品。
func (f *TiledFetcher) Fetch(ctx context.Context, artifact provenance.Artifact) (provenance.Artifact, error) {
	if err := f.validateArtifact(artifact); err != nil {
		return provenance.Artifact{}, err
	}
	if err := f.verifier.VerifyCurrent(ctx, artifact); err != nil {
		return provenance.Artifact{}, fmt.Errorf("下载前校验 Earthdata 组合修订: %w", err)
	}
	directory, err := os.MkdirTemp(f.temporaryDir, "ai-gdm-earthdata-*")
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("创建 Earthdata 分片临时目录: %w", err)
	}
	defer os.RemoveAll(directory)
	paths, parts, err := f.downloadParts(ctx, directory, artifact)
	if err != nil {
		return provenance.Artifact{}, err
	}
	if err = f.verifier.VerifyCurrent(ctx, artifact); err != nil {
		return provenance.Artifact{}, fmt.Errorf("下载后校验 Earthdata 组合修订: %w", err)
	}
	output := filepath.Join(directory, "lhasa-china.tif")
	if err = f.mosaicker.Mosaic(ctx, paths, output); err != nil {
		return provenance.Artifact{}, fmt.Errorf("拼接 Earthdata LHASA 分片: %w", err)
	}
	artifact.Provenance.SourceParts = parts
	artifact.Provenance.FetchedAt = f.now()
	artifact.Provenance.QualityFlags = appendQualityFlag(artifact.Provenance.QualityFlags, "tiled_download_verified")
	return f.saveMosaic(ctx, artifact, output)
}

func (f *TiledFetcher) validateArtifact(artifact provenance.Artifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	parts := artifact.Provenance.SourceParts
	if len(parts) == 0 || artifact.Provenance.SourceRevision != provenance.CompositeSourceRevision(parts) {
		return fmt.Errorf("%w: Earthdata 组合制品清单或修订无效", domain.ErrInvalidInput)
	}
	var total int64
	for _, part := range parts {
		if !httpclient.IsStrongETag(part.Revision) || part.SizeBytes > f.maxPartBytes ||
			part.SizeBytes > f.maxBytes-total || !validBBox(part.BBox) {
			return fmt.Errorf("%w: Earthdata 分片修订或大小无效", domain.ErrInvalidInput)
		}
		total += part.SizeBytes
	}
	if artifact.SizeBytes != total {
		return fmt.Errorf("%w: Earthdata 组合制品声明总大小无效", domain.ErrInvalidInput)
	}
	return nil
}

func (f *TiledFetcher) downloadParts(ctx context.Context, directory string,
	artifact provenance.Artifact,
) ([]string, []provenance.SourcePart, error) {
	store := artifactstore.New(filepath.Join(directory, "parts"), f.maxPartBytes)
	downloader := artifactstore.NewDownloader(f.client, store, f.maxPartBytes)
	parts := append([]provenance.SourcePart(nil), artifact.Provenance.SourceParts...)
	paths := make([]string, 0, len(parts))
	for index, part := range parts {
		stored, err := f.downloadPart(ctx, downloader, tileArtifact(artifact, part), index, len(parts))
		if err != nil {
			return nil, nil, err
		}
		if stored.SizeBytes != part.SizeBytes {
			return nil, nil, fmt.Errorf("%w: Earthdata 分片声明与实际大小不一致", domain.ErrProviderUnavailable)
		}
		parts[index].SHA256 = stored.Provenance.SHA256
		paths = append(paths, stored.LocalPath)
	}
	return paths, parts, nil
}

func (f *TiledFetcher) downloadPart(ctx context.Context, downloader ports.ArtifactFetcher,
	artifact provenance.Artifact, index, total int,
) (provenance.Artifact, error) {
	var last error
	for attempt := 1; attempt <= f.maxAttempts; attempt++ {
		stored, err := downloader.Fetch(ctx, artifact)
		if err == nil {
			return stored, nil
		}
		last = err
		if attempt == f.maxAttempts || !retryablePartError(ctx, err) {
			break
		}
		f.logger.WarnContext(ctx, "Earthdata 分片读取失败，准备整片重试",
			"part", index+1, "total", total, "attempt", attempt, "error", err)
		if err = waitPartRetry(ctx, f.retryBackoff*time.Duration(attempt)); err != nil {
			return provenance.Artifact{}, err
		}
	}
	return provenance.Artifact{}, fmt.Errorf("下载 Earthdata 分片 %d/%d: %w", index+1, total, last)
}

func retryablePartError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var providerError *httpclient.ProviderError
	if errors.As(err, &providerError) {
		return providerError.Retryable
	}
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()))
}

func waitPartRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func tileArtifact(parent provenance.Artifact, part provenance.SourcePart) provenance.Artifact {
	source := parent.Provenance
	source.SourceURI, source.SourceRevision = part.Reference, part.Revision
	source.SourceParts, source.SHA256 = nil, ""
	return provenance.Artifact{
		Reference: part.Reference, MediaType: "image/tiff", SizeBytes: part.SizeBytes,
		Provenance: source,
	}
}

func (f *TiledFetcher) saveMosaic(ctx context.Context, artifact provenance.Artifact,
	path string,
) (provenance.Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("打开 Earthdata 拼接栅格: %w", err)
	}
	defer file.Close()
	stored, err := f.saver.Save(ctx, artifact, file)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("保存 Earthdata 拼接栅格: %w", err)
	}
	return stored, nil
}

func appendQualityFlag(values []string, expected string) []string {
	for _, value := range values {
		if value == expected {
			return values
		}
	}
	return append(values, expected)
}
