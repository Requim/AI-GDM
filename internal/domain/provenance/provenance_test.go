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

func TestProvenanceRejectsOversizedStringFields(t *testing.T) {
	for _, test := range provenanceStringBudgetCases() {
		t.Run(test.name, func(t *testing.T) {
			value := validProvenance()
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestProvenanceRejectsOversizedArraysAndMetadata(t *testing.T) {
	for _, test := range provenanceCollectionBudgetCases() {
		t.Run(test.name, func(t *testing.T) {
			value := validProvenance()
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
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

func TestLatestAuthorityTimeCoversAllCausalSourceTimes(t *testing.T) {
	base := time.Date(2026, 8, 28, 8, 0, 0, 999, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Provenance, time.Time)
	}{
		{"validFrom", func(value *Provenance, candidate time.Time) { value.ValidFrom = candidate }},
		{"observedAt", func(value *Provenance, candidate time.Time) { value.ObservedAt = candidate }},
		{"publishedAt", func(value *Provenance, candidate time.Time) { value.PublishedAt = candidate }},
		{"revisionFirstSeenAt", func(value *Provenance, candidate time.Time) { value.RevisionFirstSeenAt = candidate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Provenance{FetchedAt: base}
			candidate := base.Add(time.Minute + 777*time.Nanosecond)
			test.mutate(&value, candidate)
			want := candidate.UTC().Truncate(time.Microsecond)
			if got := LatestAuthorityTime(value); !got.Equal(want) {
				t.Fatalf("LatestAuthorityTime()=%s want=%s", got, want)
			}
		})
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

type provenanceBudgetCase struct {
	name   string
	mutate func(*Provenance)
}

func provenanceStringBudgetCases() []provenanceBudgetCase {
	return []provenanceBudgetCase{
		{"provider", func(v *Provenance) { v.Provider = strings.Repeat("p", maxProviderRunes+1) }},
		{"dataset", func(v *Provenance) { v.Dataset = strings.Repeat("d", maxDatasetRunes+1) }},
		{"datasetVersion", func(v *Provenance) { v.DatasetVersion = strings.Repeat("v", maxDatasetVersionRunes+1) }},
		{"sourceRevision", func(v *Provenance) { v.SourceRevision = strings.Repeat("r", maxSourceRevisionRunes+1) }},
		{"sourceUri", func(v *Provenance) { v.SourceURI = strings.Repeat("u", maxSourceURIRunes+1) }},
		{"citation", func(v *Provenance) { v.Citation = strings.Repeat("c", maxCitationRunes+1) }},
		{"license", func(v *Provenance) { v.License = strings.Repeat("l", maxLicenseRunes+1) }},
		{"spatialResolution", func(v *Provenance) { v.SpatialResolution = strings.Repeat("s", maxSpatialResolutionRunes+1) }},
		{"temporalResolution", func(v *Provenance) { v.TemporalResolution = strings.Repeat("t", maxTemporalResolutionRunes+1) }},
		{"crs", func(v *Provenance) { v.CRS = strings.Repeat("c", maxCRSRunes+1) }},
		{"sha256", func(v *Provenance) { v.SHA256 = strings.Repeat("a", maxDigestRunes+1) }},
		{"transformVersion", func(v *Provenance) { v.TransformVersion = strings.Repeat("t", maxTransformVersionRunes+1) }},
		{"providerRequestId", func(v *Provenance) { v.ProviderRequestID = strings.Repeat("r", maxProviderRequestIDRunes+1) }},
		{"model", func(v *Provenance) { v.Model = strings.Repeat("m", maxModelRunes+1) }},
	}
}

func provenanceCollectionBudgetCases() []provenanceBudgetCase {
	return []provenanceBudgetCase{
		{"qualityFlags 数量", func(v *Provenance) { v.QualityFlags = repeatStrings("flag", maxQualityFlags+1) }},
		{"qualityFlags 长度", func(v *Provenance) { v.QualityFlags = []string{strings.Repeat("f", maxQualityFlagRunes+1)} }},
		{"limitations 数量", func(v *Provenance) { v.Limitations = repeatStrings("limit", maxLimitations+1) }},
		{"limitations 长度", func(v *Provenance) { v.Limitations = []string{strings.Repeat("l", maxLimitationRunes+1)} }},
		{"sourceParts 数量", func(v *Provenance) { v.SourceParts = repeatParts(maxSourceParts + 1) }},
		{"sourcePart reference", func(v *Provenance) { bindPart(v, strings.Repeat("r", maxSourcePartReference+1), "rev") }},
		{"sourcePart revision", func(v *Provenance) { bindPart(v, "ref", strings.Repeat("r", maxSourcePartRevision+1)) }},
		{"总元数据", func(v *Provenance) {
			v.Limitations = repeatStrings(strings.Repeat("界", maxLimitationRunes), maxLimitations)
		}},
	}
}

func validProvenance() Provenance {
	return Provenance{Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: DataKindObservation, FetchedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)}
}

func repeatStrings(value string, count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func repeatParts(count int) []SourcePart {
	result := make([]SourcePart, count)
	for index := range result {
		result[index] = SourcePart{Reference: "part-" + string(rune(index+1)), Revision: "rev", SizeBytes: 1}
	}
	return result
}

func bindPart(value *Provenance, reference, revision string) {
	value.SourceParts = []SourcePart{{Reference: reference, Revision: revision, SizeBytes: 1}}
	value.SourceRevision = CompositeSourceRevision(value.SourceParts)
}
