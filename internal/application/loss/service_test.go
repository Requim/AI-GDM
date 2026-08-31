package loss

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type inputReaderStub struct {
	value  LossInputProjection
	err    error
	limits *RiskProjectionLimits
	readAt *time.Time
}

func (r inputReaderStub) ReadLossInput(_ context.Context, _ string, now time.Time,
	limits RiskProjectionLimits,
) (LossInputProjection, error) {
	if r.limits != nil {
		*r.limits = limits
	}
	if r.readAt != nil {
		*r.readAt = now
	}
	return r.value, r.err
}

type baselineReaderStub struct {
	set     lossdomain.BaselineSet
	err     error
	queries []BaselineQuery
}

func (r *baselineReaderStub) BaselineSet(_ context.Context,
	query BaselineQuery,
) (lossdomain.BaselineSet, error) {
	r.queries = append(r.queries, query)
	return r.set, r.err
}

func TestServiceSupportsGloballyDeduplicatedMultiZoneInputs(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	first := estimateFixture(t, now, projection, approvedBaselineSet(now, "v2026"))
	second := estimateFixture(t, now.Add(time.Hour), projection, approvedBaselineSet(now, "v2026"))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("跨时间重试结果不稳定: first=%+v second=%+v", first, second)
	}
	if first.AffectedPopulation != 70 || first.AffectedRoadMeters != 15 || first.AffectedFacilities != 2 {
		t.Fatalf("全局 feature 去重统计异常: %+v", first)
	}
	if first.ConditionalLowCents != 3500 || first.ConditionalMidCents != 7000 || first.ConditionalHighCents != 10500 {
		t.Fatalf("多区去重金额 = %d/%d/%d", first.ConditionalLowCents, first.ConditionalMidCents, first.ConditionalHighCents)
	}
	assertSelectedBaselineLevels(t, first)
}

func TestServiceRejectsDuplicateGlobalFeatureBeforeBaseline(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.Features[1].FeatureID = projection.Analysis.Features[0].FeatureID
	baselines := &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}
	assertEstimateError(t, now, projection, baselines, domain.ErrInsufficientData)
	if len(baselines.queries) != 0 {
		t.Fatalf("featureId 重复时不应读取基线: %v", baselines.queries)
	}
}

func TestServiceUsesBoundedAtomicProjectionReader(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection, limits, readAt := validLossProjection(now), RiskProjectionLimits{}, time.Time{}
	projection.Stats.ZoneCount = maxLossZones + 1
	baselines := &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}
	service := mustService(t, inputReaderStub{value: projection, limits: &limits, readAt: &readAt}, baselines, now)
	_, err := service.Estimate(context.Background(), EstimateInput{SnapshotID: projection.Snapshot.ID})
	if !errors.Is(err, domain.ErrInsufficientData) || limits != lossRiskProjectionLimits() || readAt != now || len(baselines.queries) != 0 {
		t.Fatalf("有界原子读取未生效: now=%s limits=%+v baselines=%v error=%v", readAt, limits, baselines.queries, err)
	}
}

func TestServiceRejectsProjectionBudgetsBeforeBaseline(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	limits := lossRiskProjectionLimits()
	cases := []func(*RiskProjectionStats){
		func(value *RiskProjectionStats) { value.MaxGeometryPoints = limits.MaxGeometryPointsPerZone + 1 },
		func(value *RiskProjectionStats) { value.MaxGeometryBytes = limits.MaxGeometryBytesPerZone + 1 },
		func(value *RiskProjectionStats) { value.TotalGeometryPoints = limits.MaxTotalGeometryPoints + 1 },
		func(value *RiskProjectionStats) { value.TotalGeometryBytes = limits.MaxTotalGeometryBytes + 1 },
		func(value *RiskProjectionStats) { value.SpatialJSONBytes = limits.MaxSpatialJSONBytes + 1 },
		func(value *RiskProjectionStats) { value.FeatureCount = limits.MaxFeatures + 1 },
		func(value *RiskProjectionStats) { value.ProjectionBytes = limits.MaxProjectionBytes + 1 },
		func(value *RiskProjectionStats) {
			value.ProjectionLimitationCount = limits.MaxProjectionLimitations + 1
		},
		func(value *RiskProjectionStats) {
			value.MaxProjectionLimitationBytes = limits.MaxProjectionLimitationBytes + 1
		},
		func(value *RiskProjectionStats) {
			value.ProjectionLimitationBytes = limits.MaxProjectionLimitationTotalBytes + 1
		},
	}
	for index, mutate := range cases {
		projection := validLossProjection(now)
		baselines := &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}
		mutate(&projection.Stats)
		assertEstimateError(t, now, projection, baselines, domain.ErrInsufficientData)
		if len(baselines.queries) != 0 {
			t.Fatalf("边界用例 %d 在 fail-closed 前读取了基线", index)
		}
	}
}

