package survival

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestScenarioValidateRequiresSyntheticInput(t *testing.T) {
	scenario := validScenario("scenario-1", "case-1")
	if err := scenario.Validate(); err != nil {
		t.Fatal(err)
	}

	scenario.Synthetic = false
	if err := scenario.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝非合成场景")
	}
}

func TestScenarioValidateRejectsInvalidSignalsAndCompleteness(t *testing.T) {
	scenario := validScenario("scenario-1", "case-1")
	scenario.Environment.AirPocket = SignalValue("yes_typo")
	if !errors.Is(scenario.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Validate() 未拒绝非法环境信号")
	}
	scenario = validScenario("scenario-1", "case-1")
	scenario.InputCompleteness = 0.9
	if !errors.Is(scenario.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Validate() 未拒绝与字段覆盖率不一致的完整度")
	}
}

func TestScenarioCompletenessAcceptsFiniteCoverageIncrements(t *testing.T) {
	unknown := Scenario{ID: "scenario-zero", CaseID: "case-zero",
		AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC), Synthetic: true,
		Environment: EnvironmentSignals{AirPocket: SignalUnknown, WaterAvailable: SignalUnknown,
			HazardStable: SignalUnknown},
		Entrapment: EntrapmentSignals{Communication: SignalUnknown, Injury: InjuryUnknown}}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("zero coverage error=%v", err)
	}
	oneKnown := unknown
	oneKnown.ID, oneKnown.CaseID = "scenario-one", "case-one"
	oneKnown.Environment.AirPocket, oneKnown.InputCompleteness = SignalYes, 0.2
	if err := oneKnown.Validate(); err != nil {
		t.Fatalf("0.2 coverage error=%v", err)
	}
	allKnown := oneKnown
	allKnown.ID, allKnown.CaseID, allKnown.InputCompleteness = "scenario-all", "case-all", 1
	allKnown.Environment = EnvironmentSignals{AirPocket: SignalYes, WaterAvailable: SignalNo, HazardStable: SignalYes}
	allKnown.Entrapment = EntrapmentSignals{Communication: SignalNo, Injury: InjuryNone}
	if err := allKnown.Validate(); err != nil {
		t.Fatalf("full coverage error=%v", err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, 1.1} {
		invalid := unknown
		invalid.InputCompleteness = value
		if !errors.Is(invalid.Validate(), domain.ErrInvalidInput) {
			t.Fatalf("未拒绝非法完整度 %v", value)
		}
	}
}

func TestEvaluateIsDeterministicAndExplainsFactors(t *testing.T) {
	calculatedAt := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	scenario := Scenario{ID: "scenario-strong", CaseID: "case-strong",
		AsOf: calculatedAt.Add(-time.Hour), ElapsedMinutes: 30, InputCompleteness: 1, Synthetic: true,
		Environment: EnvironmentSignals{AirPocket: SignalYes, WaterAvailable: SignalYes, HazardStable: SignalYes},
		Entrapment:  EntrapmentSignals{Communication: SignalYes, Injury: InjuryNone}}
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
	scenario := validScenario("scenario-1", "case-1")
	_, err := Evaluate(scenario, time.Date(2026, 8, 27, 3, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Evaluate() error = %v", err)
	}
}

func TestScenarioDigestIsStableAndRequiresValidInput(t *testing.T) {
	scenario := validScenario("scenario-digest", "case-digest")
	first, err := scenario.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := scenario.Digest()
	if err != nil || first != second || len(first) != len("sha256:")+64 {
		t.Fatalf("digest first=%q second=%q err=%v", first, second, err)
	}
	changed := scenario
	changed.ElapsedMinutes++
	changedDigest, err := changed.Digest()
	if err != nil || changedDigest == first {
		t.Fatalf("scenario mutation digest=%q original=%q err=%v", changedDigest, first, err)
	}
	scenario.Entrapment.Injury = InjurySeverity("serious")
	if _, err = scenario.Digest(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid scenario digest error=%v", err)
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

func TestSurvivalDomainRejectsOversizedFieldsAndArrays(t *testing.T) {
	event := HistoricalEvent{
		ID: "case-1", DatasetEventID: "catalog:case-1", EventDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Category: "landslide", Country: "United States", LocationAccuracy: "approximate",
		Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history", SourceURI: "https://example.test/case",
			DataKind: provenance.DataKindHistorical, FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
	event.Source.Citation = strings.Repeat("x", 2<<20)
	if !errors.Is(event.Validate(), domain.ErrInvalidInput) {
		t.Fatal("HistoricalEvent.Validate() 未拒绝超长来源字段")
	}
	event.Source.Citation = "公开来源"
	event.Limitations = make([]string, maxTextItems+1)
	for index := range event.Limitations {
		event.Limitations[index] = "限制"
	}
	if !errors.Is(event.Validate(), domain.ErrInvalidInput) {
		t.Fatal("HistoricalEvent.Validate() 未拒绝超量限制")
	}
	scenario := validScenario(strings.Repeat("s", maxIdentifierBytes+1), "case-1")
	if !errors.Is(scenario.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Scenario.Validate() 未拒绝超长标识")
	}
}

func validScenario(id, caseID string) Scenario {
	return Scenario{
		ID: id, CaseID: caseID, AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
		ElapsedMinutes: 60, InputCompleteness: 0.6, Synthetic: true,
		Environment: EnvironmentSignals{AirPocket: SignalYes, WaterAvailable: SignalNo,
			HazardStable: SignalUnknown},
		Entrapment: EntrapmentSignals{Communication: SignalYes, Injury: InjuryUnknown},
	}
}
