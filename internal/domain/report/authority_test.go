package report

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	evacuationdomain "github.com/Requim/AI-GDM/internal/domain/evacuation"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	riskdomain "github.com/Requim/AI-GDM/internal/domain/risk"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestHazardAuthorityEnforcesDomainEnumsAndQualityCombinations(t *testing.T) {
	base := validHazardDomainAnalysis()
	assertAuthorityAccepted(t, authorityForDomainTest(AuthorityHazardSnapshot, "snapshot-1", riskdomain.RuleVersion, AuthoritySchemaHazardV1, base))
	fallback := base
	fallback.DataStatus, fallback.ConfidenceLevel = string(riskdomain.DataFallback), string(riskdomain.ConfidenceLow)
	assertAuthorityAccepted(t, authorityForDomainTest(AuthorityHazardSnapshot, "snapshot-1", riskdomain.RuleVersion, AuthoritySchemaHazardV1, fallback))
	debrisFlow := base
	debrisFlow.HazardType, debrisFlow.ConfidenceLevel = string(hazarddomain.TypeDebrisFlow), string(riskdomain.ConfidenceMedium)
	assertAuthorityAccepted(t, authorityForDomainTest(AuthorityHazardSnapshot, "snapshot-1", riskdomain.RuleVersion, AuthoritySchemaHazardV1, debrisFlow))
	zeroZones := base
	zeroZones.AffectedAreaSquareMeters, zeroZones.RiskZoneCount, zeroZones.RiskLevel = 0, 0, string(hazarddomain.RiskLow)
	assertAuthorityAccepted(t, authorityForDomainTest(AuthorityHazardSnapshot, "snapshot-1", riskdomain.RuleVersion, AuthoritySchemaHazardV1, zeroZones))

	tests := []struct {
		name   string
		mutate func(*HazardAuthorityAnalysis)
	}{
		{name: "未实现灾种", mutate: func(value *HazardAuthorityAnalysis) { value.HazardType = string(hazarddomain.TypeEarthquake) }},
		{name: "风险等级", mutate: func(value *HazardAuthorityAnalysis) { value.RiskLevel = "critical" }},
		{name: "数据状态", mutate: func(value *HazardAuthorityAnalysis) { value.DataStatus = "fresh" }},
		{name: "过期数据仍有结论", mutate: func(value *HazardAuthorityAnalysis) {
			value.DataStatus, value.ConfidenceLevel = string(riskdomain.DataExpired), string(riskdomain.ConfidenceUnavailable)
		}},
		{name: "当前数据低置信", mutate: func(value *HazardAuthorityAnalysis) { value.ConfidenceLevel = string(riskdomain.ConfidenceLow) }},
		{name: "未知置信等级", mutate: func(value *HazardAuthorityAnalysis) { value.ConfidenceLevel = "medium_high" }},
		{name: "回退数据高置信", mutate: func(value *HazardAuthorityAnalysis) {
			value.DataStatus, value.ConfidenceLevel = string(riskdomain.DataFallback), string(riskdomain.ConfidenceHigh)
		}},
		{name: "零风险区高等级", mutate: func(value *HazardAuthorityAnalysis) {
			value.AffectedAreaSquareMeters, value.RiskZoneCount = 0, 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			assertInvalidDomainAuthority(t, authorityForDomainTest(AuthorityHazardSnapshot, "snapshot-1", riskdomain.RuleVersion, AuthoritySchemaHazardV1, value))
		})
	}
}

