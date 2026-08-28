package evacuation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	domainevacuation "github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

// RouteSearchInput 描述一次带风险区排除和安全排序的路线搜索。
type RouteSearchInput struct {
	HazardType      hazard.Type                 `json:"hazardType"`
	Origin          spatial.Point               `json:"origin"`
	Destination     spatial.Point               `json:"destination"`
	Mode            domainevacuation.TravelMode `json:"mode"`
	OriginCity      string                      `json:"originCity,omitempty"`
	DestinationCity string                      `json:"destinationCity,omitempty"`
}

// ExcludedRoute 记录因穿越已知风险区而没有进入调度候选的路线。
type ExcludedRoute struct {
	Route   domainevacuation.Route `json:"route"`
	ZoneIDs []string               `json:"riskZoneIds"`
	Reason  string                 `json:"reason"`
}

// RouteSearchResult 保存过滤后的路线、排除明细和风险快照。
type RouteSearchResult struct {
	Snapshot           hazard.Snapshot          `json:"snapshot"`
	Routes             []domainevacuation.Route `json:"routes"`
	Excluded           []ExcludedRoute          `json:"excluded"`
	RiskScoreAvailable bool                     `json:"riskScoreAvailable"`
	Limitations        []string                 `json:"limitations"`
}

// RouteSafetySearcher 是 HTTP 等驱动适配器使用的安全路线用例边界。
type RouteSafetySearcher interface {
	// SearchRoutes 规划候选路线并排除穿越最新完整风险区的路线。
	SearchRoutes(ctx context.Context, input RouteSearchInput) (RouteSearchResult, error)
}

// SafetyService 编排路线供应商、风险区读取和确定性安全排序。
type SafetyService struct {
	planner ports.RoutePlanner
	risks   ports.LatestRiskReader
}

var _ RouteSafetySearcher = (*SafetyService)(nil)

// NewRouteSafetyService 创建路线安全评估服务。
func NewRouteSafetyService(planner ports.RoutePlanner, risks ports.LatestRiskReader) (*SafetyService, error) {
	if planner == nil || risks == nil {
		return nil, fmt.Errorf("%w: 路线安全服务依赖为空", domain.ErrInvalidInput)
	}
	return &SafetyService{planner: planner, risks: risks}, nil
}

// SearchRoutes 获取供应商候选路线，并按风险、时间、距离进行稳定排序。
func (s *SafetyService) SearchRoutes(ctx context.Context, input RouteSearchInput) (RouteSearchResult, error) {
	if err := validateRouteSearchInput(input); err != nil {
		return RouteSearchResult{}, err
	}
	snapshot, zones, err := s.risks.LatestRisk(ctx, input.HazardType)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return RouteSearchResult{}, fmt.Errorf("%w: 最新路线风险区不存在: %w", domain.ErrInsufficientData, err)
		}
		return RouteSearchResult{}, fmt.Errorf("读取路线风险区: %w", err)
	}
	if err := validateRouteRiskData(input.HazardType, snapshot, zones); err != nil {
		return RouteSearchResult{}, err
	}
	candidates, err := s.plan(ctx, input)
	if err != nil {
		return RouteSearchResult{}, fmt.Errorf("规划 %s 路线: %w", input.Mode, err)
	}
	if err := validateProviderResultCount("路线供应商", len(candidates), MaxRouteProviderCandidates); err != nil {
		return RouteSearchResult{}, err
	}
	return evaluateRoutes(input, snapshot, zones, candidates)
}

