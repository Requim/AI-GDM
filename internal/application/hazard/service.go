package hazard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

// RiskResult 是风险接口共享的应用层输出。
type RiskResult struct {
	Snapshot   hazarddomain.Snapshot   `json:"snapshot"`
	Zones      []hazarddomain.RiskZone `json:"zones"`
	Assessment risk.Assessment         `json:"assessment"`
}

// MapRiskResult 是浏览器地图使用的有界风险输出。
type MapRiskResult struct {
	RiskResult
	TotalZoneCount int
}

// RiskService 是 HTTP 等驱动适配器使用的风险预警用例边界。
type RiskService interface {
	// Latest 返回某灾种最新的完整风险分析。
	Latest(ctx context.Context, hazardType hazarddomain.Type) (RiskResult, error)
	// Get 返回某灾种指定快照的完整风险分析。
	Get(ctx context.Context, hazardType hazarddomain.Type, snapshotID string) (RiskResult, error)
	// Refresh 刷新某灾种并返回刷新后的完整风险分析。
	Refresh(ctx context.Context, hazardType hazarddomain.Type) (RiskResult, error)
	// LatestMap 在仓储加载风险区前应用数量上限。
	LatestMap(ctx context.Context, hazardType hazarddomain.Type, maxZones int) (MapRiskResult, error)
}

// Service 编排多灾种风险查询、详情和刷新。
type Service struct {
	latest    ports.LatestRiskReader
	mapLatest ports.LatestMapRiskReader
	detail    ports.RiskDetailReader
	registry  *Registry
	clock     ports.Clock
}

var _ RiskService = (*Service)(nil)

// NewService 创建风险预警应用服务。
func NewService(latest ports.LatestRiskReader, mapLatest ports.LatestMapRiskReader,
	detail ports.RiskDetailReader,
	registry *Registry, clock ports.Clock,
) (*Service, error) {
	if nilDependency(latest) || nilDependency(mapLatest) || nilDependency(detail) ||
		registry == nil || nilDependency(clock) {
		return nil, fmt.Errorf("%w: 风险预警服务依赖为空", domain.ErrInvalidInput)
	}
	return &Service{latest: latest, mapLatest: mapLatest, detail: detail,
		registry: registry, clock: clock}, nil
}

// Latest 返回某灾种最新完整分析及当前时刻的确定性研判。
func (s *Service) Latest(ctx context.Context, hazardType hazarddomain.Type) (RiskResult, error) {
	provider, err := s.registry.Resolve(hazardType)
	if err != nil {
		return RiskResult{}, err
	}
	snapshot, zones, err := s.latest.LatestRisk(ctx, hazardType)
	if err != nil {
		return RiskResult{}, fmt.Errorf("读取灾种 %s 最新风险: %w", hazardType, err)
	}
	return s.buildResult(provider, hazardType, snapshot, zones)
}

// LatestMap 返回经过仓储前置数量限制的最新地图风险分析。
func (s *Service) LatestMap(ctx context.Context, hazardType hazarddomain.Type,
	maxZones int,
) (MapRiskResult, error) {
	if maxZones <= 0 {
		return MapRiskResult{}, fmt.Errorf("%w: 地图风险区上限无效", domain.ErrInvalidInput)
	}
	provider, err := s.registry.Resolve(hazardType)
	if err != nil {
		return MapRiskResult{}, err
	}
	read, err := s.mapLatest.LatestMapRisk(ctx, hazardType, maxZones)
	if err != nil {
		return MapRiskResult{}, fmt.Errorf("读取灾种 %s 地图风险: %w", hazardType, err)
	}
	if read.TotalZoneCount != len(read.Zones) || read.TotalZoneCount > maxZones {
		return MapRiskResult{}, fmt.Errorf("%w: 地图风险读取结果不完整", domain.ErrInsufficientData)
	}
	result, err := s.buildResult(provider, hazardType, read.Snapshot, read.Zones)
	if err != nil {
		return MapRiskResult{}, err
	}
	return MapRiskResult{RiskResult: result, TotalZoneCount: read.TotalZoneCount}, nil
}