func TestRouteAuthorityRequiresSupportedSafeMode(t *testing.T) {
	base := validRouteDomainAnalysis()
	for _, mode := range []evacuationdomain.TravelMode{
		evacuationdomain.TravelDriving, evacuationdomain.TravelWalking, evacuationdomain.TravelTransit,
	} {
		value := base
		value.Mode = string(mode)
		assertAuthorityAccepted(t, authorityForDomainTest(AuthorityEvacuationRoute, value.RouteAnalysisID, value.RuleVersion, AuthoritySchemaRouteV1, value))
	}
	unsafe := base
	unsafe.IntersectsRiskZone = true
	assertInvalidDomainAuthority(t, authorityForDomainTest(AuthorityEvacuationRoute, unsafe.RouteAnalysisID, unsafe.RuleVersion, AuthoritySchemaRouteV1, unsafe))
	unsupported := base
	unsupported.Mode = "cycling"
	assertInvalidDomainAuthority(t, authorityForDomainTest(AuthorityEvacuationRoute, unsupported.RouteAnalysisID, unsupported.RuleVersion, AuthoritySchemaRouteV1, unsupported))
}

func TestLossAuthorityEnforcesStatusAndDerivedConfidenceBand(t *testing.T) {
	base := validLossDomainAnalysis()
	for _, test := range []struct {
		confidence float64
		band       string
	}{{0.8, "high"}, {0.5, "moderate"}, {0.25, "low"}, {0.24, "very_low"}} {
		value := base
		value.Confidence, value.ConfidenceBand = test.confidence, test.band
		assertAuthorityAccepted(t, authorityForDomainTest(AuthorityLossAssessment, value.AssessmentID, value.FormulaVersion, AuthoritySchemaLossV1, value))
	}
	insufficient := base
	insufficient.Status = string(lossdomain.AssessmentInsufficientData)
	assertAuthorityAccepted(t, authorityForDomainTest(AuthorityLossAssessment, insufficient.AssessmentID, insufficient.FormulaVersion, AuthoritySchemaLossV1, insufficient))
	for _, mutate := range []func(*LossAuthorityAnalysis){
		func(value *LossAuthorityAnalysis) { value.Status = "partial" },
		func(value *LossAuthorityAnalysis) { value.ConfidenceBand = "medium" },
		func(value *LossAuthorityAnalysis) { value.Confidence, value.ConfidenceBand = 0.5, "high" },
	} {
		value := base
		mutate(&value)
		assertInvalidDomainAuthority(t, authorityForDomainTest(AuthorityLossAssessment, value.AssessmentID, value.FormulaVersion, AuthoritySchemaLossV1, value))
	}
}

func TestSurvivalAuthorityBindsHistoricalReplaySafetyFields(t *testing.T) {
	base := validSurvivalDomainAnalysis()
	value := authorityForDomainTest(AuthoritySurvivalAssessment, base.AssessmentID, base.ModelVersion, AuthoritySchemaSurvivalV1, base)
	canonical, err := value.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"assessmentId", "caseId", "factors", "humanReviewStatus", "limitations", "modelVersion", "priority", "probabilityBand",
		"probabilityHigh", "probabilityLow", "scenarioDigest", "scenarioId", "score", "scoreBand", "usage",
	}
	if strings.Join(canonical.ImmutableFields, ",") != strings.Join(expected, ",") {
		t.Fatalf("immutableFields=%v", canonical.ImmutableFields)
	}
	var decoded SurvivalAuthorityAnalysis
	if err = json.Unmarshal(canonical.AnalysisJSON, &decoded); err != nil || !reflect.DeepEqual(decoded, base) {
		t.Fatalf("analysis=%+v err=%v", decoded, err)
	}
}

func TestSurvivalAuthorityAcceptsDeterministicBandBoundaries(t *testing.T) {
	base := validSurvivalDomainAnalysis()
	tests := []struct {
		score           int
		scoreBand, band string
		low, high       float64
		priority        survivaldomain.Priority
	}{
		{80, "high", string(survivaldomain.ProbabilityHigh), 0.60, 0.85, survivaldomain.PriorityImmediate},
		{60, "moderate", string(survivaldomain.ProbabilityModerate), 0.35, 0.59, survivaldomain.PriorityUrgent},
		{30, "low", string(survivaldomain.ProbabilityLow), 0.15, 0.34, survivaldomain.PriorityElevated},
		{10, "very_low", string(survivaldomain.ProbabilityVeryLow), 0.05, 0.14, survivaldomain.PriorityRoutine},
	}
	for _, test := range tests {
		value := base
		value.Score, value.ScoreBand, value.ProbabilityBand = test.score, test.scoreBand, test.band
		value.ProbabilityLow, value.ProbabilityHigh, value.Priority = test.low, test.high, string(test.priority)
		assertAuthorityAccepted(t, authorityForDomainTest(AuthoritySurvivalAssessment, value.AssessmentID, value.ModelVersion, AuthoritySchemaSurvivalV1, value))
	}
}

