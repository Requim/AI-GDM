package spatialanalysis

import (
	"math"
	"testing"
	"time"
)

func TestAreaAllowsUnionBelowZoneSum(t *testing.T) {
	input := validAnalysisInput()
	input.Area.TotalSquareMeters = 100
	got, err := NewAnalysis(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Area.TotalSquareMeters != 100 {
		t.Fatalf("total area = %v", got.Area.TotalSquareMeters)
	}
}

func TestAreaRejectsImpossibleTotals(t *testing.T) {
	tests := []struct {
		name  string
		total float64
	}{
		{name: "above sum", total: 151},
		{name: "below largest zone", total: 89},
		{name: "negative", total: -1},
		{name: "not finite", total: math.NaN()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validAnalysisInput()
			input.Area.TotalSquareMeters = test.total
			_, err := NewAnalysis(input)
			assertInvalid(t, err)
		})
	}
}

func TestAreaRejectsInvalidZoneAreaAndMethod(t *testing.T) {
	input := validAnalysisInput()
	input.Zones[0].Area.SquareMeters = 0
	_, err := NewAnalysis(input)
	assertInvalid(t, err)
	input = validAnalysisInput()
	input.Area.Method = "planar-degrees"
	_, err = NewAnalysis(input)
	assertInvalid(t, err)
}

func TestAnalysisRequiresUTCAndSnapshotID(t *testing.T) {
	input := validAnalysisInput()
	input.CalculatedAt = time.Date(2026, 8, 25, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	_, err := NewAnalysis(input)
	assertInvalid(t, err)
	input = validAnalysisInput()
	input.SnapshotID = " "
	_, err = NewAnalysis(input)
	assertInvalid(t, err)
}

func TestNewAnalysisRejectsDuplicateZoneIDs(t *testing.T) {
	input := validAnalysisInput()
	input.Zones[1].ZoneID = input.Zones[0].ZoneID
	_, err := NewAnalysis(input)
	assertInvalid(t, err)
}

func TestAvailableContextRequiresDatasetReferences(t *testing.T) {
	input := validAnalysisInput()
	input.DatasetReferences = nil
	_, err := NewAnalysis(input)
	assertInvalid(t, err)
}

func TestZeroZoneAnalysisRequiresCompletedLimitation(t *testing.T) {
	input := AnalysisInput{
		SnapshotID: "snapshot-empty",
		Area: AreaCalculation{
			Method: AreaMethod, InputReferences: []string{"geometry://snapshot-empty"},
		},
		CalculatedAt:    time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC),
		InputReferences: []string{"snapshot://empty"},
	}
	_, err := NewAnalysis(input)
	assertInvalid(t, err)
}

func TestValidateRejectsNonCanonicalOrTamperedAnalysis(t *testing.T) {
	value, err := NewAnalysis(validAnalysisInput())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Analysis)
	}{
		{name: "duplicate dataset", mutate: func(v *Analysis) {
			v.DatasetReferences = append(v.DatasetReferences, v.DatasetReferences[0])
		}},
		{name: "wrong status", mutate: func(v *Analysis) { v.Status = AnalysisAreaOnly }},
		{name: "wrong id", mutate: func(v *Analysis) { v.ID = "spatial-invalid" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyValue := value
			copyValue.DatasetReferences = append([]string(nil), value.DatasetReferences...)
			test.mutate(&copyValue)
			assertInvalid(t, copyValue.Validate())
		})
	}
}

func TestNormalizationCopiesMetricPointers(t *testing.T) {
	input := validAnalysisInput()
	got, err := NewAnalysis(input)
	if err != nil {
		t.Fatal(err)
	}
	*input.Zones[0].Population.Quantity = 999
	*input.Zones[0].Population.CoverageRatio = 0.25
	zone := got.Zones[1]
	if *zone.Population.Quantity == 999 || *zone.Population.CoverageRatio == 0.25 {
		t.Fatal("analysis retained pointers owned by input")
	}
}
