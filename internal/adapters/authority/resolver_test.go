package authority

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestResolverProjectsFourExplicitSchemasWithoutPII(t *testing.T) {
	fixture := newResolverFixture(t)
	routeRef, err := fixture.resolver.RecordRoute(context.Background(), fixture.snapshot,
		fixture.route, applicationevacuation.RouteSafetyRuleVersion)
	if err != nil || routeRef == nil {
		t.Fatalf("记录路线权威引用: ref=%+v err=%v", routeRef, err)
	}
	cases := []struct {
		name string
		ref  report.AnalysisReference
		keys []string
	}{
		{name: "hazard", ref: report.AnalysisReference{Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID}, keys: hazardAuthorityKeys},
		{name: "route", ref: *routeRef, keys: routeAuthorityKeys},
		{name: "loss", ref: report.AnalysisReference{Kind: report.AuthorityLossAssessment, ID: fixture.loss.value.ID}, keys: lossAuthorityKeys},
		{name: "survival", ref: report.AnalysisReference{Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID}, keys: survivalAuthorityKeys},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, resolveErr := fixture.resolver.Resolve(context.Background(), test.ref)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			assertAuthorityKeys(t, value, test.keys)
			assertAuthorityHasNoPII(t, value.AnalysisJSON)
		})
	}
}

func TestResolverRejectsRuleFormulaModelAndCacheDrift(t *testing.T) {
	t.Run("risk rule", func(t *testing.T) {
		fixture := newResolverFixture(t)
		fixture.risk.value.Assessment.RuleVersion = "risk-rules-v2"
		_, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
			Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
		})
		assertErrorIs(t, err, report.ErrInvalidAuthority)
	})
	t.Run("loss formula", func(t *testing.T) {
		fixture := newResolverFixture(t)
		fixture.loss.value.FormulaVersion = "loss-formula-v3"
		_, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
			Kind: report.AuthorityLossAssessment, ID: fixture.loss.value.ID,
		})
		assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
	})
	t.Run("route cache extra field", func(t *testing.T) {
		fixture := newResolverFixture(t)
		ref := recordRoute(t, fixture)
		injectCacheField(t, fixture.cache, routeCachePrefix+ref.ID, "address", "secret")
		_, err := fixture.resolver.Resolve(context.Background(), ref)
		assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
	})
	t.Run("route rule", func(t *testing.T) {
		fixture := newResolverFixture(t)
		ref := recordRoute(t, fixture)
		injectCacheField(t, fixture.cache, routeCachePrefix+ref.ID, "ruleVersion", "route-rules-v2")
		_, err := fixture.resolver.Resolve(context.Background(), ref)
		assertErrorIs(t, err, report.ErrInvalidAuthority)
	})
	t.Run("survival model", func(t *testing.T) {
		fixture := newResolverFixture(t)
		service := &mutatingSurvivalService{base: fixture.survival, mutate: func(value *applicationsurvival.ReplayAssessment) {
			value.Assessment.ModelVersion = "survival-rules-v2"
		}}
		resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
			fixture.catalog, service, fixture.cache, fixedClock{fixture.now})
		if err != nil {
			t.Fatal(err)
		}
		_, err = resolver.Resolve(context.Background(), report.AnalysisReference{
			Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID,
		})
		assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
	})
}

func TestResolverPreservesRequestedObjectNotFound(t *testing.T) {
	fixture := newResolverFixture(t)
	refs := []report.AnalysisReference{
		{Kind: report.AuthorityHazardSnapshot, ID: "missing-snapshot"},
		{Kind: report.AuthorityEvacuationRoute, ID: "missing-route"},
		{Kind: report.AuthorityLossAssessment, ID: "missing-loss"},
		{Kind: report.AuthoritySurvivalAssessment, ID: "missing-survival"},
	}
	for _, ref := range refs {
		_, err := fixture.resolver.Resolve(context.Background(), ref)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s/%s error=%v, want ErrNotFound", ref.Kind, ref.ID, err)
		}
	}
}

func TestLossAuthorityUsesExactDecimalCents(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.loss.value.ConditionalLowCents = 9_007_199_254_740_991
	fixture.loss.value.ConditionalMidCents = 9_007_199_254_740_992
	fixture.loss.value.ConditionalHighCents = 9_007_199_254_740_993
	rebound, err := loss.BindAssessmentIdentity(fixture.loss.value)
	if err != nil {
		t.Fatal(err)
	}
	fixture.loss.value = rebound
	value, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
		Kind: report.AuthorityLossAssessment, ID: rebound.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var analysis report.LossAuthorityAnalysis
	if err = json.Unmarshal(value.AnalysisJSON, &analysis); err != nil {
		t.Fatal(err)
	}
	if analysis.ConditionalLowCents != "9007199254740991" ||
		analysis.ConditionalCentralCents != "9007199254740992" ||
		analysis.ConditionalHighCents != "9007199254740993" {
		t.Fatalf("金额十进制字符串失真: %+v", analysis)
	}
}

