package postgres

import (
	"encoding/json"
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