func validateRouteSearchInput(input RouteSearchInput) error {
	hazardValue := strings.TrimSpace(string(input.HazardType))
	if !validHazardType(hazardValue) || hazardValue != string(input.HazardType) {
		return fmt.Errorf("%w: 灾种标识无效", domain.ErrInvalidInput)
	}
	if err := input.Origin.Validate(); err != nil {
		return fmt.Errorf("路线起点: %w", err)
	}
	if err := input.Destination.Validate(); err != nil {
		return fmt.Errorf("路线终点: %w", err)
	}
	switch input.Mode {
	case domainevacuation.TravelDriving, domainevacuation.TravelWalking:
		if input.OriginCity != "" || input.DestinationCity != "" {
			return fmt.Errorf("%w: 非公交路线不需要城市编码", domain.ErrInvalidInput)
		}
	case domainevacuation.TravelTransit:
		if err := validateCityCode(input.OriginCity, "起点城市"); err != nil {
			return err
		}
		if err := validateCityCode(input.DestinationCity, "终点城市"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: 不支持的交通方式 %q", domain.ErrInvalidInput, input.Mode)
	}
	return nil
}

func validateCityCode(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 12 {
		return fmt.Errorf("%w: %s citycode 无效", domain.ErrInvalidInput, field)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return fmt.Errorf("%w: %s 仅支持数字 citycode", domain.ErrInvalidInput, field)
		}
	}
	return nil
}

func (s *SafetyService) plan(ctx context.Context, input RouteSearchInput) ([]domainevacuation.Route, error) {
	if input.Mode == domainevacuation.TravelTransit {
		planner, ok := s.planner.(ports.TransitRoutePlanner)
		if !ok {
			return nil, fmt.Errorf("%w: 公交路线规划端口未配置", domain.ErrProviderUnavailable)
		}
		return planner.PlanTransit(ctx, input.Origin, input.Destination, input.OriginCity, input.DestinationCity)
	}
	return s.planner.Plan(ctx, input.Origin, input.Destination, input.Mode)
}

