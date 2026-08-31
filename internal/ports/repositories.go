package ports

import (
	"context"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/survival"
)

// Cache 保存可安全重建的临时 JSON 数据。
type Cache interface {
	// Get 将缓存值解码到 destination，未命中时返回 false。
	Get(ctx context.Context, key string, destination any) (bool, error)
	// Set 写入带过期时间的缓存值。
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	// Delete 删除缓存值。
	Delete(ctx context.Context, key string) error
}

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

// HazardAnalysisRefreshLease 表示同一分析族的跨进程刷新租约。
type HazardAnalysisRefreshLease interface {
	// Release 释放刷新租约。
	Release() error
}

// HazardAnalysisRefreshLocker 串行化同一分析族的完整刷新过程。
type HazardAnalysisRefreshLocker interface {
	// LockAnalysisRefresh 获取分析族级刷新租约。
	LockAnalysisRefresh(ctx context.Context, selector hazard.AnalysisSelector) (
		HazardAnalysisRefreshLease, error)
}

// HazardAnalysisWriter 原子保存完整灾害分析并协调覆盖范围身份。
type HazardAnalysisWriter interface {
	// SaveAnalysis 在同一事务保存快照和全部风险区。
	SaveAnalysis(ctx context.Context, snapshot hazard.Snapshot, zones []hazard.RiskZone) error
	// ReconcileAnalysisCoverage 恢复匹配边界的历史结果，并使审计水位内的其他范围退出最新选择。
	ReconcileAnalysisCoverage(ctx context.Context, selector hazard.AnalysisSelector,
		replacement hazard.Coverage, observedAt time.Time) error
}

// HazardAnalysisReader 读取同一处理版本最后成功的完整灾害分析。
type HazardAnalysisReader interface {
	// LatestAnalysis 返回指定灾种、模型和处理版本的最新完整分析。
	LatestAnalysis(ctx context.Context, selector hazard.AnalysisSelector) (
		hazard.Snapshot, []hazard.RiskZone, error)
}

// RiskAssessmentWriter 固化与风险快照绑定的确定性研判。
type RiskAssessmentWriter interface {
	// SaveRiskAssessment 首次写入指定快照和规则版本的不可变研判。
	SaveRiskAssessment(ctx context.Context, snapshot hazard.Snapshot, assessment risk.Assessment) error
}

// WeatherSnapshotWriter 持久化完整监测点天气批次。
type WeatherSnapshotWriter interface {
	// SaveBatch 在同一事务中保存全部监测点，任一点失败则整批回滚。
	SaveBatch(ctx context.Context, snapshots []hazard.WeatherSnapshot) error
}

// WeatherSnapshotReader 读取同一监测点集最后成功的天气批次。
type WeatherSnapshotReader interface {
	// Latest 返回同点集最近完整批次，并保持 points 的输入顺序。
	Latest(ctx context.Context, points []spatial.Point) ([]hazard.WeatherSnapshot, error)
}

// LossBaselineReader 读取损失估算所需的基线数据。
type LossBaselineReader interface {
	// CostBaselines 返回某区域已配置的单位成本。
	CostBaselines(ctx context.Context, regionCode string) ([]loss.CostBaseline, error)
	// Vulnerabilities 返回指定灾种的脆弱性参数。
	Vulnerabilities(ctx context.Context, hazardType string) ([]loss.Vulnerability, error)
	// ExposureBaselines 返回某区域指定类别的最新有效人口或道路基线。
	ExposureBaselines(ctx context.Context, regionCode string, kind loss.ExposureKind) ([]loss.ExposureBaseline, error)
}

// LossBaselineWriter 原子替换一个带版本的损失基线数据集。
type LossBaselineWriter interface {
	// ReplaceBaselineSet 校验并原子替换指定数据集版本的全部基线记录。
	ReplaceBaselineSet(ctx context.Context, set loss.BaselineSet) error
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
