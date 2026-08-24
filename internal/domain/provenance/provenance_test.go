package provenance

import (
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestProvenanceValidate(t *testing.T) {
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: DataKindNowcast, FetchedAt: time.Now().UTC(),
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceIsStaleUsesValidityWindow(t *testing.T) {
	validTo := time.Date(2026, 8, 24, 7, 0, 0, 0, time.UTC)
	value := Provenance{ValidTo: validTo}
	if value.IsStale(validTo) || !value.IsStale(validTo.Add(time.Nanosecond)) {
		t.Fatal("IsStale() 未按有效期边界判断")
	}
}

func TestProvenanceRejectsInvalidWindow(t *testing.T) {
	now := time.Now().UTC()
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: DataKindNowcast, FetchedAt: now, ValidFrom: now, ValidTo: now.Add(-time.Hour),
	}
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceRejectsNonUTCTime(t *testing.T) {
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind:  DataKindForecast,
		FetchedAt: time.Date(2026, 8, 24, 16, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
	}
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}
