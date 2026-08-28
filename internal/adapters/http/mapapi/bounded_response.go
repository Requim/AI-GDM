package mapapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const (
	maxFacilities         = 50
	maxExcludedFacilities = 50
	maxRoutes             = 10
	maxExcludedRoutes     = 10
	maxRouteVertices      = 5_000
	maxRouteGeometryBytes = applicationevacuation.MaxRouteProviderGeometryBytes
	maxTotalVertices      = 20_000
	maxRouteSteps         = 50
	maxRiskZoneIDs        = 50
	maxSourceRouteResults = applicationevacuation.MaxRouteProviderCandidates
	providerPageNumber    = 1
	providerPageSizeLimit = applicationevacuation.MaxFacilityProviderCandidates
)

var errUnsafeMapResult = errors.New("地图业务结果无法安全投影")

type nearbyResult struct {
	Snapshot    hazard.Snapshot           `json:"snapshot"`
	Facilities  []boundedFacility         `json:"facilities"`
	Excluded    []boundedExcludedFacility `json:"excluded"`
	Filter      facilityFilter            `json:"filter"`
	Limits      facilityLimits            `json:"limits"`
	Limitations []string                  `json:"limitations"`
}

type boundedFacility struct {
	evacuation.Facility
	DistanceMeters float64 `json:"distanceMeters"`
}

type boundedExcludedFacility struct {
	Facility               boundedFacility `json:"facility"`
	ZoneIDs                []string        `json:"riskZoneIds"`
	OmittedRiskZoneIDCount int             `json:"omittedRiskZoneIdCount"`
	Reason                 string          `json:"reason"`
}

type facilityFilter struct {
	CandidateCount       int `json:"candidateCount"`
	AllowedCount         int `json:"allowedCount"`
	ExcludedCount        int `json:"excludedCount"`
	VisibleAllowedCount  int `json:"visibleAllowedCount"`
	VisibleExcludedCount int `json:"visibleExcludedCount"`
	OmittedAllowedCount  int `json:"omittedAllowedCount"`
	OmittedExcludedCount int `json:"omittedExcludedCount"`
}

type facilityLimits struct {
	MaxFacilities         int `json:"maxFacilities"`
	MaxExcludedFacilities int `json:"maxExcludedFacilities"`
	MaxRiskZoneIDs        int `json:"maxRiskZoneIds"`
	MaxResponseBytes      int `json:"maxResponseBytes"`
	ProviderPageNumber    int `json:"providerPageNumber"`
	ProviderPageSizeLimit int `json:"providerPageSizeLimit"`
}

type safeRouteResult struct {
	Snapshot                  hazard.Snapshot        `json:"snapshot"`
	Routes                    []boundedRoute         `json:"routes"`
	Excluded                  []boundedExcludedRoute `json:"excluded"`
	Limitations               []string               `json:"limitations"`
	TotalRouteCount           int                    `json:"totalRouteCount"`
	VisibleRouteCount         int                    `json:"visibleRouteCount"`
	OmittedRouteCount         int                    `json:"omittedRouteCount"`
	TotalExcludedRouteCount   int                    `json:"totalExcludedRouteCount"`
	VisibleExcludedRouteCount int                    `json:"visibleExcludedRouteCount"`
	OmittedExcludedRouteCount int                    `json:"omittedExcludedRouteCount"`
	RiskScoreAvailable        bool                   `json:"riskScoreAvailable"`
	Limits                    routeLimits            `json:"limits"`
}

type boundedRoute struct {
	evacuation.Route
	RiskScore         *float64 `json:"riskScore"`
	GeometryByteCount int      `json:"geometryByteCount"`
	OmittedStepCount  int      `json:"omittedStepCount"`
}

type boundedExcludedRoute struct {
	Route                  boundedRoute `json:"route"`
	ZoneIDs                []string     `json:"riskZoneIds"`
	OmittedRiskZoneIDCount int          `json:"omittedRiskZoneIdCount"`
	Reason                 string       `json:"reason"`
}

type routeLimits struct {
	MaxRoutes             int `json:"maxRoutes"`
	MaxExcludedRoutes     int `json:"maxExcludedRoutes"`
	MaxRouteVertices      int `json:"maxRouteVertices"`
	MaxRouteGeometryBytes int `json:"maxRouteGeometryBytes"`
	MaxTotalVertices      int `json:"maxTotalVertices"`
	MaxRouteSteps         int `json:"maxRouteSteps"`
	MaxRiskZoneIDs        int `json:"maxRiskZoneIds"`
	MaxResponseBytes      int `json:"maxResponseBytes"`
}

