package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const oversizedFixtureTextBytes = 2*1024*1024 + 1024

func typedSnapshot(name string, hazardType hazard.Type) hazard.Snapshot {
	validFrom := mustTime("2026-08-27T12:00:00Z")
	validTo := mustTime(defaultValidTo)
	status := hazard.SnapshotAvailable
	stale := false
	qualityFlags := []string(nil)
	if shortValidityScenario(name) {
		validTo = mustTime(shortValidTo)
	}
	if name == "fallback_then_expiry" || name == "risk_zone_all_excluded_fallback_then_expiry" {
		validTo = mustTime(fallbackValidTo)
	}
	if fallbackScenario(name) {
		status, stale = hazard.SnapshotStale, true
		qualityFlags = []string{"fallback_last_success"}
	}
	return hazard.Snapshot{
		ID: "snapshot-evacuation", HazardType: hazardType, ModelName: "LHASA",
		ModelVersion: "e2e", RunAt: validFrom, ValidFrom: validFrom, ValidTo: validTo,
		Status: status, Source: snapshotSource(validFrom, validTo, stale, qualityFlags),
		Limitations: []string{"仅用于疏散工作台浏览器契约回归"},
	}
}

func snapshotSource(validFrom, validTo time.Time, stale bool, flags []string) provenance.Provenance {
	return provenance.Provenance{
		Provider: "NASA Earthdata", Dataset: "LHASA_Hazard_Today",
		SourceURI: "https://example.invalid/earthdata-fixture", DataKind: provenance.DataKindNowcast,
		FetchedAt: validFrom, ValidFrom: validFrom, ValidTo: validTo, CRS: "EPSG:4326",
		Stale: stale, QualityFlags: append([]string(nil), flags...),
	}
}

func shortValidityScenario(name string) bool {
	return name == "short_validity" || name == "all_omitted_short_validity"
}

func fallbackScenario(name string) bool {
	return name == "fallback_unexpired" || name == "fallback_then_expiry" ||
		name == "risk_zone_all_excluded_fallback_then_expiry"
}

func typedRiskZones(name string) []hazard.RiskZone {
	if name != "risk_zone_all_excluded" && name != "risk_zone_all_excluded_fallback_then_expiry" {
		return make([]hazard.RiskZone, 0)
	}
	coordinates := [][][]float64{{
		{104.05, 30.55}, {104.10, 30.55}, {104.10, 30.62},
		{104.05, 30.62}, {104.05, 30.55},
	}}
	payload, _ := json.Marshal(coordinates)
	return []hazard.RiskZone{{
		ID: "zone-all-excluded", SnapshotID: "snapshot-evacuation", Level: hazard.RiskHigh,
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: payload},
	}}
}

func typedFacilities(name string, kind evacuation.FacilityType) []evacuation.Facility {
	switch name {
	case "zero_facilities":
		return make([]evacuation.Facility, 0)
	case "facility_excess_count":
		return repeatedTypedFacilities(26, kind)
	case "facility_page_limit":
		return repeatedTypedFacilities(25, kind)
	case "facility_zero_distance":
		value := typedFacility("facility-zero-distance", "起点旁避难场所", kind)
		value.DistanceMeters = 0
		return []evacuation.Facility{value}
	case "facility_all_omitted", "all_omitted_short_validity":
		value := typedFacility("facility-omitted", strings.Repeat("x", oversizedFixtureTextBytes), kind)
		return []evacuation.Facility{value}
	case "html_injection":
		value := typedFacility("facility-injection", injectionName, kind)
		value.Source = entitySource(injectionVendor)
		return []evacuation.Facility{value}
	default:
		return []evacuation.Facility{typedFacility("facility-safe", "成都市应急避难场所", kind)}
	}
}

func repeatedTypedFacilities(count int, kind evacuation.FacilityType) []evacuation.Facility {
	values := make([]evacuation.Facility, count)
	for index := range values {
		values[index] = typedFacility(
			fmt.Sprintf("facility-%03d", index), fmt.Sprintf("候选设施 %d", index), kind,
		)
	}
	return values
}

func typedFacility(id, name string, kind evacuation.FacilityType) evacuation.Facility {
	return evacuation.Facility{
		ID: id, Name: name, Type: kind, Address: "成都市测试地址",
		Location: spatial.Point{Longitude: 104.082, Latitude: 30.59}, DistanceMeters: 1500,
		Source: entitySource("高德地图"),
	}
}