func TestSurvivalAuthorityRejectsTamperedBindingsAndCombinations(t *testing.T) {
	base := validSurvivalDomainAnalysis()
	tests := []struct {
		name   string
		mutate func(*SurvivalAuthorityAnalysis)
	}{
		{name: "评估标识", mutate: func(value *SurvivalAuthorityAnalysis) { value.AssessmentID = sha256DomainTest("c") }},
		{name: "案例标识", mutate: func(value *SurvivalAuthorityAnalysis) { value.CaseID = "bad case" }},
		{name: "场景标识", mutate: func(value *SurvivalAuthorityAnalysis) { value.ScenarioID = "bad scenario" }},
		{name: "场景摘要", mutate: func(value *SurvivalAuthorityAnalysis) { value.ScenarioDigest = "sha256:ABC" }},
		{name: "人工复核", mutate: func(value *SurvivalAuthorityAnalysis) { value.HumanReviewStatus = "optional" }},
		{name: "分数", mutate: func(value *SurvivalAuthorityAnalysis) { value.Score = 101 }},
		{name: "分数等级", mutate: func(value *SurvivalAuthorityAnalysis) { value.ScoreBand = "high" }},
		{name: "概率等级", mutate: func(value *SurvivalAuthorityAnalysis) { value.ProbabilityBand = "high" }},
		{name: "概率区间", mutate: func(value *SurvivalAuthorityAnalysis) { value.ProbabilityHigh = 0.58 }},
		{name: "优先级", mutate: func(value *SurvivalAuthorityAnalysis) { value.Priority = "routine" }},
		{name: "用途模式", mutate: func(value *SurvivalAuthorityAnalysis) { value.Usage.Mode = "live" }},
		{name: "合成输入", mutate: func(value *SurvivalAuthorityAnalysis) { value.Usage.SyntheticInput = false }},
		{name: "实时用途", mutate: func(value *SurvivalAuthorityAnalysis) { value.Usage.LiveUseAllowed = true }},
		{name: "安全声明", mutate: func(value *SurvivalAuthorityAnalysis) {
			value.Usage.Disclaimer = strings.Repeat("限", maxAuthorityDisclaimerRunes+1)
		}},
		{name: "因素为空", mutate: func(value *SurvivalAuthorityAnalysis) { value.Factors = nil }},
		{name: "因素过多", mutate: func(value *SurvivalAuthorityAnalysis) {
			value.Factors = make([]string, maxSurvivalAuthorityItems+1)
			for index := range value.Factors {
				value.Factors[index] = "确定性因素"
			}
		}},
		{name: "因素过长", mutate: func(value *SurvivalAuthorityAnalysis) {
			value.Factors = []string{strings.Repeat("因", maxSurvivalAuthorityRunes+1)}
		}},
		{name: "限制为空", mutate: func(value *SurvivalAuthorityAnalysis) { value.Limitations = []string{} }},
		{name: "限制过长", mutate: func(value *SurvivalAuthorityAnalysis) {
			value.Limitations = []string{strings.Repeat("限", maxSurvivalAuthorityRunes+1)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := base
			test.mutate(&analysis)
			value := authorityForDomainTest(AuthoritySurvivalAssessment, base.AssessmentID, base.ModelVersion, AuthoritySchemaSurvivalV1, analysis)
			assertInvalidDomainAuthority(t, value)
		})
	}
}

func TestSurvivalAuthorityRejectsNestedUsageFields(t *testing.T) {
	base := validSurvivalDomainAnalysis()
	payload := marshalDomainAnalysis(base)
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	object["usage"].(map[string]any)["personName"] = "张三"
	value := authorityForDomainTest(AuthoritySurvivalAssessment, base.AssessmentID, base.ModelVersion, AuthoritySchemaSurvivalV1, object)
	if _, err := value.Canonical(); !errors.Is(err, ErrUnsafeStoredAnalysis) {
		t.Fatalf("Canonical() error=%v", err)
	}
}

func validHazardDomainAnalysis() HazardAuthorityAnalysis {
	return HazardAuthorityAnalysis{
		AffectedAreaSquareMeters: 100, ConfidenceLevel: string(riskdomain.ConfidenceHigh),
		DataStatus: string(riskdomain.DataCurrent), HazardType: string(hazarddomain.TypeLandslide),
		RiskLevel: string(hazarddomain.RiskHigh), RiskZoneCount: 2,
		RuleVersion: riskdomain.RuleVersion, SnapshotID: "snapshot-1",
	}
}

func validRouteDomainAnalysis() RouteAuthorityAnalysis {
	return RouteAuthorityAnalysis{
		DistanceMeters: 1200, DurationSeconds: 600, Mode: string(evacuationdomain.TravelDriving), Rank: 1,
		RiskScore: 10, RiskScoreAvailable: true, RouteAnalysisID: "route-analysis-1",
		RouteID: "provider-route-1", RuleVersion: "route-v1", SnapshotID: "snapshot-1",
	}
}

func validLossDomainAnalysis() LossAuthorityAnalysis {
	return LossAuthorityAnalysis{
		AffectedPopulation: 10, AssessmentID: "loss-1", ConditionalCentralCents: "2000",
		ConditionalHighCents: "3000", ConditionalLowCents: "1000", Confidence: 0.8,
		ConfidenceBand: "high", FormulaVersion: lossdomain.FormulaVersion, ImpactAreaSquareMeters: 100,
		SnapshotID: "snapshot-1", Status: string(lossdomain.AssessmentAvailable),
	}
}

func validSurvivalDomainAnalysis() SurvivalAuthorityAnalysis {
	return SurvivalAuthorityAnalysis{
		AssessmentID: sha256DomainTest("a"), CaseID: "case-1", HumanReviewStatus: "required",
		Factors:      []string{"失联时间处于四小时内", "搜救输入仍有缺口"},
		Limitations:  []string{"仅用于历史案例回放", "必须由专业人员复核"},
		ModelVersion: survivaldomain.ModelVersion, Priority: string(survivaldomain.PriorityUrgent),
		ProbabilityBand: string(survivaldomain.ProbabilityModerate), ProbabilityHigh: 0.59,
		ProbabilityLow: 0.35, ScenarioDigest: sha256DomainTest("b"), ScenarioID: "scenario-1",
		Score: 60, ScoreBand: "moderate", Usage: survivaldomain.HistoricalReplayUsage(),
	}
}

func authorityForDomainTest(kind AuthorityKind, id, version, schema string, analysis any) Authority {
	return Authority{
		Kind: kind, ID: id, Version: version, SchemaVersion: schema,
		AnalysisJSON: marshalDomainAnalysis(analysis), ResolvedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
	}
}

func marshalDomainAnalysis(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func sha256DomainTest(char string) string { return "sha256:" + strings.Repeat(char, 64) }

func assertAuthorityAccepted(t *testing.T, value Authority) {
	t.Helper()
	if _, err := value.Canonical(); err != nil {
		t.Fatal(err)
	}
}

func assertInvalidDomainAuthority(t *testing.T, value Authority) {
	t.Helper()
	if _, err := value.Canonical(); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("Canonical() error=%v", err)
	}
}