type routeProjectionState struct {
	totalVertices      int
	vertexLimitOmitted bool
	stepLimitOmitted   bool
	riskZoneIDsOmitted bool
}

func buildNearbyResult(source applicationevacuation.SearchResult) (nearbyResult, error) {
	if len(source.Excluded) > providerPageSizeLimit ||
		len(source.Facilities) > providerPageSizeLimit-len(source.Excluded) {
		return nearbyResult{}, unsafeMapResult("设施供应商首屏结果超过 %d 条", providerPageSizeLimit)
	}
	result := nearbyResult{
		Snapshot: source.Snapshot, Facilities: make([]boundedFacility, 0, maxFacilities),
		Excluded:    make([]boundedExcludedFacility, 0, maxExcludedFacilities),
		Limitations: cloneSlice(source.Limitations), Limits: defaultFacilityLimits(),
		Filter: facilityFilter{AllowedCount: len(source.Facilities), ExcludedCount: len(source.Excluded),
			CandidateCount: len(source.Facilities) + len(source.Excluded)},
	}
	result.Limitations = appendUnique(result.Limitations,
		"当前地图供应商只请求第 1 页，每页最多 25 条；candidateCount 是本次返回量，不是区域设施总数")
	allowedCount := minimum(len(source.Facilities), maxFacilities)
	for _, facility := range source.Facilities[:allowedCount] {
		result.Facilities = append(result.Facilities, projectFacility(facility))
	}
	excludedCount := minimum(len(source.Excluded), maxExcludedFacilities)
	for _, excluded := range source.Excluded[:excludedCount] {
		result.Excluded = append(result.Excluded, projectExcludedFacility(excluded))
	}
	refreshNearbyCounts(&result)
	addNearbyLimitations(&result)
	if err := fitNearbyResponse(&result); err != nil {
		return nearbyResult{}, err
	}
	return result, nil
}

func projectExcludedFacility(source applicationevacuation.ExcludedFacility) boundedExcludedFacility {
	visible := minimum(len(source.ZoneIDs), maxRiskZoneIDs)
	return boundedExcludedFacility{
		Facility: projectFacility(source.Facility), ZoneIDs: cloneSlice(source.ZoneIDs[:visible]),
		OmittedRiskZoneIDCount: len(source.ZoneIDs) - visible, Reason: source.Reason,
	}
}

func projectFacility(source evacuation.Facility) boundedFacility {
	return boundedFacility{Facility: source, DistanceMeters: source.DistanceMeters}
}

func refreshNearbyCounts(result *nearbyResult) {
	result.Filter.VisibleAllowedCount = len(result.Facilities)
	result.Filter.VisibleExcludedCount = len(result.Excluded)
	result.Filter.OmittedAllowedCount = result.Filter.AllowedCount - len(result.Facilities)
	result.Filter.OmittedExcludedCount = result.Filter.ExcludedCount - len(result.Excluded)
}

func addNearbyLimitations(result *nearbyResult) {
	if result.Filter.OmittedAllowedCount > 0 || result.Filter.OmittedExcludedCount > 0 {
		result.Limitations = appendUnique(result.Limitations,
			"设施响应受展示数量上限约束，省略数量见 filter")
	}
	for _, excluded := range result.Excluded {
		if excluded.OmittedRiskZoneIDCount > 0 {
			result.Limitations = appendUnique(result.Limitations,
				"排除设施的风险区标识最多返回 50 个，省略数量见 omittedRiskZoneIdCount")
			return
		}
	}
}

func fitNearbyResponse(result *nearbyResult) error {
	if responseFits(*result) {
		return nil
	}
	result.Limitations = appendUnique(result.Limitations,
		"部分设施因 2 MiB 响应上限未返回，省略数量见 filter")
	for !responseFits(*result) {
		switch {
		case len(result.Excluded) > 0:
			result.Excluded = result.Excluded[:len(result.Excluded)-1]
		case len(result.Facilities) > 0:
			result.Facilities = result.Facilities[:len(result.Facilities)-1]
		default:
			return unsafeMapResult("设施响应固定元数据超过 %d 字节", maxResponseBytes)
		}
		refreshNearbyCounts(result)
	}
	return nil
}

