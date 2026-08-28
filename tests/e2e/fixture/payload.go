package main

import (
	"fmt"
	"math"
)

const (
	shortValidTo   = "2026-08-28T00:00:02Z"
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
	Limits                  responseLimits `json:"limits"`
	MapLimitations          []string       `json:"mapLimitations"`
}

type riskSnapshot struct {
	ID           string     `json:"id"`
	HazardType   string     `json:"hazardType"`
	ModelName    string     `json:"modelName"`
	ModelVersion string     `json:"modelVersion"`
	Status       string     `json:"status"`
	ValidTo      string     `json:"validTo,omitempty"`
	Source       riskSource `json:"source"`
	Limitations  []string   `json:"limitations"`
}

type riskSource struct {
	Provider       string   `json:"provider"`
	Dataset        string   `json:"dataset"`
	DatasetVersion string   `json:"datasetVersion"`
	FetchedAt      string   `json:"fetchedAt"`
	ValidTo        string   `json:"validTo,omitempty"`
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
		value.Data.Snapshot.ValidTo, value.Data.Snapshot.Source.ValidTo = "", ""
	case "invalid_valid_to":
		value.Data.Snapshot.ValidTo, value.Data.Snapshot.Source.ValidTo = "not-a-time", ""
	case "short_validity":
		value = validEnvelope(shortValidTo)
	case "too_many_zones":
		value.Data.Zones = repeatedZones(3001)
		value.Data.TotalZoneCount, value.Data.VisibleZoneCount = 3001, 3001
	case "complex_geometry":
		value.Data.Zones = []riskZone{zone("zone-complex", complexPolygon(5001))}
	}
	return value
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

func complexPolygon(count int) [][][]float64 {
	ring := make([][]float64, count)
	for index := 0; index < count-1; index++ {
		angle := 2 * math.Pi * float64(index) / float64(count-1)
		ring[index] = []float64{104 + math.Cos(angle), 30 + math.Sin(angle)}
	}
	ring[count-1] = append([]float64(nil), ring[0]...)
	return [][][]float64{ring}
}
