// Package evacuation 编排避险设施搜索与风险区安全筛选。
package evacuation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	domainevacuation "github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

// SearchInput 描述一次带风险区过滤的避险设施搜索。
type SearchInput struct {
	HazardType   hazard.Type                   `json:"hazardType"`
	Center       spatial.Point                 `json:"center"`
	Kind         domainevacuation.FacilityType `json:"kind"`
	RadiusMeters int                           `json:"radiusMeters"`
}

// ExcludedFacility 记录因落入风险区而被排除的候选设施。
type ExcludedFacility struct {
	Facility domainevacuation.Facility `json:"facility"`
	ZoneIDs  []string                  `json:"riskZoneIds"`
	Reason   string                    `json:"reason"`
}

// SearchResult 保存安全候选、排除明细及其风险快照。
type SearchResult struct {
	Snapshot    hazard.Snapshot             `json:"snapshot"`
	Facilities  []domainevacuation.Facility `json:"facilities"`
	Excluded    []ExcludedFacility          `json:"excluded"`
	Limitations []string                    `json:"limitations"`
}

// FacilitySearcher 是地图适配器使用的安全设施搜索用例边界。
type FacilitySearcher interface {
	// Search 查询真实 POI，并过滤落入最新完整风险区的候选点。
	Search(ctx context.Context, input SearchInput) (SearchResult, error)
}

// Service 编排地图 POI 查询和风险区空间过滤。
type Service struct {
	places ports.PlaceFinder
	risks  ports.LatestRiskReader
}

var _ FacilitySearcher = (*Service)(nil)

// NewService 创建避险设施搜索服务。
func NewService(places ports.PlaceFinder, risks ports.LatestRiskReader) (*Service, error) {
	if places == nil || risks == nil {
		return nil, fmt.Errorf("%w: 避险设施搜索依赖为空", domain.ErrInvalidInput)
	}
	return &Service{places: places, risks: risks}, nil
}

// Search 查询候选设施，并在应用层执行风险区过滤。
func (s *Service) Search(ctx context.Context, input SearchInput) (SearchResult, error) {
	if err := validateInput(input); err != nil {
		return SearchResult{}, err
	}
	snapshot, zones, err := s.risks.LatestRisk(ctx, input.HazardType)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return SearchResult{}, fmt.Errorf("%w: 最新风险快照不存在: %w", domain.ErrInsufficientData, err)
		}
		return SearchResult{}, fmt.Errorf("读取最新风险区: %w", err)
	}
	if err = validateRiskData(input.HazardType, snapshot, zones); err != nil {
		return SearchResult{}, err
	}
	candidates, err := s.places.FindNearby(ctx, input.Center, input.Kind, input.RadiusMeters)
	if err != nil {
		return SearchResult{}, fmt.Errorf("搜索 %s 避险设施: %w", input.Kind, err)
	}
	if err := validateProviderResultCount("设施供应商", len(candidates), MaxFacilityProviderCandidates); err != nil {
		return SearchResult{}, err
	}
	return filterCandidates(snapshot, zones, input.Kind, candidates)
}

func validateInput(input SearchInput) error {
	hazardValue := strings.TrimSpace(string(input.HazardType))
	if !validHazardType(hazardValue) || hazardValue != string(input.HazardType) {
		return fmt.Errorf("%w: 灾种标识无效", domain.ErrInvalidInput)
	}
	if err := input.Center.Validate(); err != nil {
		return fmt.Errorf("搜索中心坐标: %w", err)
	}
	if input.RadiusMeters <= 0 || input.RadiusMeters > 50_000 {
		return fmt.Errorf("%w: 搜索半径必须在 1 至 50000 米之间", domain.ErrInvalidInput)
	}
	switch input.Kind {
	case domainevacuation.FacilityShelter, domainevacuation.FacilityHospital, domainevacuation.FacilityTransport:
		return nil
	default:
		return fmt.Errorf("%w: 设施类型无效", domain.ErrInvalidInput)
	}
}

