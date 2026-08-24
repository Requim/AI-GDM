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
