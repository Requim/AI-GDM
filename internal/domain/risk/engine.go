package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

// Engine 使用版本化纯规则和已注册模型能力生成风险研判。
type Engine struct {
	capability ModelCapability
}

// NewEngine 创建绑定专用灾种、模型和数据集的确定性风险引擎。
func NewEngine(capability ModelCapability) (Engine, error) {
	if err := validateCapability(capability); err != nil {
		return Engine{}, err
	}
	return Engine{capability: capability}, nil
}

// Evaluate 计算整体等级、输入质量置信度、触发因素和数据状态。
func (e Engine) Evaluate(input Input) (Assessment, error) {
	if err := validateInput(input, e.capability); err != nil {
		return Assessment{}, err
	}
	references := collectInputReferences(input)
	dataStatus := deriveDataStatus(input.Snapshot, input.EvaluatedAt)
	assessment := baseAssessment(input, references, dataStatus)
	contextStatus, contextFactors, contextLimits := buildWeatherFactors(input)
	assessment.ContextStatus = contextStatus
	if dataStatus == DataExpired {
		assessment.Factors = append([]Factor{expiredPrimaryFactor(input.Snapshot)}, contextFactors...)
		assessment.Limitations = assessmentLimitations(input, true, contextLimits...)
		return finalizeAssessment(assessment)
	}
	decision, primary := deriveDecision(input.Snapshot, input.Zones, dataStatus)
	assessment.Decision = &decision
	assessment.Factors = append([]Factor{primary}, contextFactors...)
	assessment.Confidence = deriveConfidence(input.Snapshot, dataStatus)
	assessment.Limitations = assessmentLimitations(input, false, contextLimits...)
	return finalizeAssessment(assessment)
}

func baseAssessment(input Input, references []string, dataStatus DataStatus) Assessment {
	status := AssessmentAvailable
	if dataStatus == DataFallback {
		status = AssessmentDegraded
	} else if dataStatus == DataExpired {
		status = AssessmentInsufficientData
	}
	return Assessment{
		HazardType: input.Snapshot.HazardType, SnapshotID: input.Snapshot.ID,
		Status: status, DataStatus: dataStatus, ContextStatus: ContextAbsent,
		Confidence:  confidenceUnavailable("主模型数据已过期，未生成风险等级"),
		RuleVersion: RuleVersion, EvaluatedAt: input.EvaluatedAt,
		InputReferences: references,
	}
}

func deriveDecision(snapshot hazard.Snapshot, zones []hazard.RiskZone,
	status DataStatus,
) (Decision, Factor) {
	level, highestIDs := highestZoneLevel(zones)
	basis := "highest_zone_level"
	description := fmt.Sprintf("整体等级取 %d 个风险区中的最高等级 %s", len(zones), level)
	if len(zones) == 0 {
		basis = "no_elevated_zone"
		description = "完整快照未产生高于低风险阈值的风险区，整体等级为低"
	}
	decision := Decision{Level: level, ZoneCount: len(zones), HighestZoneIDs: highestIDs, Basis: basis}
	factor := Factor{
		Code: "model_risk_zones", Role: FactorDecisionBasis, AffectsLevel: true,
		DataStatus: status, Description: description,
		Metrics:         []Metric{{Code: "zone_count", Label: "风险区数量", Value: float64(len(zones)), Unit: "count"}},
		InputReferences: compactReferences(snapshot.RasterReference, snapshot.Source.SHA256),
	}
	return decision, factor
}

func highestZoneLevel(zones []hazard.RiskZone) (hazard.RiskLevel, []string) {
	level := hazard.RiskLow
	highest := make([]string, 0)
	for _, zone := range zones {
		rank, current := riskLevelRank(zone.Level), riskLevelRank(level)
		if rank > current {
			level, highest = zone.Level, []string{zone.ID}
		} else if rank == current {
			highest = append(highest, zone.ID)
		}
	}
	sort.Strings(highest)
	return level, highest
}

func finalizeAssessment(value Assessment) (Assessment, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return Assessment{}, fmt.Errorf("序列化风险研判标识输入: %w", err)
	}
	digest := sha256.Sum256(payload)
	value.ID = "risk-" + hex.EncodeToString(digest[:8])
	return value, nil
}
