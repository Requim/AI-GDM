package survival

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestCatalogListsValidatedCasesInStableOrder(t *testing.T) {
	first := validEvent("case-b", time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC))
	second := validEvent("case-a", time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC))
	service, err := NewCatalogService(eventReaderStub{events: []survivaldomain.HistoricalEvent{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	values, err := service.ListCases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Event.ID != "case-a" || values[1].Event.ID != "case-b" ||
		values[0].ScenarioID != "replay-a" {
		t.Fatalf("cases = %+v", values)
	}
	value, err := service.GetCase(context.Background(), "case-b")
	if err != nil || value.ScenarioID != "replay-b" {
		t.Fatalf("GetCase() value=%+v err=%v", value, err)
	}
	if _, err = service.GetCase(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing case error = %v", err)
	}
}

func TestCatalogRejectsInvalidCaseAndContext(t *testing.T) {
	service, err := NewCatalogService(eventReaderStub{events: []survivaldomain.HistoricalEvent{{ID: "bad"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ListCases(context.Background()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid case error = %v", err)
	}
	cancelService, err := NewCatalogService(eventReaderStub{events: []survivaldomain.HistoricalEvent{
		validEvent("case-1", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = cancelService.ListCases(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v", err)
	}
}

type eventReaderStub struct {
	events []survivaldomain.HistoricalEvent
}

func (s eventReaderStub) ListEvents(ctx context.Context) ([]survivaldomain.HistoricalEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]survivaldomain.HistoricalEvent(nil), s.events...), nil
}

func validEvent(id string, date time.Time) survivaldomain.HistoricalEvent {
	return survivaldomain.HistoricalEvent{
		ID: id, DatasetEventID: "catalog:" + id, EventDate: date.UTC(), Category: "landslide",
		Country: "United States", LocationAccuracy: "approximate", Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history", SourceURI: "https://example.test/" + id,
			DataKind: provenance.DataKindHistorical, FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
}
