package spatialanalysis

import (
	"math"
	"testing"
)

func TestFullCoverageZeroExposureIsValid(t *testing.T) {
	zero, full := floatPointer(0), floatPointer(1)
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "population", validate: func() error {
			return (PopulationExposureMetric{Status: MetricAvailable, Quantity: zero, Unit: PopulationUnit,
				CoverageRatio: full, InputReferences: []string{"dataset://population/v1"}}).Validate()
		}},
		{name: "roads", validate: func() error {
			return (RoadExposureMetric{Status: MetricAvailable, Quantity: zero, Unit: RoadUnit,
				CoverageRatio: full, InputReferences: []string{"dataset://roads/v1"}}).Validate()
		}},
		{name: "pois", validate: func() error {
			return (POIExposureMetric{Status: MetricAvailable, Quantity: zero, Unit: POIUnit,
				CoverageRatio: full, InputReferences: []string{"dataset://pois/v1"}}).Validate()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExposureUnitsAreStronglyValidated(t *testing.T) {
	quantity, full := floatPointer(1), floatPointer(1)
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "population", validate: func() error {
			return (PopulationExposureMetric{Status: MetricAvailable, Quantity: quantity, Unit: RoadUnit,
				CoverageRatio: full, InputReferences: []string{"source"}}).Validate()
		}},
		{name: "roads", validate: func() error {
			return (RoadExposureMetric{Status: MetricAvailable, Quantity: quantity, Unit: POIUnit,
				CoverageRatio: full, InputReferences: []string{"source"}}).Validate()
		}},
		{name: "pois", validate: func() error {
			return (POIExposureMetric{Status: MetricAvailable, Quantity: quantity, Unit: PopulationUnit,
				CoverageRatio: full, InputReferences: []string{"source"}}).Validate()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { assertInvalid(t, test.validate()) })
	}
}

func TestUnavailableExposureRequiresNilQuantityAndCoverage(t *testing.T) {
	population := PopulationExposureMetric{
		Status: MetricUnavailable, Quantity: floatPointer(0), Unit: PopulationUnit,
		Limitations: []string{"缺少人口基线"},
	}
	assertInvalid(t, population.Validate())
	population.Quantity = nil
	population.CoverageRatio = floatPointer(0)
	assertInvalid(t, population.Validate())
	population.CoverageRatio = nil
	if err := population.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialExposureRequiresCoverageSourceAndLimitation(t *testing.T) {
	metric := RoadExposureMetric{
		Status: MetricPartial, Quantity: floatPointer(20), Unit: RoadUnit,
		CoverageRatio: floatPointer(0.5), InputReferences: []string{"dataset://roads/v1"},
		Limitations: []string{"仅覆盖部分风险区"},
	}
	if err := metric.Validate(); err != nil {
		t.Fatal(err)
	}
	metric.CoverageRatio = floatPointer(1)
	assertInvalid(t, metric.Validate())
	metric.CoverageRatio, metric.Limitations = floatPointer(0.5), nil
	assertInvalid(t, metric.Validate())
	metric.Limitations, metric.InputReferences = []string{"部分覆盖"}, nil
	assertInvalid(t, metric.Validate())
}

func TestExposureRejectsNonFiniteValues(t *testing.T) {
	metric := PopulationExposureMetric{
		Status: MetricAvailable, Quantity: floatPointer(math.NaN()), Unit: PopulationUnit,
		CoverageRatio: floatPointer(1), InputReferences: []string{"dataset://population/v1"},
	}
	assertInvalid(t, metric.Validate())
	metric.Quantity, metric.CoverageRatio = floatPointer(1), floatPointer(math.Inf(1))
	assertInvalid(t, metric.Validate())
}

func TestAdministrativeMatchStates(t *testing.T) {
	available := AdministrativeMatch{
		Status: AdminMatchAvailable, CoverageRatio: floatPointer(1),
		InputReferences: []string{"dataset://admin/v1"},
	}
	if err := available.Validate(); err != nil {
		t.Fatal(err)
	}
	partial := available
	partial.Status, partial.CoverageRatio = AdminMatchPartial, floatPointer(0.5)
	partial.Limitations = []string{"边界仅部分覆盖"}
	if err := partial.Validate(); err != nil {
		t.Fatal(err)
	}
	unavailable := AdministrativeMatch{Status: AdminMatchUnavailable, Limitations: []string{"缺少行政边界"}}
	if err := unavailable.Validate(); err != nil {
		t.Fatal(err)
	}
	unavailable.AdminCodes = []string{"510000"}
	assertInvalid(t, unavailable.Validate())
}
