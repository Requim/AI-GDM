package risk

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestEngineProbabilityBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value float64
		level hazard.RiskLevel
	}{
		{name: "zero", value: 0, level: hazard.RiskLow},
		{name: "low-upper", value: 0.1, level: hazard.RiskLow},
		{name: "moderate-lower-open", value: math.Nextafter(0.1, math.Inf(1)), level: hazard.RiskModerate},
		{name: "moderate-upper", value: 0.5, level: hazard.RiskModerate},
		{name: "high-lower-open", value: math.Nextafter(0.5, math.Inf(1)), level: hazard.RiskHigh},
		{name: "high-upper", value: 0.9, level: hazard.RiskHigh},
		{name: "very-high-lower-open", value: math.Nextafter(0.9, math.Inf(1)), level: hazard.RiskVeryHigh},
		{name: "one", value: 1, level: hazard.RiskVeryHigh},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(now)
			input.Zones = []hazard.RiskZone{validZone(input.Snapshot.ID, "zone-1", test.value, test.level)}
			got, err := landslideEngine().Evaluate(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision == nil || got.Decision.Level != test.level {
				t.Fatalf("Evaluate() level = %+v, want %s", got.Decision, test.level)
			}
		})
	}
}

func TestEngineZeroZonesIsLowRisk(t *testing.T) {
	input := validInput(time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC))
	input.Zones = nil
	got, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision == nil || got.Decision.Level != hazard.RiskLow ||
		got.Decision.ZoneCount != 0 || got.Decision.Basis != "no_elevated_zone" {
		t.Fatalf("Evaluate() zero zones = %+v", got.Decision)
	}
}

func TestEngineUsesHighestZoneAndStableOrdering(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Zones = []hazard.RiskZone{
		validZone(input.Snapshot.ID, "zone-z", 0.95, hazard.RiskVeryHigh),
		validZone(input.Snapshot.ID, "zone-a", 0.92, hazard.RiskVeryHigh),
		validZone(input.Snapshot.ID, "zone-m", 0.6, hazard.RiskHigh),
	}
	originalIDs := zoneIDs(input.Zones)
	first, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(zoneIDs(input.Zones), originalIDs) {
		t.Fatalf("Evaluate() 修改了输入切片: %v", zoneIDs(input.Zones))
	}
	input.Zones[0], input.Zones[2] = input.Zones[2], input.Zones[0]
	second, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("输入顺序改变了确定性输出\nfirst=%+v\nsecond=%+v", first, second)
	}
	if !reflect.DeepEqual(first.Decision.HighestZoneIDs, []string{"zone-a", "zone-z"}) {
		t.Fatalf("HighestZoneIDs = %v", first.Decision.HighestZoneIDs)
	}
}

func TestEngineDataStatusAndConfidence(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	current, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if current.DataStatus != DataCurrent || current.Confidence.Level != ConfidenceMedium {
		t.Fatalf("current = status %s confidence %+v", current.DataStatus, current.Confidence)
	}
	input.Snapshot.Source.ObservedAt = now.Add(-time.Hour)
	high, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if high.Confidence.Level != ConfidenceHigh {
		t.Fatalf("high confidence = %+v", high.Confidence)
	}
	input.Snapshot.Status = hazard.SnapshotStale
	input.Snapshot.Source.Stale = true
	input.Snapshot.Source.QualityFlags = []string{fallbackQualityFlag}
	fallback, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Status != AssessmentDegraded || fallback.DataStatus != DataFallback ||
		fallback.Confidence.Level != ConfidenceLow || fallback.Decision == nil {
		t.Fatalf("fallback = %+v", fallback)
	}
	if fallback.ID == current.ID {
		t.Fatal("current 与 fallback 生成了相同评估标识")
	}
}