func TestServiceRejectsUnionAreaBelowLargestZone(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.TotalAreaSquareMeters = 1
	mustRebindLossProjection(t, &projection)
	baselines := &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}
	assertEstimateError(t, now, projection, baselines, domain.ErrInsufficientData)
	if len(baselines.queries) != 0 {
		t.Fatalf("不可能的并集面积在 fail-closed 前读取了基线: %v", baselines.queries)
	}
}

func TestServiceAllowsOnePercentAreaTolerance(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.TotalAreaSquareMeters = projection.Zones[0].AreaSquareM * 0.995
	mustRebindLossProjection(t, &projection)
	if value := estimateFixture(t, now, projection, approvedBaselineSet(now, "v2026")); value.ID == "" {
		t.Fatal("1% 内面积误差未生成评估")
	}

	projection = validLossProjection(now)
	projection.Analysis.TotalAreaSquareMeters = projection.Zones[0].AreaSquareM * 0.98
	mustRebindLossProjection(t, &projection)
	assertEstimateError(t, now, projection,
		&baselineReaderStub{set: approvedBaselineSet(now, "v2026")}, domain.ErrInsufficientData)
}

func TestServiceAccepts1000AndRejects1001Features(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	exact := projectionWithFeatureCount(now, maxLossFeatures)
	if value := estimateFixture(t, now, exact, approvedBaselineSet(now, "v2026")); len(value.Evidence.Exposures) != 998 {
		t.Fatalf("1000 个全局 feature 未完整进入证据: exposures=%d", len(value.Evidence.Exposures))
	}
	overflow := projectionWithFeatureCount(now, maxLossFeatures+1)
	baselines := &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}
	assertEstimateError(t, now, overflow, baselines, domain.ErrInsufficientData)
	if len(baselines.queries) != 0 {
		t.Fatal("1001 个 feature 应在基线读取前 fail-closed")
	}
}

func TestServicePreservesProvidedZeroAndRejectsPartial(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.Features[0].Quantity = 0
	projection.Analysis.Features[3].Quantity = 0
	projection.Analysis.Features[4].Quantity = 0
	mustRebindLossProjection(t, &projection)
	value := estimateFixture(t, now, projection, approvedBaselineSet(now, "v2026"))
	if value.ConditionalLowCents != 0 || value.AffectedRoadMeters != 0 || value.AffectedFacilities != 0 {
		t.Fatalf("权威真实零值未保留: %+v", value)
	}
	projection.Analysis.Features[3].Status = spatialdomain.MetricPartial
	assertEstimateError(t, now, projection, &baselineReaderStub{set: approvedBaselineSet(now, "v2026")}, domain.ErrInsufficientData)
}

func TestServiceChangesIdentityWhenFeatureInputChanges(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	firstProjection := validLossProjection(now)
	first := estimateFixture(t, now, firstProjection, approvedBaselineSet(now, "v2026"))
	secondProjection := validLossProjection(now)
	secondProjection.Analysis.Features[3].Quantity++
	mustRebindLossProjection(t, &secondProjection)
	second := estimateFixture(t, now, secondProjection, approvedBaselineSet(now, "v2026"))
	if first.ID == second.ID || first.InputDigest == second.InputDigest || first.ConditionalLowCents == second.ConditionalLowCents {
		t.Fatalf("决定性 feature 输入变化未改变身份或金额")
	}
}

