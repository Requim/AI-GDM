package artifactstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

const defaultMaxArtifactBytes = 512 << 20

// Store 将外部制品原子保存到受控目录并计算 SHA-256。
type Store struct {
	root     string
	maxBytes int64
}

// New 创建原子文件制品存储。
func New(root string, maxBytes int64) *Store {
	if maxBytes <= 0 {
		maxBytes = defaultMaxArtifactBytes
	}
	return &Store{root: root, maxBytes: maxBytes}
}

// Save 流式保存制品，成功前不会暴露不完整文件。
func (s *Store) Save(ctx context.Context, artifact provenance.Artifact, source io.Reader) (provenance.Artifact, error) {
	if err := artifact.Validate(); err != nil {
		return provenance.Artifact{}, err
	}
	if err := os.MkdirAll(s.root, 0o750); err != nil {
		return provenance.Artifact{}, fmt.Errorf("创建制品目录: %w", err)
	}
	temporary, err := os.CreateTemp(s.root, ".download-*")
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("创建临时制品: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	size, checksum, err := s.write(ctx, temporary, source)
	if err != nil {
		_ = temporary.Close()
		return provenance.Artifact{}, err
	}
	if err = temporary.Close(); err != nil {
		return provenance.Artifact{}, fmt.Errorf("关闭临时制品: %w", err)
	}
	finalPath := filepath.Join(s.root, checksum[:16]+"-"+safeName(artifact.Reference))
	if err = commitFile(name, finalPath); err != nil {
		return provenance.Artifact{}, err
	}
	artifact.LocalPath = finalPath
	artifact.SizeBytes = size
	artifact.Provenance.SHA256 = checksum
	if err = writeMetadata(finalPath+".metadata.json", artifact); err != nil {
		return provenance.Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) write(ctx context.Context, destination *os.File, source io.Reader) (int64, string, error) {
	digest := sha256.New()
	limited := io.LimitReader(contextReader{ctx: ctx, reader: source}, s.maxBytes+1)
	size, err := io.CopyBuffer(io.MultiWriter(destination, digest), limited, make([]byte, 64<<10))
	if err != nil {
		return 0, "", fmt.Errorf("写入外部制品: %w", err)
	}
	if size > s.maxBytes {
		return 0, "", fmt.Errorf("%w: 制品超过 %d 字节", domain.ErrInvalidInput, s.maxBytes)
	}
	if size == 0 {
		return 0, "", fmt.Errorf("%w: 制品内容为空", domain.ErrInvalidInput)
	}
	if err = destination.Sync(); err != nil {
		return 0, "", fmt.Errorf("同步外部制品: %w", err)
	}
	return size, digestHex(digest), nil
}

func writeMetadata(finalPath string, artifact provenance.Artifact) error {
	payload, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("编码制品来源元数据: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".metadata-*")
	if err != nil {
		return fmt.Errorf("创建来源元数据临时文件: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入制品来源元数据: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步制品来源元数据: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("关闭制品来源元数据: %w", err)
	}
	return commitFile(name, finalPath)
}

func commitFile(temporary, finalPath string) error {
	if _, err := os.Stat(finalPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标制品: %w", err)
	}
	if err := os.Rename(temporary, finalPath); err != nil {
		return fmt.Errorf("提交外部制品: %w", err)
	}
	return nil
}

func safeName(reference string) string {
	parsed, err := url.Parse(reference)
	name := filepath.Base(reference)
	if err == nil && parsed.Path != "" {
		name = filepath.Base(parsed.Path)
	}
	var builder strings.Builder
	for _, value := range name {
		if unicode.IsLetter(value) || unicode.IsDigit(value) || strings.ContainsRune("._-", value) {
			builder.WriteRune(value)
		}
	}
	if builder.Len() == 0 || builder.String() == "." {
		return "artifact.bin"
	}
	return builder.String()
}

func digestHex(value hash.Hash) string {
	return hex.EncodeToString(value.Sum(nil))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}