func defaultFacilityLimits() facilityLimits {
	return facilityLimits{
		MaxFacilities: maxFacilities, MaxExcludedFacilities: maxExcludedFacilities,
		MaxRiskZoneIDs: maxRiskZoneIDs, MaxResponseBytes: maxResponseBytes,
		ProviderPageNumber: providerPageNumber, ProviderPageSizeLimit: providerPageSizeLimit,
	}
}

func buildSafeRouteResult(source applicationevacuation.RouteSearchResult) (safeRouteResult, error) {
	if len(source.Excluded) > maxSourceRouteResults ||
		len(source.Routes) > maxSourceRouteResults-len(source.Excluded) {
		return safeRouteResult{}, unsafeMapResult("路线源结果超过 %d 条", maxSourceRouteResults)
	}
	result := newSafeRouteResult(source)
	state := &routeProjectionState{}
	if err := addVisibleRoutes(&result, source.Routes, state); err != nil {
		return safeRouteResult{}, err
	}
	if err := addVisibleExcludedRoutes(&result, source.Excluded, state); err != nil {
		return safeRouteResult{}, err
	}
	refreshRouteCounts(&result)
	addRouteLimitations(&result, state)
	if err := fitRouteResponse(&result); err != nil {
		return safeRouteResult{}, err
	}
	return result, nil
}

func newSafeRouteResult(source applicationevacuation.RouteSearchResult) safeRouteResult {
	return safeRouteResult{
		Snapshot: source.Snapshot, Routes: make([]boundedRoute, 0, maxRoutes),
		Excluded:        make([]boundedExcludedRoute, 0, maxExcludedRoutes),
		Limitations:     cloneSlice(source.Limitations),
		TotalRouteCount: len(source.Routes), TotalExcludedRouteCount: len(source.Excluded),
		RiskScoreAvailable: source.RiskScoreAvailable, Limits: defaultRouteLimits(),
	}
}

func addVisibleRoutes(result *safeRouteResult, source []evacuation.Route,
	state *routeProjectionState,
) error {
	for index, route := range source {
		projected, vertices, err := projectRoute(route)
		if err != nil {
			return fmt.Errorf("%w: 第 %d 条可选路线: %w", errUnsafeMapResult, index+1, err)
		}
		if len(result.Routes) >= maxRoutes || state.totalVertices+vertices > maxTotalVertices {
			state.vertexLimitOmitted = state.vertexLimitOmitted || state.totalVertices+vertices > maxTotalVertices
			continue
		}
		result.Routes = append(result.Routes, projected)
		state.totalVertices += vertices
		state.stepLimitOmitted = state.stepLimitOmitted || projected.OmittedStepCount > 0
	}
	return nil
}

func addVisibleExcludedRoutes(result *safeRouteResult, source []applicationevacuation.ExcludedRoute,
	state *routeProjectionState,
) error {
	for index, excluded := range source {
		projected, vertices, err := projectExcludedRoute(excluded)
		if err != nil {
			return fmt.Errorf("%w: 第 %d 条排除路线: %w", errUnsafeMapResult, index+1, err)
		}
		if len(result.Excluded) >= maxExcludedRoutes || state.totalVertices+vertices > maxTotalVertices {
			state.vertexLimitOmitted = state.vertexLimitOmitted || state.totalVertices+vertices > maxTotalVertices
			continue
		}
		result.Excluded = append(result.Excluded, projected)
		state.totalVertices += vertices
		state.stepLimitOmitted = state.stepLimitOmitted || projected.Route.OmittedStepCount > 0
		state.riskZoneIDsOmitted = state.riskZoneIDsOmitted || projected.OmittedRiskZoneIDCount > 0
	}
	return nil
}