func TestEngineValidToBoundaryAndExpiration(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.EvaluatedAt = input.Snapshot.ValidTo
	atBoundary, err := landslideEngine().Evaluate(input)
	if err != nil || atBoundary.Decision == nil || atBoundary.DataStatus != DataCurrent {
		t.Fatalf("validTo boundary = %+v, err=%v", atBoundary, err)
	}
	input.EvaluatedAt = input.Snapshot.ValidTo.Add(time.Nanosecond)
	input.WeatherContexts = []WeatherContext{validWeatherContext(now, 104.1, 30.6, "风险区包含关系")}
	expired, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != AssessmentInsufficientData || expired.DataStatus != DataExpired ||
		expired.Decision != nil || expired.Confidence.Level != ConfidenceUnavailable ||
		expired.ContextStatus == ContextAbsent {
		t.Fatalf("expired = %+v", expired)
	}
}

func TestEngineRejectsInvalidOrInconsistentInput(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Input)
		want   error
	}{
		{name: "hazard-mismatch", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Snapshot.HazardType = hazard.TypeDebrisFlow
		}},
		{name: "snapshot-pending", want: domain.ErrInsufficientData, mutate: func(input *Input) {
			input.Snapshot.Status = hazard.SnapshotPending
		}},
		{name: "fallback-marker-mismatch", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Snapshot.Source.Stale = true
		}},
		{name: "zone-snapshot-mismatch", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Zones[0].SnapshotID = "other"
		}},
		{name: "zone-level-mismatch", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Zones[0].Level = hazard.RiskModerate
		}},
		{name: "cross-threshold-statistics", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Zones[0].Minimum = 0.49
		}},
		{name: "duplicate-zone", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Zones = append(input.Zones, input.Zones[0])
		}},
		{name: "invalid-source-sha", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.Snapshot.Source.SHA256 = "not-a-sha256"
		}},
		{name: "non-utc-evaluation", want: domain.ErrInvalidInput, mutate: func(input *Input) {
			input.EvaluatedAt = input.EvaluatedAt.In(time.FixedZone("CST", 8*60*60))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(now)
			test.mutate(&input)
			_, err := landslideEngine().Evaluate(input)
			if !errors.Is(err, test.want) {
				t.Fatalf("Evaluate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEngineRejectsInvalidProbabilityValues(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	values := []float64{math.NaN(), math.Inf(1), -0.01, 1.01}
	for _, value := range values {
		input := validInput(now)
		input.Zones[0].Mean = value
		if _, err := landslideEngine().Evaluate(input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("probability %v error = %v", value, err)
		}
	}
}

func TestEngineRejectsInvalidThresholdScheme(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func([]hazard.RiskThreshold)
	}{
		{name: "gap", mutate: func(values []hazard.RiskThreshold) { values[1].Minimum = 0.2 }},
		{name: "duplicate-level", mutate: func(values []hazard.RiskThreshold) {
			values[1].Level = hazard.RiskLow
		}},
		{name: "unknown-level", mutate: func(values []hazard.RiskThreshold) {
			values[2].Level = hazard.RiskLevel("unknown")
		}},
		{name: "missing-description", mutate: func(values []hazard.RiskThreshold) {
			values[2].Description = ""
		}},
		{name: "non-finite", mutate: func(values []hazard.RiskThreshold) {
			values[2].Maximum = math.NaN()
		}},
		{name: "custom-boundary", mutate: func(values []hazard.RiskThreshold) {
			values[0].Maximum, values[1].Minimum = 0.2, 0.2
		}},
		{name: "near-canonical-boundary", mutate: func(values []hazard.RiskThreshold) {
			values[1].Minimum = math.Nextafter(0.1, math.Inf(1))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(now)
			test.mutate(input.Snapshot.Thresholds)
			if _, err := landslideEngine().Evaluate(input); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Evaluate() error = %v", err)
			}
		})
	}
}

func TestEngineAcceptsDedicatedDebrisFlowSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Snapshot.HazardType = hazard.TypeDebrisFlow
	input.Snapshot.ID = "debris-flow-snapshot"
	input.Snapshot.ModelName = "Dedicated Debris Flow Model"
	input.Snapshot.Source.Dataset = "dedicated-debris-flow-probability"
	input.Zones = []hazard.RiskZone{validZone(input.Snapshot.ID, "debris-zone", 0.6, hazard.RiskHigh)}
	engine, err := NewEngine(ModelCapability{
		HazardType: hazard.TypeDebrisFlow, ModelName: input.Snapshot.ModelName,
		Dataset: input.Snapshot.Source.Dataset,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := engine.Evaluate(input)
	if err != nil || got.HazardType != hazard.TypeDebrisFlow || got.Decision.Level != hazard.RiskHigh {
		t.Fatalf("debris flow = %+v, err=%v", got, err)
	}
}

func TestEngineRejectsLandslideSnapshotMasqueradingAsDebrisFlow(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Snapshot.HazardType = hazard.TypeDebrisFlow
	engine, err := NewEngine(ModelCapability{
		HazardType: hazard.TypeDebrisFlow, ModelName: "Dedicated Debris Flow Model",
		Dataset: "dedicated-debris-flow-probability",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Evaluate(input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("伪装 LHASA 泥石流快照 error = %v", err)
	}
}

func TestNewEngineRejectsIncompleteCapability(t *testing.T) {
	_, err := NewEngine(ModelCapability{HazardType: hazard.TypeDebrisFlow})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("NewEngine() error = %v", err)
	}
}

func validInput(now time.Time) Input {
	snapshot := validSnapshot(now)
	return Input{
		Snapshot: snapshot, EvaluatedAt: now,
		Zones: []hazard.RiskZone{validZone(snapshot.ID, "zone-1", 0.6, hazard.RiskHigh)},
	}
}

func landslideEngine() Engine {
	engine, err := NewEngine(ModelCapability{
		HazardType: hazard.TypeLandslide, ModelName: "NASA LHASA", Dataset: "LHASA_Hazard_Today",
	})
	if err != nil {
		panic(err)
	}
	return engine
}

func validSnapshot(now time.Time) hazard.Snapshot {
	validFrom, validTo := now.Add(-6*time.Hour), now.Add(6*time.Hour)
	return hazard.Snapshot{
		ID: "snapshot-1", HazardType: hazard.TypeLandslide,
		ModelName: "NASA LHASA", ModelVersion: "2.1.1", RunAt: now.Add(-time.Minute),
		ValidFrom: validFrom, ValidTo: validTo, RasterReference: "artifact://lhasa/current",
		ProbabilitySemantics: "测试概率语义", Thresholds: validThresholds(),
		Status: hazard.SnapshotAvailable,
		Source: provenance.Provenance{
			Provider: "NASA Earthdata GIS", Dataset: "LHASA_Hazard_Today",
			SourceRevision: "revision-1", SourceURI: "https://example.test/lhasa",
			DataKind: provenance.DataKindNowcast, RevisionFirstSeenAt: now.Add(-time.Hour),
			FetchedAt: now.Add(-time.Minute), ValidFrom: validFrom, ValidTo: validTo,
			SHA256: strings.Repeat("a", 64), TransformVersion: "gdal-lhasa-v1",
		},
		Limitations: []string{"测试限制"},
	}
}

func validThresholds() []hazard.RiskThreshold {
	return []hazard.RiskThreshold{
		{Level: hazard.RiskLow, Minimum: 0, Maximum: 0.1, Description: "[0,0.1]"},
		{Level: hazard.RiskModerate, Minimum: 0.1, Maximum: 0.5, Description: "(0.1,0.5]"},
		{Level: hazard.RiskHigh, Minimum: 0.5, Maximum: 0.9, Description: "(0.5,0.9]"},
		{Level: hazard.RiskVeryHigh, Minimum: 0.9, Maximum: 1, Description: "(0.9,1]"},
	}
}

func validZone(snapshotID, id string, value float64, level hazard.RiskLevel) hazard.RiskZone {
	return hazard.RiskZone{
		ID: id, SnapshotID: snapshotID,
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: []byte(`[[[100,20],[101,20],[101,21],[100,20]]]`)},
		Minimum:  value, Mean: value, Maximum: value, Level: level,
		InputReferences: []string{"artifact://lhasa/current", strings.Repeat("a", 64)},
	}
}

func zoneIDs(values []hazard.RiskZone) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.ID
	}
	return result
}
