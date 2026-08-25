package postgres

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	original := hazard.Snapshot{
		Thresholds:  []hazard.RiskThreshold{{Level: hazard.RiskLow, Minimum: 0, Maximum: 1}},
		Limitations: []string{"辅助研判"},
	}
	thresholds, source, limitations, err := snapshotJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded hazard.Snapshot
	if err = decodeSnapshotJSON(&decoded, thresholds, source, limitations); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Thresholds) != 1 || decoded.Limitations[0] != "辅助研判" {
		value, _ := json.Marshal(decoded)
		t.Fatalf("往返结果错误: %s", value)
	}
}

func TestRiskZoneSQLPersistsAreaCalculated(t *testing.T) {
	for name, query := range map[string]string{"save": saveZoneSQL, "select": selectZonesSQL} {
		if strings.Count(query, "area_calculated") != 1 {
			t.Fatalf("%s SQL 未唯一包含 area_calculated: %s", name, query)
		}
	}
	if !strings.Contains(saveZoneSQL, "$12") {
		t.Fatalf("saveZoneSQL 参数数量未同步: %s", saveZoneSQL)
	}
}