func TestServiceRejectsFractionalFacilityAndInvalidProjectionTime(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	t.Run("设施数量小数", func(t *testing.T) {
		projection := validLossProjection(now)
		projection.Analysis.Features[0].Quantity = 1.5
		mustRebindLossProjection(t, &projection)
		assertEstimateError(t, now, projection,
			&baselineReaderStub{set: approvedBaselineSet(now, "v2026")}, domain.ErrInsufficientData)
	})
	for name, mutate := range map[string]func(*LossInputProjection){
		"采集时间在未来": func(value *LossInputProjection) {
			value.Analysis.ProjectionCollectedAt = now.Add(time.Hour)
			value.Analysis.ProjectionValidTo = now.Add(2 * time.Hour)
		},
		"投影已过期": func(value *LossInputProjection) {
			value.Analysis.ProjectionCollectedAt = now.Add(-3 * time.Hour)
			value.Analysis.ProjectionValidFrom = now.Add(-4 * time.Hour)
			value.Analysis.ProjectionValidTo = now.Add(-time.Hour)
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := validLossProjection(now)
			mutate(&projection)
			mustRebindLossProjection(t, &projection)
			assertEstimateError(t, now, projection,
				&baselineReaderStub{set: approvedBaselineSet(now, "v2026")}, domain.ErrInsufficientData)
		})
	}
}

func TestServiceClassifiesCNBaselinesAsNational(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.RegionCode = "CN"
	for index := range projection.Zones {
		projection.Zones[index].AdminCodes = []string{"CN"}
	}
	mustRebindLossProjection(t, &projection)
	set := approvedBaselineSet(now, "v2026")
	for index := range set.Population {
		set.Population[index].RegionCode = "CN"
	}
	for index := range set.Roads {
		set.Roads[index].RegionCode = "CN"
	}
	for index := range set.Costs {
		set.Costs[index].RegionCode = "CN"
	}
	for index := range set.Vulnerabilities {
		set.Vulnerabilities[index].CalibrationRegion = "CN"
	}
	value := estimateFixture(t, now, projection, set)
	for _, cost := range value.Evidence.Costs {
		if cost.BaselineLevel != lossdomain.BaselineNational {
			t.Fatalf("CN 成本基线等级 = %s", cost.BaselineLevel)
		}
	}
	for _, vulnerability := range value.Evidence.Vulnerabilities {
		if vulnerability.BaselineLevel != lossdomain.BaselineNational {
			t.Fatalf("CN 脆弱性基线等级 = %s", vulnerability.BaselineLevel)
		}
	}
}

func TestServicePublishesProjectionLimitationsAndCapsConfidence(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.ProjectionLimitations = []string{"跳过非闭合设施 way 42", "道路查询结果不含私有道路", "跳过非闭合设施 way 42"}
	mustRebindLossProjection(t, &projection)
	value := estimateFixture(t, now, projection, approvedBaselineSet(now, "v2026"))
	want := []string{"跳过非闭合设施 way 42", "道路查询结果不含私有道路"}
	if !reflect.DeepEqual(value.Evidence.SpatialAnalysis.ProjectionLimitations, want) {
		t.Fatalf("投影限制证据 = %v", value.Evidence.SpatialAnalysis.ProjectionLimitations)
	}
	for _, limitation := range want {
		if !contains(value.Limitations, limitation) {
			t.Fatalf("评估未公开投影限制 %q: %v", limitation, value.Limitations)
		}
	}
	if value.Confidence != maxLimitedProjectionConfidence || value.ConfidenceBand != "moderate" {
		t.Fatalf("有限投影置信度 = %v/%s", value.Confidence, value.ConfidenceBand)
	}
}

func TestServiceReturnsReferenceOnlyRoadRange(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	value := estimateFixture(t, now, validLossProjection(now), referenceBaselineFixture(now))
	if value.Status != lossdomain.AssessmentReferenceOnly || value.Confidence != maxReferenceConfidence ||
		value.ConfidenceBand != "low" {
		t.Fatalf("研究参考评估状态错误: %s %.2f %s", value.Status, value.Confidence, value.ConfidenceBand)
	}
	if value.ConditionalLowCents != 459_690 || value.ConditionalMidCents != 546_540 ||
		value.ConditionalHighCents != 633_390 {
		t.Fatalf("道路研究参考区间 = %d/%d/%d", value.ConditionalLowCents,
			value.ConditionalMidCents, value.ConditionalHighCents)
	}
	if !reflect.DeepEqual(value.IncludedAssets, []lossdomain.AssetType{lossdomain.AssetRoad}) ||
		value.AffectedRoadMeters != 15 || value.AffectedFacilities != 2 || value.AffectedPopulation != 70 {
		t.Fatalf("研究参考资产或暴露背景错误: %+v", value)
	}
	assertReferenceEvidence(t, value)
}