func validateRouteRiskData(expected hazard.Type, snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	if snapshot.ID == "" || snapshot.HazardType != expected {
		return fmt.Errorf("%w: 风险快照与路线灾种不一致", domain.ErrInsufficientData)
	}
	if err := validateRiskValidTo(snapshot, "路线"); err != nil {
		return err
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return fmt.Errorf("%w: 风险快照当前不可用于路线筛选", domain.ErrInsufficientData)
	}
	if zones == nil {
		return fmt.Errorf("%w: 风险区数据不完整，拒绝返回未过滤路线", domain.ErrInsufficientData)
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

func evaluateRoutes(input RouteSearchInput, snapshot hazard.Snapshot, zones []hazard.RiskZone,
	candidates []domainevacuation.Route,
) (RouteSearchResult, error) {
	result := RouteSearchResult{
		Snapshot: snapshot, Routes: make([]domainevacuation.Route, 0, len(candidates)),
		Excluded: make([]ExcludedRoute, 0), Limitations: []string{
			"路线仅排除了穿越已知风险区的候选，不代表道路已获交管部门确认开放",
		},
	}
	if limitation := riskFreshnessLimitation(snapshot, "路线安全结果"); limitation != "" {
		result.Limitations = append(result.Limitations, limitation)
	}
	for index, candidate := range candidates {
		if err := validateProviderGeometrySize(len(candidate.Geometry.Coordinates)); err != nil {
			return RouteSearchResult{}, fmt.Errorf("校验第 %d 条路线负载: %w", index+1, err)
		}
		route := cloneRoute(candidate)
		if err := validateCandidateRoute(input, route); err != nil {
			context := fmt.Sprintf("校验第 %d 条路线", index+1)
			return RouteSearchResult{}, wrapUnsafeProviderResult(context, err)
		}
		if route.IntersectsRiskZone {
			result.Excluded = append(result.Excluded, ExcludedRoute{
				Route: route, Reason: "供应商已标记路线穿越风险区",
			})
			continue
		}
		zoneIDs, err := intersectingZoneIDs(route.Geometry, zones)
		if err != nil {
			return RouteSearchResult{}, fmt.Errorf("判断第 %d 条路线风险区关系: %w", index+1, err)
		}
		if len(zoneIDs) > 0 {
			result.Excluded = append(result.Excluded, ExcludedRoute{
				Route: route, ZoneIDs: zoneIDs, Reason: "路线几何穿越已知风险区",
			})
			continue
		}
		route.IntersectsRiskZone = false
		result.Routes = append(result.Routes, route)
	}
	sortRoutes(result.Routes)
	result.RiskScoreAvailable = riskScoresAvailable(result.Routes)
	missingScores := missingRiskScoreCount(result.Routes)
	if len(result.Routes) > 0 && missingScores == len(result.Routes) {
		result.Limitations = append(result.Limitations, "供应商未提供路线风险分数，当前按风险区相交闸门后以时间和距离排序")
	} else if missingScores > 0 {
		result.Limitations = append(result.Limitations,
			"部分路线缺少供应商风险分数；缺失项不按 0 分处理，并在已有分数之后按时间和距离排序")
	}
	return result, nil
}

func validateCandidateRoute(input RouteSearchInput, route domainevacuation.Route) error {
	if route.Mode != input.Mode {
		return fmt.Errorf("%w: 供应商返回交通方式 %q，与请求 %q 不一致", domain.ErrInvalidInput, route.Mode, input.Mode)
	}
	if !samePoint(route.Origin, input.Origin) || !samePoint(route.Destination, input.Destination) {
		return fmt.Errorf("%w: 供应商返回路线起终点与请求不一致", domain.ErrInvalidInput)
	}
	if err := route.Validate(); err != nil {
		return err
	}
	if route.Geometry.Type != "LineString" {
		return fmt.Errorf("%w: 路线几何必须是 LineString", domain.ErrInvalidInput)
	}
	if err := route.Geometry.ValidateLineString(); err != nil {
		return fmt.Errorf("校验路线几何: %w", err)
	}
	if route.RiskScoreProvided && (math.IsNaN(route.RiskScore) || math.IsInf(route.RiskScore, 0) ||
		route.RiskScore < 0 || route.RiskScore > 100) {
		return fmt.Errorf("%w: 路线风险分数必须在 0 到 100 之间", domain.ErrInvalidInput)
	}
	return nil
}

func intersectingZoneIDs(route spatial.Geometry, zones []hazard.RiskZone) ([]string, error) {
	matched := make([]string, 0)
	for _, zone := range zones {
		intersects, err := zone.Geometry.IntersectsLineString(route)
		if err != nil {
			return nil, fmt.Errorf("%w: 风险区 %s 路线相交判断失败: %w", domain.ErrInsufficientData, zone.ID, err)
		}
		if intersects {
			matched = append(matched, zone.ID)
		}
	}
	return matched, nil
}

func sortRoutes(routes []domainevacuation.Route) {
	sort.SliceStable(routes, func(left, right int) bool {
		if routes[left].RiskScoreProvided != routes[right].RiskScoreProvided {
			return routes[left].RiskScoreProvided
		}
		if routes[left].RiskScoreProvided && routes[left].RiskScore != routes[right].RiskScore {
			return routes[left].RiskScore < routes[right].RiskScore
		}
		if routes[left].DurationSeconds != routes[right].DurationSeconds {
			return routes[left].DurationSeconds < routes[right].DurationSeconds
		}
		if routes[left].DistanceMeters != routes[right].DistanceMeters {
			return routes[left].DistanceMeters < routes[right].DistanceMeters
		}
		return routes[left].ID < routes[right].ID
	})
	for index := range routes {
		routes[index].Rank = index + 1
	}
}

func riskScoresAvailable(routes []domainevacuation.Route) bool {
	for _, route := range routes {
		if route.RiskScoreProvided {
			return true
		}
	}
	return false
}

func missingRiskScoreCount(routes []domainevacuation.Route) int {
	missing := 0
	for _, route := range routes {
		if !route.RiskScoreProvided {
			missing++
		}
	}
	return missing
}

func samePoint(left, right spatial.Point) bool {
	return math.Abs(left.Longitude-right.Longitude) <= 1e-9 &&
		math.Abs(left.Latitude-right.Latitude) <= 1e-9
}

func cloneRoute(value domainevacuation.Route) domainevacuation.Route {
	value.Steps = append([]domainevacuation.RouteStep(nil), value.Steps...)
	value.Limitations = append([]string(nil), value.Limitations...)
	return value
}
