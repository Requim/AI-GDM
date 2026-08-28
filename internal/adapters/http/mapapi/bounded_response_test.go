package mapapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestNearbyProjectionClosesCountsAndExposesFirstPageLimits(t *testing.T) {
	source := applicationevacuation.SearchResult{
		Facilities: makeFacilities(13), Excluded: makeExcludedFacilities(12, 55),
		Limitations: []string{"设施结果仅覆盖供应商候选集"},
	}
	result, err := buildNearbyResult(source)
	if err != nil {
		t.Fatal(err)
	}
	filter := result.Filter
	if filter.AllowedCount != filter.VisibleAllowedCount+filter.OmittedAllowedCount ||
		filter.ExcludedCount != filter.VisibleExcludedCount+filter.OmittedExcludedCount ||
		filter.CandidateCount != filter.AllowedCount+filter.ExcludedCount {
		t.Fatalf("设施计数不闭合: %+v", filter)
	}
	if len(result.Facilities) != 13 || len(result.Excluded) != 12 ||
		result.Excluded[0].OmittedRiskZoneIDCount != 5 {
		t.Fatalf("设施投影上限错误: facilities=%d excluded=%d first=%+v",
			len(result.Facilities), len(result.Excluded), result.Excluded[0])
	}
	if result.Limits.ProviderPageNumber != 1 || result.Limits.ProviderPageSizeLimit != 25 ||
		result.Limits.MaxResponseBytes != maxResponseBytes {
		t.Fatalf("设施供应商首屏限制缺失: %+v", result.Limits)
	}
	assertBoundedSuccessPayload(t, result)
}

func TestNearbyProjectionRejectsProviderPageOverflow(t *testing.T) {
	_, err := buildNearbyResult(applicationevacuation.SearchResult{Facilities: makeFacilities(26)})
	if !errors.Is(err, errUnsafeMapResult) {
		t.Fatalf("供应商首屏越界未 fail-closed: %v", err)
	}
}

func TestSafeRouteProjectionRejectsSourceCountOverflow(t *testing.T) {
	tests := []struct {
		name     string
		routes   int
		excluded int
	}{
		{name: "routes_only", routes: maxSourceRouteResults + 1},
		{name: "combined", routes: 600, excluded: maxSourceRouteResults - 599},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
				Routes:   make([]evacuation.Route, test.routes),
				Excluded: make([]applicationevacuation.ExcludedRoute, test.excluded),
			})
			if !errors.Is(err, errUnsafeMapResult) {
				t.Fatalf("路线源数量越界未由 HTTP 投影二次拒绝: %v", err)
			}
		})
	}
}