func TestHazardAuthorityUsesDeduplicatedSpatialArea(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.risk.value.Zones = append(fixture.risk.value.Zones,
		ports.HazardZoneSummary{ID: "zone-2", SnapshotID: fixture.risk.value.Snapshot.ID,
			Level: hazard.RiskHigh})
	fixture.risk.value.TotalZoneCount = 2
	fixture.risk.value.Assessment.Decision.ZoneCount = 2
	fixture.risk.value.Assessment.Decision.HighestZoneIDs = []string{"zone-1", "zone-2"}
	analysis, err := spatialanalysis.NewAnalysis(spatialanalysis.AnalysisInput{
		SnapshotID: fixture.risk.value.Snapshot.ID,
		Area: spatialanalysis.AreaCalculation{Method: spatialanalysis.AreaMethod,
			TotalSquareMeters: 150, InputReferences: []string{"geometry://union"}},
		Zones: []spatialanalysis.ZoneResult{
			unavailableSpatialZone("zone-1"), unavailableSpatialZone("zone-2"),
		},
		CalculatedAt: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.spatial.value = analysis
	value, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
		Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dto report.HazardAuthorityAnalysis
	if err = json.Unmarshal(value.AnalysisJSON, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.AffectedAreaSquareMeters != 150 {
		t.Fatalf("权威面积=%v，未使用空间去重总面积", dto.AffectedAreaSquareMeters)
	}
}

func assertAuthorityKeys(t *testing.T, value report.Authority, keys []string) {
	t.Helper()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value.AnalysisJSON, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(keys) || len(value.ImmutableFields) != len(keys) {
		t.Fatalf("固定字段数量错误: object=%v immutable=%v", object, value.ImmutableFields)
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			t.Fatalf("缺少固定字段 %s: %s", key, value.AnalysisJSON)
		}
	}
}

func assertAuthorityHasNoPII(t *testing.T, payload []byte) {
	t.Helper()
	lower := strings.ToLower(string(payload))
	for _, forbidden := range []string{
		`"name"`, `"phone"`, `"address"`, `"origin"`, `"destination"`,
		`"geometry"`, `"steps"`, `"source"`, `"cookie"`, `"token"`,
		"张三", "13800138000", "详细住址",
	} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("权威 DTO 包含禁止字段或个人信息 %q: %s", forbidden, payload)
		}
	}
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error=%v, want %v", err, target)
	}
}

func recordRoute(t *testing.T, fixture resolverFixture) report.AnalysisReference {
	t.Helper()
	ref, err := fixture.resolver.RecordRoute(context.Background(), fixture.snapshot,
		fixture.route, applicationevacuation.RouteSafetyRuleVersion)
	if err != nil || ref == nil {
		t.Fatalf("RecordRoute() ref=%+v err=%v", ref, err)
	}
	return *ref
}

func injectCacheField(t *testing.T, cache *jsonCache, key, field string, value any) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(cache.data[key], &object); err != nil {
		t.Fatal(err)
	}
	object[field] = value
	payload, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	cache.data[key] = payload
}

var hazardAuthorityKeys = []string{
	"affectedAreaSquareMeters", "confidenceLevel", "dataStatus", "hazardType",
	"riskLevel", "riskZoneCount", "ruleVersion", "snapshotId",
}

var routeAuthorityKeys = []string{
	"distanceMeters", "durationSeconds", "intersectsRiskZone", "mode", "rank",
	"riskScore", "riskScoreAvailable", "routeAnalysisId", "routeId", "ruleVersion", "snapshotId",
}

var lossAuthorityKeys = []string{
	"affectedPopulation", "assessmentId", "conditionalCentralCents", "conditionalHighCents",
	"conditionalLowCents", "confidence", "confidenceBand", "formulaVersion",
	"impactAreaSquareMeters", "snapshotId", "status",
}

var survivalAuthorityKeys = []string{
	"assessmentId", "caseId", "factors", "humanReviewStatus", "limitations", "modelVersion", "priority", "probabilityBand",
	"probabilityHigh", "probabilityLow", "scenarioDigest", "scenarioId", "score", "scoreBand", "usage",
}
