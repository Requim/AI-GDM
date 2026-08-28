package evacuation

import (
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const fallbackQualityFlag = "fallback_last_success"

func riskFreshnessLimitation(snapshot hazard.Snapshot, subject string) string {
	if hasQualityFlag(snapshot.Source.QualityFlags, fallbackQualityFlag) {
		return fmt.Sprintf("风险区快照来自最后成功回退结果，%s需复核实时变化和数据时效", subject)
	}
	if snapshot.Status == hazard.SnapshotStale || snapshot.Source.Stale {
		return fmt.Sprintf("风险区快照标记为需复核，%s需核对数据时效", subject)
	}
	return ""
}

func hasQualityFlag(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