// Get 返回指定灾种和快照的完整风险详情。
func (s *Service) Get(ctx context.Context, hazardType hazarddomain.Type,
	snapshotID string,
) (RiskResult, error) {
	if err := validateSnapshotID(snapshotID); err != nil {
		return RiskResult{}, err
	}
	provider, err := s.registry.Resolve(hazardType)
	if err != nil {
		return RiskResult{}, err
	}
	snapshot, zones, err := s.detail.RiskDetail(ctx, snapshotID)
	if err != nil {
		return RiskResult{}, fmt.Errorf("读取风险快照 %s: %w", snapshotID, err)
	}
	if snapshot.ID != snapshotID {
		return RiskResult{}, fmt.Errorf("%w: 返回快照标识 %q 与请求 %q 不一致",
			domain.ErrInvalidInput, snapshot.ID, snapshotID)
	}
	return s.buildResult(provider, hazardType, snapshot, zones)
}

// Refresh 触发指定灾种刷新并返回刷新后研判；数据源降级由专用刷新器负责。
func (s *Service) Refresh(ctx context.Context, hazardType hazarddomain.Type) (RiskResult, error) {
	provider, err := s.registry.Resolve(hazardType)
	if err != nil {
		return RiskResult{}, err
	}
	snapshot, zones, err := provider.Refresh(ctx)
	if err != nil {
		return RiskResult{}, fmt.Errorf("刷新灾种 %s: %w", hazardType, err)
	}
	return s.buildResult(provider, hazardType, snapshot, zones)
}

func (s *Service) buildResult(provider *HazardProvider, expected hazarddomain.Type,
	snapshot hazarddomain.Snapshot, zones []hazarddomain.RiskZone,
) (RiskResult, error) {
	if snapshot.HazardType != expected {
		return RiskResult{}, fmt.Errorf("%w: 返回快照灾种 %q 与请求 %q 不一致",
			domain.ErrInvalidInput, snapshot.HazardType, expected)
	}
	evaluatedAt := s.clock.Now()
	if evaluatedAt.IsZero() || !isUTC(evaluatedAt) {
		return RiskResult{}, fmt.Errorf("%w: 风险研判时间必须是 UTC", domain.ErrInvalidInput)
	}
	assessment, err := provider.evaluate(risk.Input{
		Snapshot: snapshot, Zones: zones, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		return RiskResult{}, fmt.Errorf("研判风险快照 %s: %w", snapshot.ID, err)
	}
	if err = validateAssessment(snapshot, assessment, evaluatedAt); err != nil {
		return RiskResult{}, err
	}
	return RiskResult{Snapshot: snapshot, Zones: nonNilZones(zones), Assessment: assessment}, nil
}

func validateSnapshotID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return fmt.Errorf("%w: 风险快照标识无效", domain.ErrInvalidInput)
	}
	return nil
}

func validateAssessment(snapshot hazarddomain.Snapshot, value risk.Assessment,
	evaluatedAt time.Time,
) error {
	if value.ID == "" || value.HazardType != snapshot.HazardType || value.SnapshotID != snapshot.ID {
		return fmt.Errorf("%w: 风险研判与快照不一致", domain.ErrInvalidInput)
	}
	if !value.EvaluatedAt.Equal(evaluatedAt) || !isUTC(value.EvaluatedAt) {
		return fmt.Errorf("%w: 风险研判时间与请求不一致", domain.ErrInvalidInput)
	}
	if value.RuleVersion == "" || value.Status == "" || value.DataStatus == "" ||
		value.Confidence.Level == "" {
		return fmt.Errorf("%w: 风险研判结果不完整", domain.ErrInvalidInput)
	}
	return nil
}

func nonNilZones(values []hazarddomain.RiskZone) []hazarddomain.RiskZone {
	if values == nil {
		return []hazarddomain.RiskZone{}
	}
	return values
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