func TestServiceReferenceOnlyRequiresRoadExposure(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	projection := validLossProjection(now)
	features := make([]LossExposureFeature, 0, len(projection.Analysis.Features))
	for _, feature := range projection.Analysis.Features {
		if feature.Kind != LossFeatureRoad {
			features = append(features, feature)
		}
	}
	projection.Analysis.Features = features
	mustRebindLossProjection(t, &projection)
	assertEstimateError(t, now, projection, &baselineReaderStub{set: referenceBaselineFixture(now)}, domain.ErrInsufficientData)
}

func assertReferenceEvidence(t *testing.T, value lossdomain.Assessment) {
	t.Helper()
	if len(value.Evidence.Costs) != 1 || value.Evidence.Costs[0].Status != lossdomain.BaselineDemoOnly ||
		value.Evidence.Costs[0].BaselineLevel != lossdomain.BaselineReferenceCase {
		t.Fatalf("研究参考成本证据错误: %+v", value.Evidence.Costs)
	}
	for _, vulnerability := range value.Evidence.Vulnerabilities {
		if vulnerability.Status != lossdomain.BaselineDemoOnly ||
			vulnerability.BaselineLevel != lossdomain.BaselineReferenceCase {
			t.Fatalf("研究参考脆弱性证据错误: %+v", vulnerability)
		}
	}
	for _, limitation := range []string{lossdomain.LimitationReferenceOnly,
		lossdomain.LimitationReferenceRoadOnly, lossdomain.LimitationReferenceTransfer} {
		if !contains(value.Limitations, limitation) {
			t.Fatalf("研究参考评估缺少限制 %q: %v", limitation, value.Limitations)
		}
	}
}

func TestServiceCanonicalizesProjectionTimesAndUsesLatestAuthorityTime(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 999, time.UTC)
	projection := validLossProjection(now)
	projection.Analysis.ProjectionCollectedAt = now.Add(-5*time.Minute + 777*time.Nanosecond)
	projection.Analysis.ProjectionValidFrom = now.Add(-time.Hour + 333*time.Nanosecond)
	projection.Analysis.ProjectionValidTo = now.Add(time.Hour + 555*time.Nanosecond)
	mustRebindLossProjection(t, &projection)
	value := estimateFixture(t, now, projection, approvedBaselineSet(now, "v2026"))
	if value.CalculatedAt.Before(projection.Analysis.ProjectionCollectedAt) ||
		value.CalculatedAt.Nanosecond()%1_000 != 0 || projection.Analysis.ProjectionCollectedAt.Nanosecond()%1_000 != 0 {
		t.Fatalf("时间未规范或存在因果倒置: assessment=%s projection=%s",
			value.CalculatedAt, projection.Analysis.ProjectionCollectedAt)
	}
}

func TestServiceCalculationTimeCoversEveryProvenanceAuthorityField(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*provenance.Provenance, time.Time)
	}{
		{"validFrom", func(value *provenance.Provenance, candidate time.Time) { value.ValidFrom = candidate }},
		{"observedAt", func(value *provenance.Provenance, candidate time.Time) { value.ObservedAt = candidate }},
		{"publishedAt", func(value *provenance.Provenance, candidate time.Time) { value.PublishedAt = candidate }},
		{"revisionFirstSeenAt", func(value *provenance.Provenance, candidate time.Time) { value.RevisionFirstSeenAt = candidate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := approvedBaselineSet(now, "v2026")
			candidate := now.Add(-10 * time.Minute)
			mutateBaselineSources(&set, func(value *provenance.Provenance) { test.mutate(value, candidate) })
			assessment := estimateFixture(t, now, validLossProjection(now), set)
			if !assessment.CalculatedAt.Equal(candidate) {
				t.Fatalf("calculatedAt=%s want=%s", assessment.CalculatedAt, candidate)
			}
		})
	}
}

func TestServiceRejectsFutureProvenanceAuthorityTime(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	set := approvedBaselineSet(now, "v2026")
	mutateBaselineSources(&set, func(value *provenance.Provenance) {
		value.PublishedAt = now.Add(time.Microsecond)
	})
	assertEstimateError(t, now, validLossProjection(now), &baselineReaderStub{set: set}, domain.ErrInsufficientData)
}

func TestServiceRejectsProjectionWindowOutsideSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*LossInputProjection){
		"validFrom提前": func(value *LossInputProjection) {
			value.Analysis.ProjectionValidFrom = value.Snapshot.ValidFrom.Add(-time.Microsecond)
		},
		"validTo延后": func(value *LossInputProjection) {
			value.Analysis.ProjectionValidTo = value.Snapshot.ValidTo.Add(time.Microsecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := validLossProjection(now)
			mutate(&projection)
			mustRebindLossProjection(t, &projection)
			assertEstimateError(t, now, projection,
				&baselineReaderStub{set: approvedBaselineSet(now, "v2026")}, domain.ErrInsufficientData)
		})
	}
}

func TestServiceFailsClosedForUntrustedInputs(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*LossInputProjection, *lossdomain.BaselineSet)
	}{
		{"快照来源过期", func(value *LossInputProjection, _ *lossdomain.BaselineSet) { value.Snapshot.Source.ValidTo = now }},
		{"空间摘要损坏", func(value *LossInputProjection, _ *lossdomain.BaselineSet) { value.Analysis.Digest = "bad" }},
		{"空间统计错绑", func(value *LossInputProjection, _ *lossdomain.BaselineSet) { value.Stats.AnalysisID = "other" }},
		{"演示成本基线", func(_ *LossInputProjection, set *lossdomain.BaselineSet) {
			set.Costs[0].Status, set.Costs[0].ApprovedBy = lossdomain.BaselineDemoOnly, ""
		}},
		{"成本单位错配", func(_ *LossInputProjection, set *lossdomain.BaselineSet) { set.Costs[0].Unit = "kilometers" }},
		{"基线版本错配", func(_ *LossInputProjection, set *lossdomain.BaselineSet) {
			set.Vulnerabilities[0].Source.DatasetVersion = "other"
		}},
		{"快照模型缺失", func(value *LossInputProjection, _ *lossdomain.BaselineSet) { value.Snapshot.ModelName = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, set := validLossProjection(now), approvedBaselineSet(now, "v2026")
			test.mutate(&projection, &set)
			assertEstimateError(t, now, projection, &baselineReaderStub{set: set}, domain.ErrInsufficientData)
		})
	}
}

func TestServicePreservesPureNotFoundClassification(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	service := mustService(t, inputReaderStub{err: domain.ErrNotFound}, &baselineReaderStub{}, now)
	_, err := service.Estimate(context.Background(), EstimateInput{SnapshotID: "missing"})
	if !errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("纯 not found 分类被污染: %v", err)
	}
}

func TestDamageCentsUsesExactDecimalArithmetic(t *testing.T) {
	exposure := lossdomain.Exposure{Quantity: 1, CoverageRatio: 1}
	values := []int64{1<<53 - 1, 1 << 53, 1<<53 + 1, math.MaxInt64}
	for _, value := range values {
		got, err := damageCents(exposure, value, 1, 1)
		if err != nil || got != value {
			t.Fatalf("damageCents(%d) = %d, %v", value, got, err)
		}
	}
	got, err := damageCents(exposure, 1, 0.5, 1)
	if err != nil || got != 1 {
		t.Fatalf("半分舍入 = %d, %v", got, err)
	}
	exposure.Quantity = 2
	if _, err = damageCents(exposure, math.MaxInt64, 1, 1); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("近 MaxInt64 溢出未 fail-closed: %v", err)
	}
}