func projectRoute(source evacuation.Route) (boundedRoute, int, error) {
	if len(source.Geometry.Coordinates) > maxRouteGeometryBytes {
		return boundedRoute{}, 0, fmt.Errorf("路线几何超过解析字节上限")
	}
	if err := source.Geometry.ValidateLineString(); err != nil {
		return boundedRoute{}, 0, fmt.Errorf("路线几何无效: %w", err)
	}
	var coordinates [][]float64
	if err := json.Unmarshal(source.Geometry.Coordinates, &coordinates); err != nil {
		return boundedRoute{}, 0, fmt.Errorf("解析路线几何: %w", err)
	}
	if len(coordinates) > maxRouteVertices {
		return boundedRoute{}, 0, fmt.Errorf("路线几何含 %d 个顶点，超过 %d 上限", len(coordinates), maxRouteVertices)
	}
	originalStepCount := len(source.Steps)
	visibleSteps := minimum(originalStepCount, maxRouteSteps)
	source.Steps = cloneSlice(source.Steps[:visibleSteps])
	source.Limitations = cloneSlice(source.Limitations)
	var riskScore *float64
	if source.RiskScoreProvided {
		value := source.RiskScore
		riskScore = &value
	}
	return boundedRoute{
		Route: source, RiskScore: riskScore, GeometryByteCount: len(source.Geometry.Coordinates),
		OmittedStepCount: originalStepCount - visibleSteps,
	}, len(coordinates), nil
}

func projectExcludedRoute(source applicationevacuation.ExcludedRoute) (boundedExcludedRoute, int, error) {
	projected, vertices, err := projectRoute(source.Route)
	if err != nil {
		return boundedExcludedRoute{}, 0, err
	}
	visibleZoneIDs := minimum(len(source.ZoneIDs), maxRiskZoneIDs)
	return boundedExcludedRoute{
		Route: projected, ZoneIDs: cloneSlice(source.ZoneIDs[:visibleZoneIDs]),
		OmittedRiskZoneIDCount: len(source.ZoneIDs) - visibleZoneIDs, Reason: source.Reason,
	}, vertices, nil
}

func refreshRouteCounts(result *safeRouteResult) {
	result.VisibleRouteCount = len(result.Routes)
	result.OmittedRouteCount = result.TotalRouteCount - len(result.Routes)
	result.VisibleExcludedRouteCount = len(result.Excluded)
	result.OmittedExcludedRouteCount = result.TotalExcludedRouteCount - len(result.Excluded)
}

func addRouteLimitations(result *safeRouteResult, state *routeProjectionState) {
	if result.OmittedRouteCount > 0 || result.OmittedExcludedRouteCount > 0 {
		result.Limitations = appendUnique(result.Limitations,
			"路线响应受展示数量和总顶点上限约束，省略数量见顶层计数")
	}
	if state.vertexLimitOmitted {
		result.Limitations = appendUnique(result.Limitations, "部分路线因总顶点上限未返回")
	}
	if state.stepLimitOmitted {
		result.Limitations = appendUnique(result.Limitations, "路线步骤最多返回 50 条，省略数量见 omittedStepCount")
	}
	if state.riskZoneIDsOmitted {
		result.Limitations = appendUnique(result.Limitations,
			"排除路线的风险区标识最多返回 50 个，省略数量见 omittedRiskZoneIdCount")
	}
}

func fitRouteResponse(result *safeRouteResult) error {
	if responseFits(*result) {
		return nil
	}
	result.Limitations = appendUnique(result.Limitations,
		"部分路线因 2 MiB 响应上限未返回，省略数量见顶层计数")
	for !responseFits(*result) {
		switch {
		case len(result.Excluded) > 0:
			result.Excluded = result.Excluded[:len(result.Excluded)-1]
		case len(result.Routes) > 0:
			result.Routes = result.Routes[:len(result.Routes)-1]
		default:
			return unsafeMapResult("路线响应固定元数据超过 %d 字节", maxResponseBytes)
		}
		refreshRouteCounts(result)
	}
	return nil
}

func defaultRouteLimits() routeLimits {
	return routeLimits{
		MaxRoutes: maxRoutes, MaxExcludedRoutes: maxExcludedRoutes,
		MaxRouteVertices: maxRouteVertices, MaxRouteGeometryBytes: maxRouteGeometryBytes,
		MaxTotalVertices: maxTotalVertices,
		MaxRouteSteps:    maxRouteSteps, MaxRiskZoneIDs: maxRiskZoneIDs,
		MaxResponseBytes: maxResponseBytes,
	}
}

func responseFits(data any) bool {
	payload, err := json.Marshal(successResponse{
		Data: data, RequestID: strings.Repeat("x", maxRequestIDBytes),
	})
	return err == nil && len(payload) <= maxResponseBytes
}

func unsafeMapResult(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", errUnsafeMapResult, fmt.Sprintf(format, arguments...))
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func minimum(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func cloneSlice[T any](values []T) []T {
	result := make([]T, len(values))
	copy(result, values)
	return result
}
