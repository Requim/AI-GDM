package collection

import (
	"context"
	"log/slog"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/ports"
)

// WithSource 绑定组合根选择的数据源身份，避免应用层依赖具体供应商实现。
func (c *LHASACollector) WithSource(provider, dataset string) *LHASACollector {
	c.providerName, c.datasetName = provider, dataset
	return c
}

// WithRetention 启用提交后原始制品保留策略；清理失败不撤销已提交的新快照。
func (c *LHASACollector) WithRetention(retainer ports.ArtifactRetainer, logger *slog.Logger) *LHASACollector {
	c.retainer, c.logger = retainer, logger
	return c
}

func (c *LHASACollector) prepareCoverage(ctx context.Context, boundary hazard.ProcessingBoundary) error {
	if c.retainer != nil {
		// 单快照模式直到替代分析提交才退出旧范围，失败仍保留旧快照。
		return nil
	}
	return c.reconcileCoverage(ctx, boundary)
}

func (c *LHASACollector) retainArtifact(ctx context.Context, snapshot hazard.Snapshot) {
	if c.retainer == nil {
		return
	}
	if err := c.retainer.RetainArtifact(ctx, snapshot.Source); err != nil && c.logger != nil {
		c.logger.WarnContext(ctx, "新快照已提交，旧原始制品清理待重试", "snapshot_id", snapshot.ID, "error", err)
	}
}