func validHazardType(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validateRiskData(expected hazard.Type, snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	if snapshot.ID == "" || snapshot.HazardType != expected {
		return fmt.Errorf("%w: 风险快照与搜索灾种不一致", domain.ErrInsufficientData)
	}
	if err := validateRiskValidTo(snapshot, "设施"); err != nil {
		return err
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return fmt.Errorf("%w: 风险快照当前不可用于设施筛选", domain.ErrInsufficientData)
	}
	if zones == nil {
		return fmt.Errorf("%w: 风险区数据为空，拒绝返回未过滤设施", domain.ErrInsufficientData)
	}
	for _, zone := range zones {
		if zone.ID == "" || zone.SnapshotID != snapshot.ID {
			return fmt.Errorf("%w: 风险区所属快照无效", domain.ErrInsufficientData)
		}
		if err := zone.Geometry.ValidateArea(); err != nil {
			return fmt.Errorf("%w: 校验风险区 %s 几何: %w", domain.ErrInsufficientData, zone.ID, err)
		}
	}
	return nil
}

func validateRiskValidTo(snapshot hazard.Snapshot, subject string) error {
	if snapshot.ValidTo.IsZero() || snapshot.Source.ValidTo.IsZero() {
		return fmt.Errorf("%w: 风险快照缺少有效期，拒绝筛选%s", domain.ErrInsufficientData, subject)
	}
	if _, offset := snapshot.ValidTo.Zone(); offset != 0 {
		return fmt.Errorf("%w: 风险快照有效期必须使用 UTC，拒绝筛选%s", domain.ErrInsufficientData, subject)
	}
	if _, offset := snapshot.Source.ValidTo.Zone(); offset != 0 {
		return fmt.Errorf("%w: 风险来源有效期必须使用 UTC，拒绝筛选%s", domain.ErrInsufficientData, subject)
	}
	if !snapshot.ValidTo.Equal(snapshot.Source.ValidTo) {
		return fmt.Errorf("%w: 风险快照与来源有效期不一致，拒绝筛选%s", domain.ErrInsufficientData, subject)
	}
	return nil
}

func filterCandidates(snapshot hazard.Snapshot, zones []hazard.RiskZone,
	kind domainevacuation.FacilityType, candidates []domainevacuation.Facility,
) (SearchResult, error) {
	result := SearchResult{
		Snapshot:   snapshot,
		Facilities: make([]domainevacuation.Facility, 0, len(candidates)),
		Excluded:   make([]ExcludedFacility, 0),
		Limitations: []string{
			"设施结果仅覆盖地图供应商本次返回的有界候选集，空结果不代表附近不存在可用设施",
		},
	}
	if limitation := riskFreshnessLimitation(snapshot, "设施筛选结果"); limitation != "" {
		result.Limitations = append(result.Limitations, limitation)
	}
	for index, candidate := range candidates {
		if err := validateCandidate(kind, candidate); err != nil {
			context := fmt.Sprintf("校验第 %d 个设施候选", index+1)
			return SearchResult{}, wrapUnsafeProviderResult(context, err)
		}
		zoneIDs, err := matchingZones(candidate.Location, zones)
		if err != nil {
			return SearchResult{}, fmt.Errorf("判断设施 %s 风险区关系: %w", candidate.ID, err)
		}
		if len(zoneIDs) > 0 {
			result.Excluded = append(result.Excluded, ExcludedFacility{
				Facility: candidate, ZoneIDs: zoneIDs, Reason: "设施坐标落入风险区",
			})
			continue
		}
		result.Facilities = append(result.Facilities, candidate)
	}
	return result, nil
}

func validateCandidate(kind domainevacuation.FacilityType, candidate domainevacuation.Facility) error {
	if candidate.Type != kind {
		return fmt.Errorf("%w: 供应商返回设施类型 %q，与请求 %q 不一致", domain.ErrInvalidInput, candidate.Type, kind)
	}
	if err := candidate.Location.Validate(); err != nil {
		return fmt.Errorf("设施坐标: %w", err)
	}
	return nil
}

func matchingZones(point spatial.Point, zones []hazard.RiskZone) ([]string, error) {
	matched := make([]string, 0)
	for _, zone := range zones {
		inside, err := zone.Geometry.ContainsPoint(point)
		if err != nil {
			return nil, fmt.Errorf("%w: 风险区 %s 几何判断失败: %w", domain.ErrInsufficientData, zone.ID, err)
		}
		if inside {
			matched = append(matched, zone.ID)
		}
	}
	return matched, nil
}
