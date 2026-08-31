package main

import "fmt"

const (
	shortValidTo   = "2026-08-28T00:10:00Z"
	tooOldValidTo  = "2026-08-24T00:00:00Z"
	defaultValidTo = "2099-01-01T00:00:00Z"
)

type riskEnvelope struct {
	Data      riskData `json:"data"`
	RequestID string   `json:"requestId"`
}

type riskData struct {
	Snapshot                riskSnapshot   `json:"snapshot"`
	Zones                   []riskZone     `json:"zones"`
	Assessment              riskAssessment `json:"assessment"`
	TotalZoneCount          int            `json:"totalZoneCount"`
	VisibleZoneCount        int            `json:"visibleZoneCount"`
	OmittedZoneCount        int            `json:"omittedZoneCount"`
	OmittedComplexZoneCount int            `json:"omittedComplexZoneCount"`
	OmittedPayloadZoneCount int            `json:"omittedPayloadZoneCount"`
	Coverage                riskCoverage   `json:"coverage"`
	Limits                  responseLimits `json:"limits"`
	MapLimitations          []string       `json:"mapLimitations"`
}

type riskCoverage struct {
	Mode                string `json:"mode"`
	Label               string `json:"label"`
	Source              string `json:"source,omitempty"`
	License             string `json:"license,omitempty"`
	RegionCode          string `json:"regionCode"`
	BoundaryID          string `json:"boundaryId"`
	BoundaryType        string `json:"boundaryType"`
	BoundaryVersion     string `json:"boundaryVersion"`
	ViewportIndependent bool   `json:"viewportIndependent"`
}

type riskSnapshot struct {
	ID           string     `json:"id"`
	HazardType   string     `json:"hazardType"`
	ModelName    string     `json:"modelName"`
	ModelVersion string     `json:"modelVersion"`
	Status       string     `json:"status"`
	ValidTo      any        `json:"validTo,omitempty"`
	Source       riskSource `json:"source"`
	Limitations  []string   `json:"limitations"`
}

type riskSource struct {
	Provider       string   `json:"provider"`
	Dataset        string   `json:"dataset"`
	DatasetVersion string   `json:"datasetVersion"`
	FetchedAt      string   `json:"fetchedAt"`
	ValidTo        any      `json:"validTo,omitempty"`
	CRS            string   `json:"crs"`
	Stale          bool     `json:"stale"`
	Limitations    []string `json:"limitations"`
}

type riskAssessment struct {
	Decision    riskDecision   `json:"decision"`
	Status      string         `json:"status"`
	DataStatus  string         `json:"dataStatus"`
	Confidence  riskConfidence `json:"confidence"`
	RuleVersion string         `json:"ruleVersion"`
	Limitations []string       `json:"limitations"`
}

type riskDecision struct {
	Level string `json:"level"`
}

type riskConfidence struct {
	Level string `json:"level"`
}

type riskZone struct {
	ID                 string       `json:"id"`
	RiskLevel          string       `json:"riskLevel"`
	ProbabilityMinimum float64      `json:"probabilityMinimum"`
	ProbabilityMean    float64      `json:"probabilityMean"`
	ProbabilityMaximum float64      `json:"probabilityMaximum"`
	AreaSquareMeters   float64      `json:"areaSquareMeters"`
	AreaCalculated     bool         `json:"areaCalculated"`
	Geometry           riskGeometry `json:"geometry"`
}

type riskGeometry struct {
	Type        string `json:"type"`
	Coordinates any    `json:"coordinates"`
}

type responseLimits struct {
	MaxZones         int `json:"maxZones"`
	MaxSourceZones   int `json:"maxSourceZones"`
	MaxZoneVertices  int `json:"maxZoneVertices"`
	MaxTotalVertices int `json:"maxTotalVertices"`
	MaxGeometryBytes int `json:"maxGeometryBytes"`
	MaxResponseBytes int `json:"maxResponseBytes"`
}

