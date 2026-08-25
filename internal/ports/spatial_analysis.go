package ports

import (
	"context"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

// SpatialAnalysisExecutor 原子计算并保存一次风险区域空间分析。
type SpatialAnalysisExecutor interface {
	// Execute 锁定指定快照的一致风险区集合，计算并持久化空间结果。
	Execute(ctx context.Context, snapshotID string, calculatedAt time.Time) (
		spatialanalysis.Analysis, error)
}

// SpatialAnalysisReader 读取已完成且可审计的空间分析。
type SpatialAnalysisReader interface {
	// Get 按分析标识读取结果。
	Get(ctx context.Context, id string) (spatialanalysis.Analysis, error)
	// LatestBySnapshot 返回某灾害快照最新完成的空间分析。
	LatestBySnapshot(ctx context.Context, snapshotID string) (spatialanalysis.Analysis, error)
}
