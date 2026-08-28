package main

import (
	"encoding/json"
	"fmt"
)

const (
	defaultValidTo  = "2099-01-01T00:00:00Z"
	shortValidTo    = "2026-08-28T00:10:00Z"
	fallbackValidTo = "2026-08-28T00:10:00Z"
	injectionName   = "<img src=x onerror=\"window.__evacuationInjected=true\">"
	injectionVendor = "<svg onload=\"window.__evacuationInjected=true\">"
	injectionLimit  = "<script>window.__evacuationInjected=true</script>"
)

func facilityEnvelopeFor(name string) map[string]any {
	facilities := []map[string]any{facility("facility-safe", "成都市应急避难场所", "NASA Earthdata")}
	limitations := []string{"供应商结果经过风险区相交筛选，仍需人工核实开放状态"}
	switch name {
	case "html_injection":
		facilities = []map[string]any{facility("facility-injection", injectionName, injectionVendor)}
		limitations = []string{injectionLimit}
	case "facility_delayed":
		facilities = []map[string]any{facility("facility-late", "迟到设施结果", "高德地图")}
	case "zero_facilities":
		facilities = []map[string]any{}
	case "facility_excess_count":
		facilities = repeatedFacilities(51)
	}
	count := len(facilities)
	data := map[string]any{
		"snapshot": snapshotFor(name), "facilities": facilities, "excluded": []map[string]any{},
		"filter": map[string]any{"candidateCount": count, "allowedCount": count, "excludedCount": 0,
			"visibleAllowedCount": count, "visibleExcludedCount": 0,
			"omittedAllowedCount": 0, "omittedExcludedCount": 0},
		"limits": map[string]any{"maxFacilities": 50, "maxExcludedFacilities": 50,
			"maxRiskZoneIds": 50, "maxResponseBytes": 2 * 1024 * 1024,
			"providerPageNumber": 1, "providerPageSizeLimit": 25},
		"limitations": limitations,
	}
	return envelope(data, "fixture-facilities")
}

func routeEnvelopeFor(name string) map[string]any {
	scoreAvailable := true
	routes := []map[string]any{candidateRoute("route-safe", 18.5, true, routeCoordinates(3), "高德地图")}
	limitations := []string{"候选路线不代表道路已经开放"}
	switch name {
	case "html_injection":
		routes = []map[string]any{candidateRoute("route-injection", 18.5, true, routeCoordinates(3), injectionVendor)}
		routes[0]["limitations"] = []string{injectionLimit}
		limitations = []string{injectionLimit}
	case "risk_score_unavailable":
		scoreAvailable = false
		routes = []map[string]any{candidateRoute("route-no-score", nil, false, routeCoordinates(3), "高德地图")}
	case "risk_score_zero":
		routes = []map[string]any{candidateRoute("route-zero", 0.0, true, routeCoordinates(3), "高德地图")}
	case "risk_score_mixed":
		routes = []map[string]any{
			candidateRoute("route-missing", nil, false, routeCoordinates(3), "高德地图"),
			candidateRoute("route-scored", 27.5, true, routeCoordinates(3), "高德地图"),
		}
		routes[1]["rank"] = 2
	case "route_delayed":
		routes = []map[string]any{candidateRoute("route-late", 18.5, true, routeCoordinates(3), "迟到路线供应商")}
	case "route_excess_geometry":
		routes = []map[string]any{candidateRoute("route-complex", 18.5, true, routeCoordinates(5001), "高德地图")}
	}
	count := len(routes)
	data := map[string]any{
		"snapshot": snapshotFor(name), "routes": routes, "excluded": []map[string]any{},
		"limitations": limitations, "totalRouteCount": count, "visibleRouteCount": count,
		"omittedRouteCount": 0, "totalExcludedRouteCount": 0,
		"visibleExcludedRouteCount": 0, "omittedExcludedRouteCount": 0,
		"riskScoreAvailable": scoreAvailable,
		"limits": map[string]any{"maxRoutes": 10, "maxExcludedRoutes": 10,
			"maxRouteVertices": 5000, "maxTotalVertices": 20000, "maxRouteSteps": 50,
			"maxRiskZoneIds": 50, "maxRouteGeometryBytes": 512 * 1024,
			"maxResponseBytes": 2 * 1024 * 1024},
	}
	return envelope(data, "fixture-routes")
}

