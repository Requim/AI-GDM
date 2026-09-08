package artifactstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// Find 复用同一来源强版本的完整原始制品，并重新验证文件摘要。
func (s *Store) Find(ctx context.Context, requested provenance.Artifact) (provenance.Artifact, bool, error) {
	values, err := s.metadata(ctx)
	if err != nil {
		return provenance.Artifact{}, false, err
	}
	for _, value := range values {
		if value.Reference != requested.Reference || value.Provenance.SourceRevision != requested.Provenance.SourceRevision ||
			value.Provenance.Provider != requested.Provenance.Provider || value.Provenance.Dataset != requested.Provenance.Dataset ||
			!value.Provenance.PublishedAt.Equal(requested.Provenance.PublishedAt) {
			continue
		}
		if value.SizeBytes != requested.SizeBytes {
			return provenance.Artifact{}, false, fmt.Errorf("%w: 缓存制品长度冲突", domain.ErrInvalidInput)
		}
		file, err := os.Open(value.LocalPath)
		if err != nil {
			return provenance.Artifact{}, false, err
		}
		digest, size, err := artifactDigest(ctx, file)
		_ = file.Close()
		if err != nil {
			return provenance.Artifact{}, false, err
		}
		if digest != value.Provenance.SHA256 || size != value.SizeBytes {
			return provenance.Artifact{}, false, fmt.Errorf("%w: 缓存制品摘要不一致", domain.ErrInvalidInput)
		}
		return value, true, nil
	}
	return provenance.Artifact{}, false, nil
}

// RetainArtifact 仅在新快照提交后调用，删除同源模型的其他原始制品。
func (s *Store) RetainArtifact(ctx context.Context, source provenance.Provenance) error {
	if len(source.SHA256) != 64 || source.Provider == "" || source.Dataset == "" {
		return fmt.Errorf("%w: 保留制品身份无效", domain.ErrInvalidInput)
	}
	values, err := s.metadata(ctx)
	if err != nil {
		return err
	}
	if !retainedArtifactExists(values, source.SHA256) {
		return fmt.Errorf("%w: 当前原始制品不存在，拒绝清理旧制品", domain.ErrNotFound)
	}
	for _, value := range values {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if value.Provenance.SHA256 == source.SHA256 || !sameRetentionFamily(value.Provenance, source) {
			continue
		}
		if err = os.Remove(value.LocalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理旧原始制品: %w", err)
		}
		if err = os.Remove(value.LocalPath + ".metadata.json"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理旧制品描述: %w", err)
		}
	}
	return nil
}

func sameRetentionFamily(left, right provenance.Provenance) bool {
	return left.Provider == right.Provider && left.Dataset == right.Dataset
}

func (s *Store) metadata(ctx context.Context) ([]provenance.Artifact, error) {
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var values []provenance.Artifact
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !strings.HasSuffix(entry.Name(), ".metadata.json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		value, err := s.readMetadata(entry.Name())
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) readMetadata(name string) (provenance.Artifact, error) {
	file, err := os.Open(filepath.Join(s.root, name))
	if err != nil {
		return provenance.Artifact{}, err
	}
	defer file.Close()
	var value provenance.Artifact
	if err = json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&value); err != nil {
		return value, err
	}
	if len(value.Provenance.SHA256) != 64 {
		return value, fmt.Errorf("%w: 制品摘要无效", domain.ErrInvalidInput)
	}
	if _, err = hex.DecodeString(value.Provenance.SHA256); err != nil {
		return value, fmt.Errorf("%w: 制品摘要编码无效", domain.ErrInvalidInput)
	}
	expected := filepath.Join(s.root, value.Provenance.SHA256[:16]+"-"+safeName(value.Reference))
	if filepath.Clean(value.LocalPath) != filepath.Clean(expected) || filepath.Base(expected)+".metadata.json" != name {
		return value, fmt.Errorf("%w: 制品清理路径越界", domain.ErrInvalidInput)
	}
	info, err := os.Lstat(expected)
	if err != nil && !os.IsNotExist(err) {
		return value, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return value, fmt.Errorf("%w: 制品不是普通文件", domain.ErrInvalidInput)
	}
	return value, nil
}

func retainedArtifactExists(values []provenance.Artifact, digest string) bool {
	for _, value := range values {
		if value.Provenance.SHA256 != digest {
			continue
		}
		info, err := os.Lstat(value.LocalPath)
		if err == nil && info.Mode().IsRegular() && info.Size() == value.SizeBytes {
			return true
		}
	}
	return false
}
