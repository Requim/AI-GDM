package evacuation

import (
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestRiskFreshnessLimitationDistinguishesFallbackFromExpiry(t *testing.T) {
	snapshot := hazard.Snapshot{Status: hazard.SnapshotStale}
	snapshot.Source.Stale = true
	snapshot.Source.QualityFlags = []string{fallbackQualityFlag}
	limitation := riskFreshnessLimitation(snapshot, "路线安全结果")
	if !strings.Contains(limitation, "最后成功回退结果") || strings.Contains(limitation, "已过期") {
		t.Fatalf("回退限制语义错误: %q", limitation)
	}
}

func TestRiskFreshnessLimitationMarksUnknownStaleForReview(t *testing.T) {
	snapshot := hazard.Snapshot{Status: hazard.SnapshotStale}
	limitation := riskFreshnessLimitation(snapshot, "设施筛选结果")
	if !strings.Contains(limitation, "标记为需复核") || strings.Contains(limitation, "已过期") {
		t.Fatalf("stale 限制语义错误: %q", limitation)
	}
}

func TestRiskFreshnessLimitationOmitsCurrentSnapshot(t *testing.T) {
	if limitation := riskFreshnessLimitation(hazard.Snapshot{Status: hazard.SnapshotAvailable}, "设施筛选结果"); limitation != "" {
		t.Fatalf("当前快照不应追加限制: %q", limitation)
	}
}
