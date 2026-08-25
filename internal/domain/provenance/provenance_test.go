package provenance

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestProvenanceOmitsUnknownObservationTime(t *testing.T) {
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: DataKindNowcast, RevisionFirstSeenAt: time.Now().UTC(), FetchedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "observedAt") {
		t.Fatalf("unknown observedAt was serialized: %s", payload)
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

func TestCompositeSourceRevisionIgnoresPartOrder(t *testing.T) {
	first := []SourcePart{
		{Reference: "https://example.test/b", Revision: `"b"`, SizeBytes: 2},
		{Reference: "https://example.test/a", Revision: `"a"`, SizeBytes: 1},
	}
	second := []SourcePart{first[1], first[0]}
	if CompositeSourceRevision(first) != CompositeSourceRevision(second) {
		t.Fatal("CompositeSourceRevision() 受分片顺序影响")
	}
}

func TestProvenanceRejectsInvalidSourceParts(t *testing.T) {
	now := time.Now().UTC()
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: DataKindNowcast, FetchedAt: now,
		SourceParts: []SourcePart{{Reference: "https://example.test/tile", SizeBytes: 1}},
	}
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceRejectsTamperedCompositeRevision(t *testing.T) {
	now := time.Now().UTC()
	parts := []SourcePart{{
		Reference: "https://example.test/tile", Revision: `"revision-1"`, SizeBytes: 1024,
	}}
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		SourceRevision: CompositeSourceRevision(parts), DataKind: DataKindNowcast,
		FetchedAt: now, SourceParts: parts,
	}
	value.SourceParts[0].Revision = `"revision-2"`
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceRejectsInvalidPartChecksum(t *testing.T) {
	now := time.Now().UTC()
	parts := []SourcePart{{
		Reference: "https://example.test/tile", Revision: `"revision-1"`,
		SizeBytes: 1024, SHA256: "invalid",
	}}
	value := Provenance{
		Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		SourceRevision: CompositeSourceRevision(parts), DataKind: DataKindNowcast,
		FetchedAt: now, SourceParts: parts,
	}
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceRejectsNonUTCTime(t *testing.T) {
	nonUTC := time.Date(2026, 8, 24, 16, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for _, value := range []Provenance{
		{
			Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
			DataKind: DataKindForecast, FetchedAt: nonUTC,
		},
		{
			Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
			DataKind: DataKindNowcast, RevisionFirstSeenAt: nonUTC, FetchedAt: time.Now().UTC(),
		},
	} {
		if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("Validate() error = %v", err)
		}
	}
}