func estimateFixture(t *testing.T, now time.Time, projection LossInputProjection, set lossdomain.BaselineSet) lossdomain.Assessment {
	t.Helper()
	baselines := &baselineReaderStub{set: set}
	service := mustService(t, inputReaderStub{value: projection}, baselines, now)
	value, err := service.Estimate(context.Background(), EstimateInput{SnapshotID: projection.Snapshot.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines.queries) != 1 || baselines.queries[0].RegionCode != projection.Analysis.RegionCode ||
		baselines.queries[0].HazardType != "landslide" {
		t.Fatalf("基线查询未使用服务端派生条件: %+v", baselines.queries)
	}
	query := baselines.queries[0]
	if !query.At.Equal(now.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("基线查询未复用服务时钟: %v", query.At)
	}
	wantRequirements, err := deriveBaselineRequirements(value.Evidence.Exposures)
	if err != nil || !reflect.DeepEqual(query.Requirements, wantRequirements) {
		t.Fatalf("基线查询未绑定实际资产、单位和强度: got=%+v want=%+v err=%v",
			query.Requirements, wantRequirements, err)
	}
	return value
}

func assertEstimateError(t *testing.T, now time.Time, projection LossInputProjection, baselines *baselineReaderStub, wanted error) {
	t.Helper()
	service := mustService(t, inputReaderStub{value: projection}, baselines, now)
	_, err := service.Estimate(context.Background(), EstimateInput{SnapshotID: projection.Snapshot.ID})
	if !errors.Is(err, wanted) {
		t.Fatalf("Estimate() error = %v, want %v", err, wanted)
	}
}

func mustService(t *testing.T, reader LossInputProjectionReader, baselines BaselineSetReader, now time.Time) *Service {
	t.Helper()
	service, err := NewService(reader, baselines, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertSelectedBaselineLevels(t *testing.T, value lossdomain.Assessment) {
	t.Helper()
	if len(value.Evidence.Costs) != 2 || len(value.Evidence.Vulnerabilities) != 2 {
		t.Fatalf("基线证据数量异常: %+v", value.Evidence)
	}
	levels := map[lossdomain.AssetType]lossdomain.BaselineLevel{}
	for _, cost := range value.Evidence.Costs {
		if !cost.Provided || cost.Status != lossdomain.BaselineApproved {
			t.Fatalf("成本基线可用性未显式绑定: %+v", cost)
		}
		levels[cost.AssetType] = cost.BaselineLevel
	}
	if levels[lossdomain.AssetFacility] != lossdomain.BaselineNational || levels[lossdomain.AssetRoad] != lossdomain.BaselineNational {
		t.Fatalf("成本基线级别异常: %v", levels)
	}
}

func validLossProjection(now time.Time) LossInputProjection {
	snapshot := lossSnapshot(now)
	zones := []LossRiskZone{
		{ID: "zone-1", SnapshotID: snapshot.ID, Level: hazarddomain.RiskLow, AreaSquareM: 100, AreaCalculated: true, AdminCodes: []string{"CN"}},
		{ID: "zone-2", SnapshotID: snapshot.ID, Level: hazarddomain.RiskVeryHigh, AreaSquareM: 100, AreaCalculated: true, AdminCodes: []string{"CN"}},
	}
	analysis := LossSpatialProjection{ID: "analysis-loss-1", Version: "spatial-v2", Digest: strings.Repeat("b", 64),
		ProjectionCollectedAt: now.Add(-30 * time.Minute), ProjectionValidFrom: now.Add(-time.Hour),
		ProjectionValidTo: now.Add(12 * time.Hour), AdminBoundaryID: "CHN-ADM0-geoboundaries-v6",
		AdminBoundaryDigest: strings.Repeat("c", 64), AdminBoundaryReference: "https://example.test/boundary/chn",
		SnapshotID: snapshot.ID, Status: spatialdomain.AnalysisAvailable, RegionCode: "CN", TotalAreaSquareMeters: 150,
		CalculatedAt: snapshot.RunAt.Add(30 * time.Minute), InputReferences: []string{"https://example.test/spatial/input"},
		DatasetReferences: []string{"https://example.test/spatial/dataset"}, Features: lossFeatures()}
	stats := RiskProjectionStats{ZoneCount: 2, MaxGeometryPoints: 5, MaxGeometryBytes: 100,
		TotalGeometryPoints: 10, TotalGeometryBytes: 200, SpatialJSONBytes: 400,
		FeatureCount: len(analysis.Features), ProjectionBytes: 4096,
		AnalysisID: analysis.ID, AnalysisDigest: analysis.Digest}
	result := LossInputProjection{Snapshot: snapshot, Zones: zones, Analysis: analysis, Stats: stats}
	result.Stats.ReferenceCount = projectionReferenceCount(result)
	result.Stats.UniqueReferenceCount = projectionUniqueReferenceCount(result)
	if err := BindRiskProjectionIdentity(&result); err != nil {
		panic(err)
	}
	return result
}

func projectionWithFeatureCount(now time.Time, count int) LossInputProjection {
	value := validLossProjection(now)
	for index := len(value.Analysis.Features); index < count; index++ {
		value.Analysis.Features = append(value.Analysis.Features, LossExposureFeature{
			FeatureID: fmt.Sprintf("road-zz-%04d", index), Kind: LossFeatureRoad, ZoneIDs: []string{"zone-2"},
			Quantity: 0, Unit: "meters", CoverageRatio: 1, Status: spatialdomain.MetricAvailable,
			Provided: true, InputReferences: []string{"https://example.test/road/bulk"}})
	}
	value.Stats.FeatureCount = len(value.Analysis.Features)
	value.Stats.ReferenceCount = projectionReferenceCount(value)
	value.Stats.UniqueReferenceCount = projectionUniqueReferenceCount(value)
	value.Stats.ProjectionBytes = maxLossProjectionBytes - 1
	if err := BindRiskProjectionIdentity(&value); err != nil {
		panic(err)
	}
	return value
}

func mustRebindLossProjection(t *testing.T, value *LossInputProjection) {
	t.Helper()
	if err := BindRiskProjectionIdentity(value); err != nil {
		t.Fatal(err)
	}
}

func lossFeatures() []LossExposureFeature {
	available := spatialdomain.MetricAvailable
	return []LossExposureFeature{
		{FeatureID: "facility-shared", Kind: LossFeatureFacility, ZoneIDs: []string{"zone-1", "zone-2"}, Quantity: 2, Unit: "count", CoverageRatio: 1, Status: available, Provided: true, InputReferences: []string{"https://example.test/facility/shared"}},
		{FeatureID: "population-shared", Kind: LossFeaturePopulation, ZoneIDs: []string{"zone-1", "zone-2"}, Quantity: 50, Unit: "people", CoverageRatio: 1, Status: available, Provided: true, InputReferences: []string{"https://example.test/population/shared"}},
		{FeatureID: "population-unique", Kind: LossFeaturePopulation, ZoneIDs: []string{"zone-2"}, Quantity: 20, Unit: "people", CoverageRatio: 1, Status: available, Provided: true, InputReferences: []string{"https://example.test/population/unique"}},
		{FeatureID: "road-shared", Kind: LossFeatureRoad, ZoneIDs: []string{"zone-1", "zone-2"}, Quantity: 10, Unit: "meters", CoverageRatio: 1, Status: available, Provided: true, InputReferences: []string{"https://example.test/road/shared"}},
		{FeatureID: "road-unique", Kind: LossFeatureRoad, ZoneIDs: []string{"zone-2"}, Quantity: 5, Unit: "meters", CoverageRatio: 1, Status: available, Provided: true, InputReferences: []string{"https://example.test/road/unique"}},
	}
}

func approvedBaselineSet(now time.Time, version string) lossdomain.BaselineSet {
	source := baselineSource(now, version)
	return lossdomain.BaselineSet{Version: version,
		Population: []lossdomain.ExposureBaseline{{ID: "population-cn", RegionCode: "CN", Kind: lossdomain.ExposurePopulation,
			Quantity: 1, Unit: "people", DataYear: 2026, CoverageRatio: 1, Source: source}},
		Roads: []lossdomain.ExposureBaseline{{ID: "road-cn", RegionCode: "CN", Kind: lossdomain.ExposureRoad,
			Quantity: 1, Unit: "meters", DataYear: 2026, CoverageRatio: 1, Source: source}},
		Costs: []lossdomain.CostBaseline{
			approvedCost(lossdomain.AssetFacility, "CN", "count", 1000, 2000, 3000, now, source),
			approvedCost(lossdomain.AssetRoad, "CN", "meters", 100, 200, 300, now, source)},
		Vulnerabilities: []lossdomain.Vulnerability{
			approvedVulnerability(lossdomain.AssetFacility, "CN", source),
			approvedVulnerability(lossdomain.AssetRoad, "CN", source)}}
}

func referenceBaselineFixture(now time.Time) lossdomain.BaselineSet {
	const version = "road-reference-v1"
	source := baselineSource(now, version)
	source.QualityFlags = []string{"demo_only", "research_reference"}
	return lossdomain.BaselineSet{Version: version,
		Population: []lossdomain.ExposureBaseline{{ID: "reference-population", RegionCode: "CN-54",
			Kind: lossdomain.ExposurePopulation, Quantity: 0, Unit: "people", DataYear: 2024,
			CoverageRatio: 1, Source: source}},
		Roads: []lossdomain.ExposureBaseline{{ID: "reference-road", RegionCode: "CN-54",
			Kind: lossdomain.ExposureRoad, Quantity: 0, Unit: "meters", DataYear: 2024,
			CoverageRatio: 1, Source: source}},
		Costs: []lossdomain.CostBaseline{{ID: "reference-road-cost", AssetType: lossdomain.AssetRoad,
			RegionCode: "CN-54", Unit: "meters", LowCents: 30_646, CentralCents: 36_436,
			HighCents: 42_226, Currency: "CNY", PriceBaseDate: now.Add(-365 * 24 * time.Hour),
			Status: lossdomain.BaselineDemoOnly, Source: source}},
		Vulnerabilities: []lossdomain.Vulnerability{{ID: "reference-road-vulnerability",
			AssetType: lossdomain.AssetRoad, HazardType: string(hazarddomain.TypeLandslide),
			IntensityBand: string(hazarddomain.RiskVeryHigh), ImpactFractionLow: 1,
			ImpactFractionMid: 1, ImpactFractionHigh: 1, DamageRatioLow: 1,
			DamageRatioMid: 1, DamageRatioHigh: 1, CalibrationRegion: "CN-54",
			Status: lossdomain.BaselineDemoOnly, Source: source}}}
}

func approvedCost(asset lossdomain.AssetType, region, unit string, low, mid, high int64, now time.Time,
	source provenance.Provenance) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{ID: "cost-" + string(asset), AssetType: asset, RegionCode: region, Unit: unit,
		LowCents: low, CentralCents: mid, HighCents: high, Currency: "CNY", PriceBaseDate: now.Add(-30 * 24 * time.Hour),
		Status: lossdomain.BaselineApproved, ApprovedBy: "国家地质灾害监控中心", Source: source}
}

func approvedVulnerability(asset lossdomain.AssetType, region string, source provenance.Provenance) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{ID: "vulnerability-" + string(asset), AssetType: asset,
		HazardType: string(hazarddomain.TypeLandslide), IntensityBand: string(hazarddomain.RiskVeryHigh),
		ImpactFractionLow: 1, ImpactFractionMid: 1, ImpactFractionHigh: 1,
		DamageRatioLow: 1, DamageRatioMid: 1, DamageRatioHigh: 1, CalibrationRegion: region,
		Status: lossdomain.BaselineApproved, ApprovedBy: "国家地质灾害监控中心", Source: source}
}

func baselineSource(now time.Time, version string) provenance.Provenance {
	return provenance.Provenance{Provider: "authority", Dataset: "loss-baseline", DatasetVersion: version,
		SourceRevision: "revision-1", SourceURI: "https://example.test/baseline/" + version,
		Citation: "已审核损失基线", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline,
		FetchedAt: now.Add(-24 * time.Hour), ValidFrom: now.Add(-30 * 24 * time.Hour),
		ValidTo: now.Add(365 * 24 * time.Hour), SHA256: strings.Repeat("a", 64),
		TransformVersion: "baseline-import-v1", QualityFlags: []string{"approved"}}
}

func mutateBaselineSources(value *lossdomain.BaselineSet, mutate func(*provenance.Provenance)) {
	for index := range value.Population {
		mutate(&value.Population[index].Source)
	}
	for index := range value.Roads {
		mutate(&value.Roads[index].Source)
	}
	for index := range value.Costs {
		mutate(&value.Costs[index].Source)
	}
	for index := range value.Vulnerabilities {
		mutate(&value.Vulnerabilities[index].Source)
	}
}

func lossSnapshot(now time.Time) hazarddomain.Snapshot {
	source := provenance.Provenance{Provider: "NASA", Dataset: "LHASA", DatasetVersion: "2.1.1",
		SourceRevision: "revision-1", SourceURI: "https://example.test/lhasa.tif", Citation: "NASA LHASA",
		License: "NASA Open Data", DataKind: provenance.DataKindNowcast, FetchedAt: now.Add(-2 * time.Hour),
		ValidFrom: now.Add(-3 * time.Hour), ValidTo: now.Add(24 * time.Hour), TransformVersion: "gdal-v1",
		QualityFlags: []string{"checked"}}
	return hazarddomain.Snapshot{ID: "snapshot-loss-1", HazardType: hazarddomain.TypeLandslide,
		ModelName: "LHASA", ModelVersion: "2.1.1", RunAt: now.Add(-time.Hour), ValidFrom: now.Add(-2 * time.Hour),
		ValidTo: now.Add(12 * time.Hour), RasterReference: source.SourceURI, ProbabilitySemantics: "模型概率",
		Thresholds: []hazarddomain.RiskThreshold{{Level: hazarddomain.RiskLow, Minimum: 0, Maximum: 1}},
		Status:     hazarddomain.SnapshotAvailable, Source: source, Limitations: []string{"辅助研判"}}
}
