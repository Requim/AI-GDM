package ports

import (
	"context"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
)

// HazardAuthorityLimits 要求仓储在加载风险区明细或几何前完成负载边界判断。
type HazardAuthorityLimits struct {
	MaxZones          int
	MaxGeometryPoints int
	MaxGeometryBytes  int
}

// HazardZoneSummary 是不含几何、行政区或来源 URL 的风险区绑定摘要。
type HazardZoneSummary struct {
	ID         string
	SnapshotID string
	Level      hazard.RiskLevel
}

// HazardAuthorityRead 是仓储前置限量后返回的不可变评估和无几何摘要。
type HazardAuthorityRead struct {
	Snapshot            hazard.Snapshot
	Assessment          risk.Assessment
	Zones               []HazardZoneSummary
	TotalZoneCount      int
	TotalGeometryPoints int
	TotalGeometryBytes  int
}

// HazardAuthorityReader 按快照读取已持久化评估；实现必须在读取几何前执行 limits。
type HazardAuthorityReader interface {
	ReadAuthority(context.Context, string, HazardAuthorityLimits) (HazardAuthorityRead, error)
}

// RiskAuthorityReuser 判断已固化风险权威是否与本次完整分析完全一致。
// 返回 true 时调用方必须复用既有空间分析与风险评估，不得重新计算。
type RiskAuthorityReuser interface {
	ReuseRiskAuthority(context.Context, hazard.Snapshot, []hazard.RiskZone) (bool, error)
}
