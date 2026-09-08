package nccs

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// RetainArtifact 在数据库新快照已提交且刷新租约仍持有时回收旧制品及过时下载工作目录。
func (f *Fetcher) RetainArtifact(ctx context.Context, source provenance.Provenance) error {
	if err := f.store.RetainArtifact(ctx, source); err != nil {
		return err
	}
	legacy := source
	legacy.Provider, legacy.Dataset = "NASA Earthdata GIS", "LHASA Hazard Today"
	if err := f.store.RetainArtifact(ctx, legacy); err != nil {
		return err
	}
	entries, err := os.ReadDir(f.config.Directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() || len(entry.Name()) != 64 {
			continue
		}
		if _, err = hex.DecodeString(entry.Name()); err != nil {
			continue
		}
		if err = os.RemoveAll(filepath.Join(f.config.Directory, entry.Name())); err != nil {
			return fmt.Errorf("清理过时 NCCS 下载目录: %w", err)
		}
	}
	return nil
}