func typedRoutes(name string, origin, destination spatial.Point,
	mode evacuation.TravelMode,
) []evacuation.Route {
	switch name {
	case "risk_score_unavailable":
		return []evacuation.Route{typedRoute("route-no-score", origin, destination, mode, 0, false, 3)}
	case "risk_score_zero":
		return []evacuation.Route{typedRoute("route-zero", origin, destination, mode, 0, true, 3)}
	case "risk_score_mixed":
		return mixedScoreRoutes(origin, destination, mode)
	case "route_rank_contract":
		return rankContractRoutes(origin, destination, mode)
	case "route_intersects_flag":
		value := typedRoute("route-provider-blocked", origin, destination, mode, 12, true, 3)
		value.IntersectsRiskZone = true
		return []evacuation.Route{value}
	case "route_empty_optional_lists":
		value := typedRoute("route-empty-lists", origin, destination, mode, 18.5, true, 3)
		value.Steps, value.Limitations = nil, nil
		return []evacuation.Route{value}
	case "route_excess_count":
		return repeatedTypedRoutes(12, origin, destination, mode)
	case "route_excess_geometry":
		return []evacuation.Route{typedRoute("route-complex", origin, destination, mode, 18.5, true, 5001)}
	case "route_all_omitted", "all_omitted_short_validity":
		value := typedRoute("route-omitted", origin, destination, mode, 18.5, true, 3)
		value.Steps[0].Instruction = strings.Repeat("x", oversizedFixtureTextBytes)
		return []evacuation.Route{value}
	case "html_injection":
		value := typedRoute("route-injection", origin, destination, mode, 18.5, true, 3)
		value.Source = entitySource(injectionVendor)
		value.Limitations = []string{injectionLimit}
		return []evacuation.Route{value}
	default:
		return []evacuation.Route{typedRoute("route-safe", origin, destination, mode, 18.5, true, 3)}
	}
}

func mixedScoreRoutes(origin, destination spatial.Point, mode evacuation.TravelMode) []evacuation.Route {
	missing := typedRoute("route-missing", origin, destination, mode, 0, false, 3)
	missing.DurationSeconds = 300
	scored := typedRoute("route-scored", origin, destination, mode, 27.5, true, 3)
	scored.DurationSeconds = 720
	return []evacuation.Route{missing, scored}
}

func rankContractRoutes(origin, destination spatial.Point, mode evacuation.TravelMode) []evacuation.Route {
	values := []evacuation.Route{
		typedRoute("route-score-40", origin, destination, mode, 40, true, 3),
		typedRoute("route-missing", origin, destination, mode, 0, false, 3),
		typedRoute("route-score-10-slow", origin, destination, mode, 10, true, 3),
		typedRoute("route-score-10-fast", origin, destination, mode, 10, true, 3),
	}
	values[0].DurationSeconds, values[1].DurationSeconds = 900, 300
	values[2].DurationSeconds, values[3].DurationSeconds = 800, 600
	for index := range values {
		values[index].Rank = 99
	}
	return values
}

func repeatedTypedRoutes(count int, origin, destination spatial.Point,
	mode evacuation.TravelMode,
) []evacuation.Route {
	values := make([]evacuation.Route, count)
	for index := range values {
		values[index] = typedRoute(
			fmt.Sprintf("route-%03d", index), origin, destination, mode, float64(index), true, 3,
		)
	}
	return values
}

func typedRoute(id string, origin, destination spatial.Point, mode evacuation.TravelMode,
	score float64, provided bool, vertexCount int,
) evacuation.Route {
	coordinates := routeCoordinatesBetween(origin, destination, vertexCount)
	payload, _ := json.Marshal(coordinates)
	return evacuation.Route{
		ID: id, Origin: origin, Destination: destination, Mode: mode,
		DistanceMeters: 3200, DurationSeconds: 720,
		Geometry:  spatial.Geometry{Type: "LineString", Coordinates: payload},
		Steps:     []evacuation.RouteStep{{Instruction: "沿主干道向东北行驶", DistanceM: 3200}},
		RiskScore: score, RiskScoreProvided: provided, Source: entitySource("高德地图"),
		Limitations: []string{"需结合交管和现场状态人工确认"},
	}
}

func routeCoordinatesBetween(origin, destination spatial.Point, count int) [][]float64 {
	values := make([][]float64, count)
	for index := range values {
		ratio := float64(index) / float64(maximum(count-1, 1))
		values[index] = []float64{
			origin.Longitude + ratio*(destination.Longitude-origin.Longitude),
			origin.Latitude + ratio*(destination.Latitude-origin.Latitude),
		}
	}
	return values
}

func entitySource(provider string) provenance.Provenance {
	return provenance.Provenance{
		Provider: provider, Dataset: "evacuation-fixture",
		SourceURI: "https://example.invalid/map-fixture", DataKind: provenance.DataKindObservation,
		FetchedAt: mustTime("2026-08-28T00:00:00Z"), Stale: false,
	}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
