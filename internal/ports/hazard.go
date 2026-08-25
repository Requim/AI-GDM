package ports

import (
	"context"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
)

// LatestRiskReader 读取某灾种最新的完整风险分析。
type LatestRiskReader interface {
	// LatestRisk 在一致性视图中返回快照及其全部风险区。
	LatestRisk(ctx context.Context, hazardType hazard.Type) (
		hazard.Snapshot, []hazard.RiskZone, error)
}

// RiskDetailReader 读取指定快照的完整风险分析。
type RiskDetailReader interface {
	// RiskDetail 在一致性视图中返回快照及其全部风险区。
	RiskDetail(ctx context.Context, snapshotID string) (
		hazard.Snapshot, []hazard.RiskZone, error)
}

// HazardRefresher 刷新单一灾种的实时数据并返回已持久化结果。
type HazardRefresher interface {
	// Refresh 保留具体数据源的最后成功结果回退语义。
	Refresh(ctx context.Context) (hazard.Snapshot, []hazard.RiskZone, error)
}

// RiskEvaluator 对完整快照执行确定性风险研判。
type RiskEvaluator interface {
	// Evaluate 不得调用大模型或修改输入数据。
	Evaluate(input risk.Input) (risk.Assessment, error)
}
