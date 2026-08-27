package survival

import (
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestScenarioValidateRequiresSyntheticInput(t *testing.T) {
	scenario := Scenario{
		ID: "scenario-1", AsOf: time.Now().UTC(), ElapsedMinutes: 60,
		InputCompleteness: 0.8, Synthetic: true,
	}
	if err := scenario.Validate(); err != nil {
		t.Fatal(err)
	}

	scenario.Synthetic = false
	if err := scenario.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝非合成场景")
	}
}

func TestEvaluateIsDeterministicAndExplainsFactors(t *testing.T) {
	calculatedAt := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	scenario := Scenario{
		ID: "scenario-strong", AsOf: calculatedAt.Add(-time.Hour), ElapsedMinutes: 30,
		InputCompleteness: 0.9, Synthetic: true,
		Environment: map[string]string{"air_pocket": "yes", "water_available": "yes", "hazard_stable": "yes"},
		Entrapment:  map[string]string{"communication": "yes"},
	}
	first, err := Evaluate(scenario, calculatedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Evaluate(scenario, calculatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if first.Score != second.Score || first.ProbabilityBand != ProbabilityHigh ||
		first.Priority != PriorityImmediate || len(first.Factors) < 4 {
		t.Fatalf("unexpected assessment: %+v", first)
	}
	if first.ProbabilityLow > first.ProbabilityHigh || first.ModelVersion != ModelVersion {
		t.Fatalf("invalid probability/version: %+v", first)
	}
}

func TestEvaluateRejectsLocalTime(t *testing.T) {
	scenario := Scenario{ID: "scenario-1", AsOf: time.Now().UTC(), Synthetic: true}
	_, err := Evaluate(scenario, time.Date(2026, 8, 27, 3, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Evaluate() error = %v", err)
	}
}

func TestHistoricalEventValidateRequiresHistoricalProvenance(t *testing.T) {
	event := HistoricalEvent{
		ID: "case-1", DatasetEventID: "catalog:case-1", EventDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Category: "landslide", Country: "United States", LocationAccuracy: "approximate",
		Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history", SourceURI: "https://example.test/case",
			DataKind: provenance.DataKindHistorical, FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.Source.DataKind = provenance.DataKindObservation
	if !errors.Is(event.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Validate() 未拒绝非 historical 来源")
	}
}
