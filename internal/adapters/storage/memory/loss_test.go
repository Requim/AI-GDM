package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestLossAssessmentStoreRoundTripsIndependentCopy(t *testing.T) {
	store := NewLossAssessmentStore()
	value := validAssessment(t)
	wantID, wantReference := value.ID, value.InputReferences[0]
	if err := store.SaveAssessment(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.InputReferences[0] = "https://mutated.test"
	value.ExpectedLowCents = ptrInt64(999)
	got, err := store.GetAssessment(context.Background(), wantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputReferences[0] != wantReference || got.ExpectedLowCents == nil || *got.ExpectedLowCents != 10 {
		t.Fatalf("仓储返回了共享引用: %+v", got)
	}
	got.InputReferences[0] = "https://changed.test"
	again, err := store.GetAssessment(context.Background(), wantID)
	if err != nil || again.InputReferences[0] != wantReference {
		t.Fatalf("读取副本未隔离: value=%+v err=%v", again, err)
	}
}

func TestLossAssessmentStoreRejectsInvalidAndMissingValues(t *testing.T) {
	store := NewLossAssessmentStore()
	if err := store.SaveAssessment(context.Background(), lossdomain.Assessment{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法评估错误 = %v", err)
	}
	if _, err := store.GetAssessment(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺失评估错误 = %v", err)
	}
	if _, err := store.GetAssessment(context.Background(), "bad id"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法标识错误 = %v", err)
	}
}

func TestLossAssessmentStoreRejectsConflictingSameID(t *testing.T) {
	store := NewLossAssessmentStore()
	value := validAssessment(t)
	if err := store.SaveAssessment(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.ConditionalHighCents++
	if err := store.SaveAssessment(context.Background(), value); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同标识不同内容未被拒绝: %v", err)
	}
}

func TestLossAssessmentStoreHonorsContextCancellation(t *testing.T) {
	store := NewLossAssessmentStore()
	value := validAssessment(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.SaveAssessment(ctx, value); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消保存错误 = %v", err)
	}
	if _, err := store.GetAssessment(ctx, value.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消读取错误 = %v", err)
	}
}

func validAssessment(t *testing.T) lossdomain.Assessment {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validAssessmentEvidence(now)
	value := lossdomain.Assessment{
		SnapshotID: evidence.Snapshot.ID, FormulaVersion: lossdomain.FormulaVersion,
		ScenarioMethod: "确定性公式", HazardType: evidence.Snapshot.HazardType, RegionCode: "CN",
		ConditionalLowCents: 10, ConditionalMidCents: 20, ConditionalHighCents: 30,
		ExpectedLowCents: ptrInt64(10), ExpectedMidCents: ptrInt64(20), ExpectedHighCents: ptrInt64(30),
		ImpactAreaSquareM: 100, AffectedPopulation: 10, AffectedRoadMeters: 5, AffectedFacilities: 1,
		InputReferences: lossdomain.EvidenceReferences(evidence),
		IncludedAssets:  []lossdomain.AssetType{lossdomain.AssetFacility, lossdomain.AssetRoad},
		ExcludedLosses:  []string{"建筑物损失未纳入"}, Status: lossdomain.AssessmentAvailable,
		Confidence: 1, ConfidenceBand: "high", Limitations: []string{"辅助研判"}, CalculatedAt: now, Evidence: evidence,
	}
	bound, err := lossdomain.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func validAssessmentEvidence(now time.Time) lossdomain.AssessmentEvidence {
	baseline := validBaselineSource(now)
	dynamic := baseline
	dynamic.Provider, dynamic.Dataset, dynamic.DataKind = "NASA", "LHASA", provenance.DataKindNowcast
	dynamic.SourceURI, dynamic.FetchedAt = "https://example.test/lhasa", now.Add(-time.Hour)
	dynamic.ValidFrom, dynamic.ValidTo = now.Add(-2*time.Hour), now.Add(12*time.Hour)
	return lossdomain.AssessmentEvidence{
		Version: lossdomain.EvidenceVersion,
		Snapshot: lossdomain.SnapshotEvidence{ID: "snapshot-1", HazardType: "landslide", ModelName: "LHASA",
			ModelVersion: "2.1.1", Status: "available", RunAt: now.Add(-time.Hour),
			ValidFrom: now.Add(-2 * time.Hour), ValidTo: now.Add(12 * time.Hour), Source: dynamic},
		SpatialAnalysis: validSpatialEvidence(now), BaselineSet: lossdomain.BaselineSetEvidence{
			Provider: baseline.Provider, Dataset: baseline.Dataset, Version: baseline.DatasetVersion},
		IntensityBand: "high", RiskZones: []lossdomain.RiskZoneEvidence{{
			ID: "zone-1", Level: "high", AreaSquareMeters: 100, AdminCodes: []string{"CN"}}},
		Population: []lossdomain.PopulationEvidence{{FeatureID: "population-1", ZoneID: "zone-1",
			ZoneIDs: []string{"zone-1"}, Quantity: 10, Unit: "people", CoverageRatio: 1,
			Provided: true, MetricStatus: "available", InputReferences: []string{"population://zone-1"}}},
		Exposures: validExposureEvidence(), Costs: []lossdomain.CostBaseline{
			validCost(lossdomain.AssetFacility, "count", baseline), validCost(lossdomain.AssetRoad, "meters", baseline)},
		Vulnerabilities: []lossdomain.Vulnerability{
			validVulnerability(lossdomain.AssetFacility, baseline), validVulnerability(lossdomain.AssetRoad, baseline)},
	}
}

func validSpatialEvidence(now time.Time) lossdomain.SpatialAnalysisEvidence {
	return lossdomain.SpatialAnalysisEvidence{
		ID: "analysis-1", Version: "analysis-v1", Digest: strings.Repeat("b", 64),
		ProjectionID: "exposure-" + strings.Repeat("c", 64), ProjectionVersion: lossdomain.RiskProjectionVersion,
		ProjectionDigest: strings.Repeat("c", 64), ProjectionCollectedAt: now.Add(-20 * time.Minute),
		ProjectionValidFrom: now.Add(-time.Hour), ProjectionValidTo: now.Add(time.Hour),
		SourceReferenceDigests: []string{strings.Repeat("d", 64)}, ProjectionLimitations: []string{},
		AdminBoundaryID: "CHN-ADM0-geoboundaries-v6", AdminBoundaryDigest: strings.Repeat("e", 64),
		Status: "available", RegionCode: "CN", TotalAreaSquareM: 100,
		CalculatedAt: now.Add(-30 * time.Minute), InputReferences: []string{"analysis://input"},
		DatasetReferences: []string{"analysis://dataset"},
	}
}

func validExposureEvidence() []lossdomain.Exposure {
	return []lossdomain.Exposure{
		{FeatureID: "facility-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"},
			AssetType: lossdomain.AssetFacility, Quantity: 1, Unit: "count", CoverageRatio: 1,
			Provided: true, MetricStatus: "available", IntensityBand: "high", AnalysisID: "analysis-1",
			AnalysisVersion: "analysis-v1", InputReferences: []string{"poi://zone-1"}},
		{FeatureID: "road-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"},
			AssetType: lossdomain.AssetRoad, Quantity: 5, Unit: "meters", CoverageRatio: 1,
			Provided: true, MetricStatus: "available", IntensityBand: "high", AnalysisID: "analysis-1",
			AnalysisVersion: "analysis-v1", InputReferences: []string{"road://zone-1"}},
	}
}

func validBaselineSource(now time.Time) provenance.Provenance {
	return provenance.Provenance{
		Provider: "test", Dataset: "loss-baseline", DatasetVersion: "v1", SourceRevision: "revision-1",
		SourceURI: "https://example.test/baseline", Citation: "测试基线引用", License: "CC-BY-4.0",
		SHA256: strings.Repeat("a", 64), TransformVersion: "fixture-v1", QualityFlags: []string{"test_fixture"},
		DataKind: provenance.DataKindBaseline, FetchedAt: now.Add(-24 * time.Hour),
		ValidFrom: now.Add(-24 * time.Hour), ValidTo: now.Add(365 * 24 * time.Hour),
	}
}

func validCost(asset lossdomain.AssetType, unit string, source provenance.Provenance) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{
		ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: unit,
		LowCents: 10, CentralCents: 20, HighCents: 30, Currency: "CNY", PriceBaseDate: source.ValidFrom,
		Status: lossdomain.BaselineApproved, Provided: true, BaselineLevel: lossdomain.BaselineNational,
		ApprovedBy: "reviewer", Source: source,
	}
}

func validVulnerability(asset lossdomain.AssetType, source provenance.Provenance) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{
		ID: "vulnerability-" + string(asset), AssetType: asset, HazardType: "landslide", IntensityBand: "high",
		ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
		DamageRatioLow: 0.1, DamageRatioMid: 0.2, DamageRatioHigh: 0.3, CalibrationRegion: "CN",
		Status: lossdomain.BaselineApproved, Provided: true, BaselineLevel: lossdomain.BaselineNational,
		ApprovedBy: "reviewer", Source: source,
	}
}

func ptrInt64(value int64) *int64 { return &value }
