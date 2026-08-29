package survival

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestCatalogListsSummariesAndReturnsAuditableDetail(t *testing.T) {
	first := validEvent("case-b", time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC))
	second := validEvent("case-a", time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC))
	source := catalogReaderStub{events: []survivaldomain.HistoricalEvent{first, second},
		scenarios: map[string]survivaldomain.Scenario{
			"case-a": validScenarioForCase("case-a"), "case-b": validScenarioForCase("case-b"),
		}}
	service, err := NewCatalogService(source)
	if err != nil {
		t.Fatal(err)
	}
	values, err := service.ListCases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Event.ID != "case-a" || values[1].Event.ID != "case-b" ||
		values[0].ScenarioID != "replay-case-a" {
		t.Fatalf("cases = %+v", values)
	}
	payload, err := json.Marshal(values)
	if err != nil || bytes.Contains(payload, []byte(`"environment"`)) {
		t.Fatalf("列表泄露完整场景 payload=%s err=%v", payload, err)
	}
	detail, err := service.GetCase(context.Background(), "case-b")
	if err != nil || detail.Scenario.CaseID != "case-b" || detail.ScenarioDigest == "" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if detail.Usage.LiveUseAllowed || detail.Usage.Mode != survivaldomain.ReplayModeHistorical {
		t.Fatalf("usage=%+v", detail.Usage)
	}
	if err = detail.Validate(); err != nil {
		t.Fatalf("detail validation=%v", err)
	}
	detail.ScenarioDigest = "sha256:00"
	if !errors.Is(detail.Validate(), domain.ErrInvalidInput) {
		t.Fatal("HistoricalCaseDetail.Validate() 未拒绝错误场景摘要")
	}
	early, err := service.GetCase(context.Background(), "case-b")
	if err != nil {
		t.Fatal(err)
	}
	early.Scenario.AsOf = early.Event.EventDate.Add(-time.Second)
	early.ScenarioDigest, err = early.Scenario.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(early.Validate(), domain.ErrInvalidInput) {
		t.Fatal("HistoricalCaseDetail.Validate() 未拒绝早于事件日期的场景")
	}
}

func TestCatalogFailsClosedForMissingAndCrossBoundScenario(t *testing.T) {
	event := validEvent("case-a", time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC))
	missing, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missing.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("missing scenario error=%v", err)
	}
	cross, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event},
		scenarios: map[string]survivaldomain.Scenario{"case-a": validScenarioForCase("case-b")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cross.GetCase(context.Background(), "case-a"); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("cross-bound scenario error=%v", err)
	}
	invalidScenario := validScenarioForCase("case-a")
	invalidScenario.Environment.AirPocket = survivaldomain.SignalValue("yes_typo")
	invalid, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event},
		scenarios: map[string]survivaldomain.Scenario{"case-a": invalidScenario}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invalid.ListCases(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid scenario wrapping error=%v", err)
	}
}

func TestCatalogRejectsInvalidCaseContextAndDependencies(t *testing.T) {
	if _, err := NewCatalogService(nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil source error=%v", err)
	}
	service, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{{ID: "bad"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ListCases(context.Background()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid case error=%v", err)
	}
	badID := validEvent("bad id", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	badIDService, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{badID},
		scenarios: map[string]survivaldomain.Scenario{"bad id": validScenarioForCase("bad id")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = badIDService.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("bad case identifier error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.ListCases(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error=%v", err)
	}
}

func TestCatalogRejectsInvalidAndDuplicateScenarioIdentifiers(t *testing.T) {
	event := validEvent("case-a", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	invalidScenario := validScenarioForCase("case-a")
	invalidScenario.ID = "bad scenario"
	invalid, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event},
		scenarios: map[string]survivaldomain.Scenario{"case-a": invalidScenario}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invalid.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("bad scenario identifier error=%v", err)
	}
	duplicate, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event, event},
		scenarios: map[string]survivaldomain.Scenario{"case-a": validScenarioForCase("case-a")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = duplicate.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("duplicate scenario error=%v", err)
	}
}

func TestCatalogRejectsOversizedDirectoryBeforeScenarioReads(t *testing.T) {
	service, err := NewCatalogService(oversizedCatalogReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("oversized catalog error=%v", err)
	}
}

func TestCatalogRejectsScenarioBeforeHistoricalEvent(t *testing.T) {
	event := validEvent("case-early", time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC))
	scenario := validScenarioForCase(event.ID)
	scenario.AsOf = event.EventDate.Add(-time.Second)
	service, err := NewCatalogService(catalogReaderStub{events: []survivaldomain.HistoricalEvent{event},
		scenarios: map[string]survivaldomain.Scenario{event.ID: scenario}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ListCases(context.Background()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("early scenario error=%v", err)
	}
}

type oversizedCatalogReader struct{}

func (oversizedCatalogReader) ListEvents(context.Context) ([]survivaldomain.HistoricalEvent, error) {
	return make([]survivaldomain.HistoricalEvent, MaxCatalogCases+1), nil
}

func (oversizedCatalogReader) ScenarioForEvent(context.Context, string) (survivaldomain.Scenario, error) {
	panic("目录上限检查后不应读取场景")
}

type catalogReaderStub struct {
	events    []survivaldomain.HistoricalEvent
	scenarios map[string]survivaldomain.Scenario
}

func (s catalogReaderStub) ListEvents(ctx context.Context) ([]survivaldomain.HistoricalEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]survivaldomain.HistoricalEvent(nil), s.events...), nil
}

func (s catalogReaderStub) ScenarioForEvent(ctx context.Context, id string) (survivaldomain.Scenario, error) {
	if err := ctx.Err(); err != nil {
		return survivaldomain.Scenario{}, err
	}
	value, ok := s.scenarios[id]
	if !ok {
		return survivaldomain.Scenario{}, domain.ErrNotFound
	}
	return value, nil
}

func validEvent(id string, date time.Time) survivaldomain.HistoricalEvent {
	return survivaldomain.HistoricalEvent{
		ID: id, DatasetEventID: "catalog:" + id, EventDate: date.UTC(), Category: "landslide",
		Country: "United States", LocationAccuracy: "approximate", Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history", SourceURI: "https://example.test/" + id,
			SourceRevision: "revision-1", DataKind: provenance.DataKindHistorical,
			FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
}

func validScenarioForCase(caseID string) survivaldomain.Scenario {
	return survivaldomain.Scenario{
		ID: "replay-" + caseID, CaseID: caseID, AsOf: time.Date(2020, 1, 3, 1, 0, 0, 0, time.UTC),
		ElapsedMinutes: 60, InputCompleteness: 0.6, Synthetic: true,
		Environment: survivaldomain.EnvironmentSignals{AirPocket: survivaldomain.SignalYes,
			WaterAvailable: survivaldomain.SignalNo, HazardStable: survivaldomain.SignalUnknown},
		Entrapment: survivaldomain.EntrapmentSignals{Communication: survivaldomain.SignalYes,
			Injury: survivaldomain.InjuryUnknown},
	}
}