func snapshotFor(name string) map[string]any {
	snapshot := map[string]any{"id": "snapshot-evacuation", "status": "available", "validTo": defaultValidTo,
		"source": map[string]any{"provider": "NASA Earthdata", "dataset": "LHASA_Hazard_Today",
			"validTo": defaultValidTo, "stale": false}}
	source := snapshot["source"].(map[string]any)
	switch name {
	case "missing_snapshot_valid_to":
		delete(snapshot, "validTo")
	case "missing_source_valid_to":
		delete(source, "validTo")
	case "array_snapshot_valid_to":
		snapshot["validTo"] = []string{defaultValidTo}
	case "array_source_valid_to":
		source["validTo"] = []string{defaultValidTo}
	case "non_strict_valid_to":
		snapshot["validTo"], source["validTo"] = "2099-01-01T00:00:00+00:00", "2099-01-01T00:00:00+00:00"
	case "invalid_calendar_valid_to":
		snapshot["validTo"], source["validTo"] = "2099-02-30T00:00:00Z", "2099-02-30T00:00:00Z"
	case "mismatched_valid_to":
		source["validTo"] = "2099-01-01T00:00:01Z"
	case "missing_source_stale":
		delete(source, "stale")
	case "invalid_source_stale":
		source["stale"] = "false"
	case "short_validity":
		snapshot["validTo"], source["validTo"] = shortValidTo, shortValidTo
	}
	return snapshot
}

func facility(id, name, provider string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "type": "shelter", "address": "成都市测试地址",
		"location":       map[string]any{"longitude": 104.082, "latitude": 30.59},
		"distanceMeters": 1500.0, "source": source(provider),
	}
}

func repeatedFacilities(count int) []map[string]any {
	values := make([]map[string]any, count)
	for index := range values {
		values[index] = facility(fmt.Sprintf("facility-%03d", index), fmt.Sprintf("候选设施 %d", index), "高德地图")
	}
	return values
}

func candidateRoute(id string, score any, provided bool, coordinates [][]float64, provider string) map[string]any {
	coordinatePayload, _ := json.Marshal(coordinates)
	return map[string]any{
		"id": id, "origin": map[string]any{"longitude": 104.066541, "latitude": 30.572269},
		"destination": map[string]any{"longitude": 104.082, "latitude": 30.59}, "mode": "driving",
		"distanceMeters": 3200.0, "durationSeconds": 720, "riskScore": score,
		"riskScoreProvided": provided, "intersectsRiskZone": false, "rank": 1,
		"geometry":          map[string]any{"type": "LineString", "coordinates": coordinates},
		"geometryByteCount": len(coordinatePayload),
		"steps":             []map[string]any{{"instruction": "沿主干道向东北行驶", "distanceMeters": 3200.0}},
		"omittedStepCount":  0, "source": source(provider),
		"limitations": []string{"需结合交管和现场状态人工确认"},
	}
}

func routeCoordinates(count int) [][]float64 {
	values := make([][]float64, count)
	for index := range values {
		ratio := float64(index) / float64(maximum(count-1, 1))
		values[index] = []float64{104.066541 + ratio*.015459, 30.572269 + ratio*.017731}
	}
	return values
}

func source(provider string) map[string]any {
	return map[string]any{"provider": provider, "dataset": "evacuation-fixture"}
}

func envelope(data map[string]any, requestID string) map[string]any {
	return map[string]any{"data": data, "requestId": requestID}
}

func facilityEnvelopeMissingOmittedCount() map[string]any {
	payload := facilityEnvelopeFor("success")
	data := payload["data"].(map[string]any)
	filter := data["filter"].(map[string]any)
	delete(filter, "omittedAllowedCount")
	return payload
}

func routeEnvelopeMissingOmittedCount() map[string]any {
	payload := routeEnvelopeFor("success")
	data := payload["data"].(map[string]any)
	delete(data, "omittedRouteCount")
	return payload
}

func maximum(left, right int) int {
	if left > right {
		return left
	}
	return right
}
