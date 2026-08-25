package spatialanalysis

import (
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

func validAnalysisInput() AnalysisInput {
	return AnalysisInput{
		SnapshotID: " snapshot-001 ",
		Area: AreaCalculation{
			Method: AreaMethod, TotalSquareMeters: 135,
			InputReferences: []string{"geometry://snapshot-001", "geometry://snapshot-001"},
		},
		Zones: []ZoneResult{
			availableZone("zone-002", 60),
			availableZone("zone-001", 90),
		},
		CalculatedAt:      time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC),
		InputReferences:   []string{"snapshot://001", "snapshot://001"},
		DatasetReferences: []string{"dataset://roads/v1", "dataset://population/v1", "dataset://roads/v1"},
		Limitations:       []string{"仅用于辅助研判", "仅用于辅助研判"},
	}
}

func availableZone(id string, area float64) ZoneResult {
	full := floatPointer(1)
	return ZoneResult{
		ZoneID: id,
		Area:   ZoneArea{SquareMeters: area, InputReferences: []string{"geometry://" + id}},
		Population: PopulationExposureMetric{
			Status: MetricAvailable, Quantity: floatPointer(120), Unit: PopulationUnit,
			CoverageRatio: full, InputReferences: []string{"dataset://population/v1"},
		},
		Roads: RoadExposureMetric{
			Status: MetricAvailable, Quantity: floatPointer(350), Unit: RoadUnit,
			CoverageRatio: full, InputReferences: []string{"dataset://roads/v1"},
		},
		POIs: POIExposureMetric{
			Status: MetricAvailable, Quantity: floatPointer(2), Unit: POIUnit,
			CoverageRatio: full, InputReferences: []string{"dataset://pois/v1"},
		},
		Administration: AdministrativeMatch{
			Status: AdminMatchAvailable, AdminCodes: []string{"510100", "510000", "510100"},
			CoverageRatio: full, InputReferences: []string{"dataset://admin/v1"},
		},
	}
}

func unavailableZone(id string, area float64) ZoneResult {
	return ZoneResult{
		ZoneID: id,
		Area:   ZoneArea{SquareMeters: area, InputReferences: []string{"geometry://" + id}},
		Population: PopulationExposureMetric{
			Status: MetricUnavailable, Unit: PopulationUnit, Limitations: []string{"缺少人口基线"},
		},
		Roads: RoadExposureMetric{
			Status: MetricUnavailable, Unit: RoadUnit, Limitations: []string{"缺少道路基线"},
		},
		POIs: POIExposureMetric{
			Status: MetricUnavailable, Unit: POIUnit, Limitations: []string{"缺少 POI 基线"},
		},
		Administration: AdministrativeMatch{
			Status: AdminMatchUnavailable, Limitations: []string{"缺少行政边界"},
		},
	}
}

func floatPointer(value float64) *float64 {
	return &value
}

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}
