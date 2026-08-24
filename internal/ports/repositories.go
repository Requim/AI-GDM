package ports

import (
	"context"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/survival"
)

// HazardSnapshotWriter 持久化灾害快照元数据。
type HazardSnapshotWriter interface {
	// SaveSnapshot 原子保存快照元数据。
	SaveSnapshot(ctx context.Context, snapshot hazard.Snapshot) error
}

// HazardSnapshotReader 读取灾害快照元数据。
type HazardSnapshotReader interface {
	// Latest 返回指定灾种的最新可用快照。
	Latest(ctx context.Context, hazardType hazard.Type) (hazard.Snapshot, error)
	// GetSnapshot 按标识读取快照。
	GetSnapshot(ctx context.Context, id string) (hazard.Snapshot, error)
}

// RiskZoneWriter 持久化快照对应的风险区。
type RiskZoneWriter interface {
	// SaveZones 原子替换某个快照的风险区。
	SaveZones(ctx context.Context, snapshotID string, zones []hazard.RiskZone) error
}

// RiskZoneReader 读取快照对应的风险区。
type RiskZoneReader interface {
	// ZonesBySnapshot 返回某快照的全部风险区。
	ZonesBySnapshot(ctx context.Context, snapshotID string) ([]hazard.RiskZone, error)
}

// LossBaselineReader 读取损失估算所需的基线数据。
type LossBaselineReader interface {
	// CostBaselines 返回某区域已配置的单位成本。
	CostBaselines(ctx context.Context, regionCode string) ([]loss.CostBaseline, error)
	// Vulnerabilities 返回指定灾种的脆弱性参数。
	Vulnerabilities(ctx context.Context, hazardType string) ([]loss.Vulnerability, error)
}

// LossAssessmentWriter 保存可回放的损失评估。
type LossAssessmentWriter interface {
	// SaveAssessment 保存评估结果及其输入引用。
	SaveAssessment(ctx context.Context, assessment loss.Assessment) error
}

// LossAssessmentReader 读取已完成的损失评估。
type LossAssessmentReader interface {
	// GetAssessment 按标识读取评估结果。
	GetAssessment(ctx context.Context, id string) (loss.Assessment, error)
}

// HistoricalEventReader 读取匿名事件级历史记录。
type HistoricalEventReader interface {
	// ListEvents 返回可回放的历史事件。
	ListEvents(ctx context.Context) ([]survival.HistoricalEvent, error)
}

// RescueScenarioReader 读取不含个人信息的合成搜救场景。
type RescueScenarioReader interface {
	// GetScenario 返回不含个人信息的合成场景。
	GetScenario(ctx context.Context, id string) (survival.Scenario, error)
}
