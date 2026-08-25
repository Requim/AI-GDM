package spatialanalysis

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewAnalysisNormalizesWithoutMutatingInput(t *testing.T) {
	input := validAnalysisInput()
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewAnalysis(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("NewAnalysis() 修改了输入")
	}
	if got.SnapshotID != "snapshot-001" || got.Status != AnalysisAvailable {
		t.Fatalf("analysis identity/status = %q/%q", got.SnapshotID, got.Status)
	}
	if got.Zones[0].ZoneID != "zone-001" || got.Zones[1].ZoneID != "zone-002" {
		t.Fatalf("zones not sorted: %+v", got.Zones)
	}
	if len(got.DatasetReferences) != 2 || got.DatasetReferences[0] != "dataset://population/v1" {
		t.Fatalf("dataset references = %v", got.DatasetReferences)
	}
	if len(got.Zones[0].Administration.AdminCodes) != 2 {
		t.Fatalf("admin codes not deduplicated: %v", got.Zones[0].Administration.AdminCodes)
	}
	if !strings.HasPrefix(got.ID, "spatial-") || len(got.ID) != len("spatial-")+64 {
		t.Fatalf("analysis id = %q", got.ID)
	}
}

func TestAnalysisIDIgnoresCalculatedAt(t *testing.T) {
	firstInput := validAnalysisInput()
	secondInput := validAnalysisInput()
	secondInput.CalculatedAt = firstInput.CalculatedAt.Add(3 * time.Hour)
	first, err := NewAnalysis(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAnalysis(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("IDs differ by execution time: %s / %s", first.ID, second.ID)
	}
	if first.CalculatedAt.Equal(second.CalculatedAt) {
		t.Fatal("test did not retain different execution times")
	}
}

func TestAnalysisIDIncludesSnapshotDatasetAndResult(t *testing.T) {
	baseline, err := NewAnalysis(validAnalysisInput())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*AnalysisInput)
	}{
		{name: "snapshot", mutate: func(v *AnalysisInput) { v.SnapshotID = "snapshot-002" }},
		{name: "dataset", mutate: func(v *AnalysisInput) { v.DatasetReferences = append(v.DatasetReferences, "dataset://pois/v2") }},
		{name: "quantity", mutate: func(v *AnalysisInput) { v.Zones[0].Population.Quantity = floatPointer(121) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validAnalysisInput()
			test.mutate(&input)
			got, buildErr := NewAnalysis(input)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if got.ID == baseline.ID {
				t.Fatalf("ID did not include changed %s", test.name)
			}
		})
	}
}

func TestZeroZoneAnalysisIsCompletedAreaOnlyResult(t *testing.T) {
	input := AnalysisInput{
		SnapshotID: "snapshot-empty",
		Area: AreaCalculation{
			Method: AreaMethod, TotalSquareMeters: 0,
			InputReferences: []string{"geometry://snapshot-empty"},
		},
		CalculatedAt:    time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC),
		InputReferences: []string{"snapshot://empty"},
		Limitations:     []string{"完整快照未产生风险区；该空间分析批次已完成"},
	}
	got, err := NewAnalysis(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AnalysisAreaOnly || got.Area.TotalSquareMeters != 0 || len(got.Zones) != 0 {
		t.Fatalf("empty completed analysis = %+v", got)
	}
	if got.ID == "" {
		t.Fatal("completed empty analysis has no stable ID")
	}
}

func TestAnalysisDerivesPartialAndAreaOnlyStatuses(t *testing.T) {
	partialInput := validAnalysisInput()
	partialInput.Zones[0].POIs = POIExposureMetric{
		Status: MetricUnavailable, Unit: POIUnit, Limitations: []string{"缺少 POI 基线"},
	}
	partial, err := NewAnalysis(partialInput)
	if err != nil {
		t.Fatal(err)
	}
	areaOnlyInput := validAnalysisInput()
	areaOnlyInput.DatasetReferences = nil
	areaOnlyInput.Zones = []ZoneResult{unavailableZone("zone-001", 90), unavailableZone("zone-002", 60)}
	areaOnly, err := NewAnalysis(areaOnlyInput)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != AnalysisPartial || areaOnly.Status != AnalysisAreaOnly {
		t.Fatalf("statuses = %q / %q", partial.Status, areaOnly.Status)
	}
}

func TestUnavailableMetricsSerializeMissingValuesAsNull(t *testing.T) {
	metric := unavailableZone("zone-001", 1).Population
	payload, err := json.Marshal(metric)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `"quantity":null`) || !strings.Contains(text, `"coverageRatio":null`) {
		t.Fatalf("payload = %s", text)
	}
}