func envelopeFor(name string) riskEnvelope {
	value := validEnvelope(defaultValidTo)
	switch name {
	case "missing_valid_to":
		value.Data.Snapshot.ValidTo, value.Data.Snapshot.Source.ValidTo = nil, nil
	case "missing_snapshot_valid_to":
		value.Data.Snapshot.ValidTo = nil
	case "missing_source_valid_to":
		value.Data.Snapshot.Source.ValidTo = nil
	case "invalid_valid_to":
		value.Data.Snapshot.ValidTo, value.Data.Snapshot.Source.ValidTo = "not-a-time", ""
	case "non_string_snapshot_valid_to":
		value.Data.Snapshot.ValidTo = []string{"2099-01-01T00:00:00Z"}
	case "non_string_source_valid_to":
		value.Data.Snapshot.Source.ValidTo = []string{"2099-01-01T00:00:00Z"}
	case "non_strict_valid_to":
		value.Data.Snapshot.ValidTo = "2099-01-01T00:00:00+00:00"
		value.Data.Snapshot.Source.ValidTo = "2099-01-01T00:00:00+00:00"
	case "invalid_calendar_valid_to":
		value.Data.Snapshot.ValidTo, value.Data.Snapshot.Source.ValidTo =
			"2099-02-30T00:00:00Z", "2099-02-30T00:00:00Z"
	case "source_valid_to_mismatch":
		value.Data.Snapshot.Source.ValidTo = "2099-01-01T00:00:01Z"
	case "short_validity":
		value = validEnvelope(shortValidTo)
	case "too_old_for_loss_reference":
		value = validEnvelope(tooOldValidTo)
	case "fallback_unexpired":
		markFallback(&value)
	case "fallback_then_expiry":
		value = validEnvelope(shortValidTo)
		markFallback(&value)
	case "legacy_bbox":
		value.Data.Coverage = riskCoverage{Mode: "bounding_box",
			Label: "中国外接矩形预筛选（包含部分境外区域）", ViewportIndependent: true}
	case "invalid_coverage":
		value.Data.Coverage.ViewportIndependent = false
	case "coverage_version_mismatch":
		value.Data.Coverage.BoundaryVersion = "2024"
	case "all_zones_omitted":
		markAllZonesOmitted(&value)
	case "all_zones_omitted_then_expiry":
		value = validEnvelope(shortValidTo)
		markAllZonesOmitted(&value)
	case "partial_omission":
		markPartialOmission(&value)
	case "too_many_zones":
		value.Data.Zones = repeatedZones(3001)
		value.Data.TotalZoneCount, value.Data.VisibleZoneCount = 3001, 3001
	}
	return value
}

func markFallback(value *riskEnvelope) {
	value.Data.Snapshot.Status = "stale"
	value.Data.Snapshot.Source.Stale = true
	value.Data.Assessment.Status = "degraded"
	value.Data.Assessment.DataStatus = "fallback"
	value.Data.Assessment.Confidence.Level = "medium"
}

func markAllZonesOmitted(value *riskEnvelope) {
	value.Data.Zones = []riskZone{}
	value.Data.TotalZoneCount, value.Data.VisibleZoneCount = 1, 0
	value.Data.OmittedZoneCount = 1
	value.Data.OmittedComplexZoneCount, value.Data.OmittedPayloadZoneCount = 1, 0
	value.Data.MapLimitations = []string{"全部风险区因地图安全上限被省略"}
}

func markPartialOmission(value *riskEnvelope) {
	value.Data.Zones = repeatedZones(3000)
	value.Data.TotalZoneCount, value.Data.VisibleZoneCount = 24553, 3000
	value.Data.OmittedZoneCount = 21553
	value.Data.OmittedComplexZoneCount, value.Data.OmittedPayloadZoneCount = 153, 21400
	value.Data.MapLimitations = []string{"地图已按风险优先级省略部分风险区，省略不表示风险为零"}
}

func validEnvelope(validTo string) riskEnvelope {
	zones := []riskZone{zone("zone-high", simplePolygon())}
	return riskEnvelope{Data: riskData{
		Snapshot: riskSnapshot{ID: "snapshot-browser", HazardType: "landslide",
			ModelName: "NASA LHASA", ModelVersion: "2.1.1", Status: "available", ValidTo: validTo,
			Source: riskSource{Provider: "NASA Earthdata", Dataset: "LHASA_Hazard_Today",
				DatasetVersion: "2026-08-28", FetchedAt: "2026-08-28T00:00:00Z",
				ValidTo: validTo, CRS: "WGS84", Limitations: []string{}}, Limitations: []string{}},
		Zones: zones, Assessment: riskAssessment{Decision: riskDecision{Level: "high"},
			Status: "available", DataStatus: "current", Confidence: riskConfidence{Level: "high"},
			RuleVersion: "ai-gdm-risk-rules-v1", Limitations: []string{}},
		TotalZoneCount: len(zones), VisibleZoneCount: len(zones),
		Coverage: riskCoverage{Mode: "administrative_boundary", Label: "CHN ADM0 边界（2019）",
			Source: "geoBoundaries, Wikimedia Commons", License: "Public Domain",
			RegionCode: "CN", BoundaryID: "CHN-ADM0-1", BoundaryType: "ADM0",
			BoundaryVersion: "2019", ViewportIndependent: true},
		Limits: responseLimits{MaxZones: 3000, MaxSourceZones: 100000,
			MaxZoneVertices: 5000, MaxTotalVertices: 200000,
			MaxGeometryBytes: 512 * 1024, MaxResponseBytes: 8 * 1024 * 1024},
		MapLimitations: []string{},
	}, RequestID: "fixture-success"}
}

func repeatedZones(count int) []riskZone {
	values := make([]riskZone, count)
	for index := range values {
		values[index] = zone(fmt.Sprintf("zone-%04d", index), simplePolygon())
	}
	return values
}

func zone(id string, coordinates any) riskZone {
	return riskZone{ID: id, RiskLevel: "high", ProbabilityMinimum: .5,
		ProbabilityMean: .7, ProbabilityMaximum: .9, AreaSquareMeters: 1_000_000,
		AreaCalculated: true, Geometry: riskGeometry{Type: "Polygon", Coordinates: coordinates}}
}

func simplePolygon() [][][]float64 {
	return [][][]float64{{{104, 30}, {105, 30}, {105, 31}, {104, 30}}}
}
