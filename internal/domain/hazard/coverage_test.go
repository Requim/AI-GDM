package hazard

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestProcessingBoundaryValidatesVersionedAdministrativeScope(t *testing.T) {
	value := validProcessingBoundary()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if value.Coverage.Identity() != "CHN-ADM0-1|2024|"+strings.Repeat("a", 64)+"|"+
		value.Coverage.GeometrySHA256 {
		t.Fatalf("Identity()=%s", value.Coverage.Identity())
	}
}

func TestProcessingBoundaryRejectsInvalidDigestAndGeometry(t *testing.T) {
	value := validProcessingBoundary()
	value.Coverage.SHA256 = "bad"
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid digest error=%v", err)
	}
	value = validProcessingBoundary()
	value.Geometry.Coordinates = json.RawMessage(`[[[100,30],[101,30],[100,30]]]`)
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid geometry error=%v", err)
	}
	value = validProcessingBoundary()
	value.Geometry.Coordinates = json.RawMessage(`[[[100,30],[102,30],[102,31],[100,30]]]`)
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) ||
		!strings.Contains(err.Error(), "几何摘要不匹配") {
		t.Fatalf("geometry digest mismatch error=%v", err)
	}
}

func validProcessingBoundary() ProcessingBoundary {
	value := ProcessingBoundary{
		Coverage: Coverage{Mode: CoverageAdministrativeBoundary, RegionCode: "CN",
			BoundaryID: "CHN-ADM0-1", BoundaryType: "ADM0", BoundaryVersion: "2024",
			Source: "fixture", License: "Public Domain", Reference: "https://example.test/china.geojson",
			SHA256: strings.Repeat("a", 64), CollectedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
		Geometry: spatial.Geometry{Type: "Polygon",
			Coordinates: json.RawMessage(`[[[100,30],[101,30],[101,31],[100,30]]]`)},
		InputReferences: []string{"https://example.test/china.geojson"},
	}
	value.Coverage.GeometrySHA256, _ = BoundaryGeometryDigest(value.Geometry)
	return value
}
