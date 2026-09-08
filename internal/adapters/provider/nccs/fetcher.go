package nccs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// RevisionVerifier 约束完整文件下载前后的来源版本检查。
type RevisionVerifier interface {
	VerifyCurrent(context.Context, provenance.Artifact) error
}

// FetchConfig 配置持久断点目录和有限并发。
type FetchConfig struct {
	Directory  string
	ChunkBytes int64
	Workers    int
	Attempts   int
	Backoff    time.Duration
	Logger     *slog.Logger
}

// Fetcher 获取完整全球文件；只有字节、分段摘要和源版本均通过才提交原始制品。
type Fetcher struct {
	client   *httpclient.Client
	verifier RevisionVerifier
	store    *artifactstore.Store
	config   FetchConfig
}

// NewFetcher 创建可跨进程重启恢复的完整文件下载器；调用方持有刷新租约。
func NewFetcher(client *httpclient.Client, verifier RevisionVerifier,
	store *artifactstore.Store, config FetchConfig,
) (*Fetcher, error) {
	if config.ChunkBytes == 0 {
		config.ChunkBytes = 1 << 20
	}
	if config.Workers == 0 {
		config.Workers = 4
	}
	if config.Attempts == 0 {
		config.Attempts = 6
	}
	if config.Backoff == 0 {
		config.Backoff = time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil || verifier == nil || store == nil || config.Directory == "" ||
		config.ChunkBytes < 1024 || config.ChunkBytes > 8<<20 || config.Workers < 1 ||
		config.Workers > 4 || config.Attempts < 1 || config.Attempts > 6 || config.Backoff < 0 {
		return nil, fmt.Errorf("%w: NCCS 完整下载配置无效", domain.ErrInvalidInput)
	}
	return &Fetcher{client: client, verifier: verifier, store: store, config: config}, nil
}

// Fetch 校验同版本已有制品，或恢复有界分段下载，失败不删除断点和旧快照。
func (f *Fetcher) Fetch(ctx context.Context, artifact provenance.Artifact) (provenance.Artifact, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	if err := artifact.Validate(); err != nil {
		return provenance.Artifact{}, err
	}
	if artifact.SizeBytes < 1024 || artifact.SizeBytes > MaxFileBytes ||
		!httpclient.IsStrongETag(artifact.Provenance.SourceRevision) {
		return provenance.Artifact{}, fmt.Errorf("%w: NCCS 文件大小或修订无效", domain.ErrInvalidInput)
	}
	if err := f.verifier.VerifyCurrent(ctx, artifact); err != nil {
		return provenance.Artifact{}, err
	}
	if stored, ok, err := f.store.Find(ctx, artifact); err != nil || ok {
		return stored, err
	}
	directory := filepath.Join(f.config.Directory, downloadKey(artifact, f.config.ChunkBytes))
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return provenance.Artifact{}, fmt.Errorf("创建 NCCS 断点目录: %w", err)
	}
	if err := f.download(ctx, artifact, directory); err != nil {
		return provenance.Artifact{}, err
	}
	if err := f.verifier.VerifyCurrent(ctx, artifact); err != nil {
		return provenance.Artifact{}, err
	}
	stored, err := f.assemble(ctx, artifact, directory)
	if err == nil {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			f.config.Logger.WarnContext(ctx, "NCCS 已完成断点清理失败", "error", cleanupErr)
		}
	}
	return stored, err
}

func downloadKey(artifact provenance.Artifact, chunkBytes int64) string {
	payload := fmt.Sprintf("%s|%s|%d|%d", artifact.Reference,
		artifact.Provenance.SourceRevision, artifact.SizeBytes, chunkBytes)
	value := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(value[:])
}

func (f *Fetcher) download(ctx context.Context, artifact provenance.Artifact, directory string) error {
	group, workCtx := errgroup.WithContext(ctx)
	group.SetLimit(f.config.Workers)
	count := (artifact.SizeBytes + f.config.ChunkBytes - 1) / f.config.ChunkBytes
	for index := int64(0); index < count; index++ {
		if workCtx.Err() != nil {
			break
		}
		group.Go(func() error { return f.block(workCtx, artifact, directory, index) })
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("获取 NCCS 完整文件: %w", err)
	}
	return ctx.Err()
}

func (f *Fetcher) assemble(ctx context.Context, artifact provenance.Artifact, directory string) (provenance.Artifact, error) {
	temporary, err := os.CreateTemp(directory, ".assembled-")
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("创建 NCCS 组装文件: %w", err)
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	for index, offset := int64(0), int64(0); offset < artifact.SizeBytes; index, offset = index+1, offset+f.config.ChunkBytes {
		length := min(f.config.ChunkBytes, artifact.SizeBytes-offset)
		if err = appendVerifiedBlock(ctx, temporary, blockPath(directory, index), length); err != nil {
			return provenance.Artifact{}, err
		}
	}
	if err = temporary.Sync(); err != nil {
		return provenance.Artifact{}, fmt.Errorf("同步 NCCS 完整文件: %w", err)
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return provenance.Artifact{}, err
	}
	artifact.Provenance.FetchedAt = time.Now().UTC()
	artifact.Provenance.QualityFlags = append(artifact.Provenance.QualityFlags, "complete_file_range_verified")
	return f.store.Save(ctx, artifact, temporary)
}