func TestNearbyProjectionCountsPayloadOmissions(t *testing.T) {
	source := applicationevacuation.SearchResult{Facilities: []evacuation.Facility{{
		ID: "oversized", Name: strings.Repeat("x", maxResponseBytes+1),
	}}}
	result, err := buildNearbyResult(source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Filter.VisibleAllowedCount != 0 || result.Filter.OmittedAllowedCount != 1 {
		t.Fatalf("超大设施未计入省略: %+v", result.Filter)
	}
	assertBoundedSuccessPayload(t, result)
}

func TestNearbyProjectionSerializesZeroDistanceAndEmptyZoneIDs(t *testing.T) {
	result, err := buildNearbyResult(applicationevacuation.SearchResult{
		Facilities: []evacuation.Facility{{ID: "center", DistanceMeters: 0}},
		Excluded:   []applicationevacuation.ExcludedFacility{{Facility: evacuation.Facility{ID: "flagged"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Facilities []struct {
			DistanceMeters *float64 `json:"distanceMeters"`
		} `json:"facilities"`
		Excluded []struct {
			RiskZoneIDs []string `json:"riskZoneIds"`
		} `json:"excluded"`
	}
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Facilities[0].DistanceMeters == nil || *decoded.Facilities[0].DistanceMeters != 0 ||
		decoded.Excluded[0].RiskZoneIDs == nil {
		t.Fatalf("设施零距离或空风险区 ID 未规范化: %s", payload)
	}
}

func TestSafeRouteProjectionClosesCountsAndTruncatesDetails(t *testing.T) {
	routes := makeRoutes(12, 2, 55, true)
	excluded := makeExcludedRoutes(12, 2, 55, true)
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: routes, Excluded: excluded, RiskScoreAvailable: true,
		Limitations: []string{"路线不代表道路开放"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalRouteCount != result.VisibleRouteCount+result.OmittedRouteCount ||
		result.TotalExcludedRouteCount != result.VisibleExcludedRouteCount+result.OmittedExcludedRouteCount {
		t.Fatalf("路线计数不闭合: %+v", result)
	}
	if len(result.Routes) != 10 || len(result.Excluded) != 10 ||
		result.Routes[0].OmittedStepCount != 5 ||
		result.Routes[0].GeometryByteCount != len(routes[0].Geometry.Coordinates) ||
		result.Excluded[0].OmittedRiskZoneIDCount != 5 {
		t.Fatalf("路线详情上限错误: routes=%d excluded=%d route=%+v excluded0=%+v",
			len(result.Routes), len(result.Excluded), result.Routes[0], result.Excluded[0])
	}
	if result.Limits.MaxRouteVertices != 5_000 || result.Limits.MaxTotalVertices != 20_000 ||
		result.Limits.MaxRouteGeometryBytes != maxRouteGeometryBytes ||
		result.Limits.MaxRouteSteps != 50 || result.Limits.MaxResponseBytes != maxResponseBytes {
		t.Fatalf("路线限制缺失: %+v", result.Limits)
	}
	assertBoundedSuccessPayload(t, result)
}

func TestSafeRouteProjectionAcceptsExactGeometryByteLimit(t *testing.T) {
	prefix := `[[116,39],[117,39]`
	coordinates := prefix + strings.Repeat(" ", maxRouteGeometryBytes-len(prefix)-1) + `]`
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: []evacuation.Route{projectionRouteWithRaw("exact-limit", coordinates)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Routes[0].GeometryByteCount != maxRouteGeometryBytes {
		t.Fatalf("几何字节计数错误: %d", result.Routes[0].GeometryByteCount)
	}
}

func TestSafeRouteProjectionSerializesMissingRiskScoreAsNull(t *testing.T) {
	missing := projectionRoute("missing", 2, 0, false)
	providedZero := projectionRoute("provided", 2, 0, true)
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: []evacuation.Route{missing, providedZero}, RiskScoreAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Routes []struct {
			RiskScore         *float64 `json:"riskScore"`
			RiskScoreProvided bool     `json:"riskScoreProvided"`
		} `json:"routes"`
	}
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Routes[0].RiskScore != nil || decoded.Routes[0].RiskScoreProvided ||
		decoded.Routes[1].RiskScore == nil || *decoded.Routes[1].RiskScore != 0 ||
		!decoded.Routes[1].RiskScoreProvided {
		t.Fatalf("风险分数提供性序列化错误: %s", payload)
	}
}

func TestSafeRouteProjectionSerializesEmptyCollectionsAsArrays(t *testing.T) {
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: []evacuation.Route{projectionRoute("empty-details", 2, 0, false)},
		Excluded: []applicationevacuation.ExcludedRoute{{
			Route: projectionRoute("provider-flagged", 2, 0, false), Reason: "供应商标记穿越风险区",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Routes []struct {
			Steps       []evacuation.RouteStep `json:"steps"`
			Limitations []string               `json:"limitations"`
		} `json:"routes"`
		Excluded []struct {
			RiskZoneIDs []string `json:"riskZoneIds"`
		} `json:"excluded"`
	}
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Routes[0].Steps == nil || decoded.Routes[0].Limitations == nil ||
		decoded.Excluded[0].RiskZoneIDs == nil {
		t.Fatalf("路线空集合未规范化为数组: %s", payload)
	}
}

func TestSafeRouteProjectionRejectsOversizedOrInvalidGeometry(t *testing.T) {
	tests := []struct {
		name  string
		route evacuation.Route
	}{
		{name: "too_many_vertices", route: projectionRoute("large", maxRouteVertices+1, 0, false)},
		{name: "too_many_geometry_bytes", route: projectionRouteWithRaw("large-bytes",
			`[[116,39],[117,39]`+strings.Repeat(" ", maxRouteGeometryBytes)+`]`)},
		{name: "invalid_geometry", route: projectionRouteWithRaw("invalid", `[[116,39]]`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
				Routes: []evacuation.Route{test.route},
			})
			if !errors.Is(err, errUnsafeMapResult) {
				t.Fatalf("非法路线几何未 fail-closed: %v", err)
			}
		})
	}
}

func TestSafeRouteProjectionHonorsTotalVertexLimit(t *testing.T) {
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: makeRoutes(5, maxRouteVertices, 0, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.VisibleRouteCount != 4 || result.OmittedRouteCount != 1 {
		t.Fatalf("总顶点上限未生效: visible=%d omitted=%d", result.VisibleRouteCount, result.OmittedRouteCount)
	}
	assertBoundedSuccessPayload(t, result)
}

func TestSafeRouteProjectionCountsPayloadOmissions(t *testing.T) {
	route := projectionRoute("oversized", 2, 1, false)
	route.Steps[0].Instruction = strings.Repeat("x", maxResponseBytes+1)
	result, err := buildSafeRouteResult(applicationevacuation.RouteSearchResult{
		Routes: []evacuation.Route{route},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.VisibleRouteCount != 0 || result.OmittedRouteCount != 1 {
		t.Fatalf("超大路线未计入省略: visible=%d omitted=%d",
			result.VisibleRouteCount, result.OmittedRouteCount)
	}
	assertBoundedSuccessPayload(t, result)
}

func TestMapAPIWriteJSONNeverExceedsResponseLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/map", nil)
	response := httptest.NewRecorder()
	writeJSON(response, request, http.StatusOK, map[string]string{
		"payload": strings.Repeat("x", maxResponseBytes+1),
	})
	if response.Code != http.StatusInternalServerError || response.Body.Len() > maxResponseBytes {
		t.Fatalf("最终响应未受字节上限保护: status=%d bytes=%d", response.Code, response.Body.Len())
	}
	if !strings.Contains(response.Body.String(), "response_too_large") {
		t.Fatalf("响应上限错误不可审计: %s", response.Body.String())
	}
}

func assertBoundedSuccessPayload(t *testing.T, data any) {
	t.Helper()
	payload, err := json.Marshal(successResponse{
		Data: data, RequestID: strings.Repeat("x", maxRequestIDBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > maxResponseBytes {
		t.Fatalf("投影响应超过上限: %d", len(payload))
	}
}

func makeFacilities(count int) []evacuation.Facility {
	result := make([]evacuation.Facility, count)
	for index := range result {
		result[index] = evacuation.Facility{ID: fmt.Sprintf("facility-%03d", index)}
	}
	return result
}

func makeExcludedFacilities(count, zoneCount int) []applicationevacuation.ExcludedFacility {
	result := make([]applicationevacuation.ExcludedFacility, count)
	for index := range result {
		result[index] = applicationevacuation.ExcludedFacility{
			Facility: evacuation.Facility{ID: fmt.Sprintf("excluded-%03d", index)},
			ZoneIDs:  makeZoneIDs(zoneCount), Reason: "风险区内",
		}
	}
	return result
}

func makeRoutes(count, vertices, steps int, scoreProvided bool) []evacuation.Route {
	result := make([]evacuation.Route, count)
	for index := range result {
		result[index] = projectionRoute(fmt.Sprintf("route-%03d", index), vertices, steps, scoreProvided)
	}
	return result
}

func makeExcludedRoutes(count, vertices, zoneCount int,
	scoreProvided bool,
) []applicationevacuation.ExcludedRoute {
	result := make([]applicationevacuation.ExcludedRoute, count)
	for index := range result {
		result[index] = applicationevacuation.ExcludedRoute{
			Route:   projectionRoute(fmt.Sprintf("excluded-%03d", index), vertices, 0, scoreProvided),
			ZoneIDs: makeZoneIDs(zoneCount), Reason: "穿越风险区",
		}
	}
	return result
}

func projectionRoute(id string, vertexCount, stepCount int, scoreProvided bool) evacuation.Route {
	coordinates := make([][]float64, vertexCount)
	for index := range coordinates {
		coordinates[index] = []float64{116 + float64(index%1000)/10_000, 39}
	}
	payload, _ := json.Marshal(coordinates)
	steps := make([]evacuation.RouteStep, stepCount)
	return evacuation.Route{
		ID: id, Geometry: spatial.Geometry{Type: "LineString", Coordinates: payload}, Steps: steps,
		RiskScoreProvided: scoreProvided,
	}
}

func projectionRouteWithRaw(id, coordinates string) evacuation.Route {
	return evacuation.Route{
		ID: id, Geometry: spatial.Geometry{Type: "LineString", Coordinates: json.RawMessage(coordinates)},
	}
}

func makeZoneIDs(count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = fmt.Sprintf("zone-%03d", index)
	}
	return result
}
