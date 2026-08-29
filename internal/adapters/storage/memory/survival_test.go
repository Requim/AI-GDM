package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestDefaultSurvivalCatalogContainsAuditableCases(t *testing.T) {
	catalog, err := NewSurvivalCatalog(time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	values, err := catalog.ListEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) < 3 {
		t.Fatalf("历史事件数量 = %d", len(values))
	}
	if !values[0].EventDate.After(values[len(values)-1].EventDate) {
		t.Fatalf("事件未按日期倒序: %+v", values)
	}
	for _, value := range values {
		if value.Source.Provider == "" || value.Source.SourceURI == "" ||
			value.Source.DataKind != "historical" || value.Source.SourceRevision == "" ||
			value.Source.CRS != "EPSG:4326" {
			t.Fatalf("来源字段不完整: %+v", value.Source)
		}
	}
}

func TestDefaultSurvivalCatalogUsesStableVerifiedTime(t *testing.T) {
	first, err := NewDefaultSurvivalCatalog()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDefaultSurvivalCatalog()
	if err != nil {
		t.Fatal(err)
	}
	firstEvents, err := first.ListEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondEvents, err := second.ListEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for index := range firstEvents {
		source := firstEvents[index].Source
		if !source.FetchedAt.Equal(defaultSurvivalCatalogVerifiedAt) ||
			!source.RevisionFirstSeenAt.Equal(defaultSurvivalCatalogVerifiedAt) ||
			source.SourceRevision != defaultHistoricalSourceRevision ||
			!source.FetchedAt.Equal(secondEvents[index].Source.FetchedAt) {
			t.Fatalf("unstable source first=%+v second=%+v", source, secondEvents[index].Source)
		}
	}
}

func TestCatalogScenarioLookupAndCancellation(t *testing.T) {
	catalog, err := NewDefaultSurvivalCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := catalog.GetScenario(context.Background(), "replay-oso-2014")
	if err != nil || !scenario.Synthetic || scenario.CaseID != "case-oso-2014" ||
		scenario.InputCompleteness != scenario.KnownFieldCoverage() {
		t.Fatalf("scenario=%+v err=%v", scenario, err)
	}
	linked, err := catalog.ScenarioForEvent(context.Background(), "case-oso-2014")
	if err != nil || linked.ID != scenario.ID {
		t.Fatalf("linked scenario=%+v err=%v", linked, err)
	}
	if _, err = catalog.ScenarioForEvent(context.Background(), "case-missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing event scenario error=%v", err)
	}
	duplicate := scenario
	duplicate.ID = "replay-oso-duplicate"
	catalog.scenarios[duplicate.ID] = duplicate
	if _, err = catalog.ScenarioForEvent(context.Background(), "case-oso-2014"); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("duplicate event scenario error=%v", err)
	}
	if _, err = catalog.GetScenario(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing scenario error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = catalog.ListEvents(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v", err)
	}
}

func TestNewSurvivalCatalogRejectsNonUTC(t *testing.T) {
	_, err := NewSurvivalCatalog(time.Date(2026, 8, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("non UTC error = %v", err)
	}
}

var _ interface {
	ListEvents(context.Context) ([]survival.HistoricalEvent, error)
} = (*SurvivalCatalog)(nil)
