package survival

import (
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestHistoricalEventCatalogRequiresHistoricalProvenance(t *testing.T) {
	event := HistoricalEvent{
		ID: "case-catalog-1", DatasetEventID: "catalog:case-catalog-1",
		EventDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Category:  "landslide", Country: "United States", LocationAccuracy: "approximate",
		Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history",
			SourceURI: "https://example.test/case-catalog-1", DataKind: provenance.DataKindHistorical,
			FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.Source.DataKind = provenance.DataKindObservation
	if !errors.Is(event.Validate(), domain.ErrInvalidInput) {
		t.Fatal("历史事件未拒绝非 historical 来源")
	}
}

func TestHistoricalEventCatalogRejectsNonUTCTimestamps(t *testing.T) {
	event := HistoricalEvent{
		ID: "case-catalog-2", DatasetEventID: "catalog:case-catalog-2",
		EventDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		Category:  "landslide", Country: "United States", LocationAccuracy: "approximate",
		Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history",
			SourceURI: "https://example.test/case-catalog-2", DataKind: provenance.DataKindHistorical,
			FetchedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)},
	}
	if !errors.Is(event.Validate(), domain.ErrInvalidInput) {
		t.Fatal("历史事件未拒绝非 UTC 日期")
	}
}
