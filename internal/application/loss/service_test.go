package loss

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatial"
	analysisdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

var _ ports.Clock = fixedClock{}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type riskReaderStub struct {
	snapshot hazarddomain.Snapshot
	zones    []hazarddomain.RiskZone
	err      error
}

func (r riskReaderStub) RiskDetail(context.Context, string) (hazarddomain.Snapshot, []hazarddomain.RiskZone, error) {
	return r.snapshot, r.zones, r.err
}

type analysisReaderStub struct {
	value analysisdomain.Analysis
	err   error
}

func (r analysisReaderStub) Get(context.Context, string) (analysisdomain.Analysis, error) {
	return r.value, r.err
}

func (r analysisReaderStub) LatestBySnapshot(context.Context, string) (analysisdomain.Analysis, error) {
	return r.value, r.err
}

type baselineReaderStub struct {
	costs           []lossdomain.CostBaseline
	vulnerabilities []lossdomain.Vulnerability
	err             error
}

func (r baselineReaderStub) CostBaselines(context.Context, string) ([]lossdomain.CostBaseline, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.costs, nil
}

func (r baselineReaderStub) Vulnerabilities(context.Context, string) ([]lossdomain.Vulnerability, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.vulnerabilities, nil
}

func (r baselineReaderStub) ExposureBaselines(context.Context, string, lossdomain.ExposureKind) ([]lossdomain.ExposureBaseline, error) {
	if r.err != nil {
		return nil, r.err
	}
	return nil, domain.ErrNotFound
}

func TestServiceEstimateCalculatesScenariosAndIsDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	snapshot, zones := lossRiskFixture(now, hazarddomain.SnapshotAvailable)
	analysis := lossAnalysisFixture(t, snapshot.ID)
	service, err := NewService(riskReaderStub{snapshot: snapshot, zones: zones}, analysisReaderStub{value: analysis}, lossBaselineReaderStub(), fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	input := EstimateInput{SnapshotID: snapshot.ID, RegionCode: "CN", HazardType: hazarddomain.TypeLandslide, IntensityBand: "high", Exposures: []lossdomain.Exposure{lossExposure(zones[0].ID, lossdomain.AssetBuilding, 100, 1, now)}}
	first, err := service.Estimate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Estimate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复计算结果不稳定: first=%+v second=%+v", first, second)
	}
	if first.ConditionalLowCents != 20000 || first.ConditionalMidCents != 240000 || first.ConditionalHighCents != 900000 {
		t.Fatalf("情景金额 = %d/%d/%d，结果不符合公式", first.ConditionalLowCents, first.ConditionalMidCents, first.ConditionalHighCents)
	}
	if first.FormulaVersion != lossdomain.FormulaVersion || first.Status != lossdomain.AssessmentAvailable || first.ConfidenceBand != "low" {
		t.Fatalf("评估元数据不完整: %+v", first)
	}
	if len(first.InputReferences) < 3 || first.ImpactAreaSquareM != 100 {
		t.Fatalf("输入依据或影响面积异常: %+v", first)
	}
}

func TestServiceEstimateMatchesVulnerabilityPerAsset(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	snapshot, zones := lossRiskFixture(now, hazarddomain.SnapshotAvailable)
	analysis := lossAnalysisFixture(t, snapshot.ID)
	base := lossBaselineReaderStub()
	base.costs = append(base.costs, costBaseline(lossdomain.AssetRoad, 5000, 8000, 12000, now))
	base.vulnerabilities = append(base.vulnerabilities, vulnerability(lossdomain.AssetRoad, now))
	service, err := NewService(riskReaderStub{snapshot: snapshot, zones: zones}, analysisReaderStub{value: analysis}, base, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	input := EstimateInput{SnapshotID: snapshot.ID, RegionCode: "CN", HazardType: hazarddomain.TypeLandslide, IntensityBand: "high", Exposures: []lossdomain.Exposure{
		lossExposure(zones[0].ID, lossdomain.AssetBuilding, 100, 1, now), lossExposure(zones[0].ID, lossdomain.AssetRoad, 50, 0.5, now),
	}}
	assessment, err := service.Estimate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.ConditionalLowCents != 30000 || assessment.ConditionalMidCents != 336000 || assessment.ConditionalHighCents != 1260000 {
		t.Fatalf("多资产情景金额 = %d/%d/%d", assessment.ConditionalLowCents, assessment.ConditionalMidCents, assessment.ConditionalHighCents)
	}
	if len(assessment.IncludedAssets) != 2 {
		t.Fatalf("IncludedAssets = %+v", assessment.IncludedAssets)
	}
}

func TestServiceEstimateRejectsMissingInputsWithErrorChain(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	snapshot, zones := lossRiskFixture(now, hazarddomain.SnapshotAvailable)
	analysis := lossAnalysisFixture(t, snapshot.ID)
	service, err := NewService(riskReaderStub{snapshot: snapshot, zones: zones}, analysisReaderStub{value: analysis}, baselineReaderStub{err: domain.ErrNotFound}, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	input := EstimateInput{SnapshotID: snapshot.ID, RegionCode: "CN", HazardType: hazarddomain.TypeLandslide, IntensityBand: "high", Exposures: []lossdomain.Exposure{lossExposure(zones[0].ID, lossdomain.AssetBuilding, 100, 1, now)}}
	_, err = service.Estimate(context.Background(), input)
	if !errors.Is(err, domain.ErrInsufficientData) || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误链 = %v", err)
	}
}

func TestServiceEstimateRejectsUnknownZoneAndInvalidNumbers(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	snapshot, zones := lossRiskFixture(now, hazarddomain.SnapshotAvailable)
	analysis := lossAnalysisFixture(t, snapshot.ID)
	service, err := NewService(riskReaderStub{snapshot: snapshot, zones: zones}, analysisReaderStub{value: analysis}, lossBaselineReaderStub(), fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	input := EstimateInput{SnapshotID: snapshot.ID, RegionCode: "CN", HazardType: hazarddomain.TypeLandslide, IntensityBand: "high", Exposures: []lossdomain.Exposure{lossExposure("missing-zone", lossdomain.AssetBuilding, 100, 1, now)}}
	_, err = service.Estimate(context.Background(), input)
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("未知风险区错误 = %v", err)
	}
	input.Exposures[0].Quantity = math.NaN()
	_, err = service.Estimate(context.Background(), input)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法暴露数值错误 = %v", err)
	}
}

func TestServiceEstimateMarksStaleSnapshotInConfidence(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	snapshot, zones := lossRiskFixture(now, hazarddomain.SnapshotStale)
	analysis := lossAnalysisFixture(t, snapshot.ID)
	service, err := NewService(riskReaderStub{snapshot: snapshot, zones: zones}, analysisReaderStub{value: analysis}, lossBaselineReaderStub(), fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	input := EstimateInput{SnapshotID: snapshot.ID, RegionCode: "CN", HazardType: hazarddomain.TypeLandslide, IntensityBand: "high", Exposures: []lossdomain.Exposure{lossExposure(zones[0].ID, lossdomain.AssetBuilding, 100, 1, now)}}
	assessment, err := service.Estimate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Confidence >= 0.56 || !contains(assessment.InputReferences, "snapshot:stale") {
		t.Fatalf("过期状态未降级: %+v", assessment)
	}
}

func lossBaselineReaderStub() baselineReaderStub {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return baselineReaderStub{costs: []lossdomain.CostBaseline{costBaseline(lossdomain.AssetBuilding, 10000, 20000, 30000, now)}, vulnerabilities: []lossdomain.Vulnerability{vulnerability(lossdomain.AssetBuilding, now)}}
}

func costBaseline(asset lossdomain.AssetType, low, central, high int64, now time.Time) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: "平方米", LowCents: low, CentralCents: central, HighCents: high, Currency: "CNY", PriceBaseDate: now, Status: lossdomain.BaselineDemoOnly, Source: baselineSource(now)}
}

func vulnerability(asset lossdomain.AssetType, now time.Time) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{ID: "vulnerability-" + string(asset), AssetType: asset, HazardType: string(hazarddomain.TypeLandslide), IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.3, ImpactFractionHigh: 0.5, DamageRatioLow: 0.2, DamageRatioMid: 0.4, DamageRatioHigh: 0.6, CalibrationRegion: "CN", Status: lossdomain.BaselineDemoOnly, Source: baselineSource(now)}
}

func lossExposure(zoneID string, asset lossdomain.AssetType, quantity, coverage float64, now time.Time) lossdomain.Exposure {
	return lossdomain.Exposure{ZoneID: zoneID, AssetType: string(asset), Quantity: quantity, Unit: "平方米", DataYear: 2025, CoverageRatio: coverage, Source: provenance.Provenance{Provider: "baseline-provider", Dataset: "exposure", DatasetVersion: "v1", SourceRevision: "rev-1", SourceURI: "https://example.test/exposure/" + zoneID, Citation: "公开统计基线", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline, FetchedAt: now, ValidFrom: now.Add(-24 * time.Hour), ValidTo: now.Add(365 * 24 * time.Hour), TransformVersion: "exposure-v1", QualityFlags: []string{"versioned"}}}
}

func baselineSource(now time.Time) provenance.Provenance {
	return provenance.Provenance{Provider: "baseline-provider", Dataset: "loss-baseline", DatasetVersion: "v1", SourceRevision: "rev-1", SourceURI: "https://example.test/baseline/v1", Citation: "公开统计基线", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline, FetchedAt: now, ValidFrom: now, ValidTo: now.Add(365 * 24 * time.Hour), SHA256: strings.Repeat("a", 64), TransformVersion: "baseline-v1", QualityFlags: []string{"reviewed"}}
}

func lossRiskFixture(now time.Time, status hazarddomain.SnapshotStatus) (hazarddomain.Snapshot, []hazarddomain.RiskZone) {
	source := provenance.Provenance{Provider: "NASA", Dataset: "LHASA", DatasetVersion: "2.1.1", SourceRevision: "rev-1", SourceURI: "https://example.test/lhasa.tif", Citation: "NASA LHASA", License: "NASA Open Data", DataKind: provenance.DataKindNowcast, FetchedAt: now, ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(12 * time.Hour), TransformVersion: "gdal-v1", QualityFlags: []string{"checked"}}
	snapshot := hazarddomain.Snapshot{ID: "snapshot-loss-1", HazardType: hazarddomain.TypeLandslide, ModelName: "LHASA", ModelVersion: "2.1.1", RunAt: now.Add(-time.Minute), ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(12 * time.Hour), RasterReference: "https://example.test/lhasa.tif", ProbabilitySemantics: "模型概率", Thresholds: []hazarddomain.RiskThreshold{{Level: hazarddomain.RiskLow, Minimum: 0, Maximum: 1}}, Status: status, Source: source, Limitations: []string{"仅用于损失评估测试"}}
	geometry := json.RawMessage(`[[[116,39],[116.01,39],[116.01,39.01],[116,39]]]`)
	zone := hazarddomain.RiskZone{ID: "zone-loss-1", SnapshotID: snapshot.ID, Geometry: spatialdomain.Geometry{Type: "Polygon", Coordinates: geometry}, Minimum: 0.5, Mean: 0.6, Maximum: 0.7, Level: hazarddomain.RiskHigh, AreaSquareM: 100, AreaCalculated: true, InputReferences: []string{snapshot.RasterReference}, Limitations: []string{"仅用于损失评估测试"}}
	return snapshot, []hazarddomain.RiskZone{zone}
}

func lossAnalysisFixture(t *testing.T, snapshotID string) analysisdomain.Analysis {
	t.Helper()
	analysis, err := analysisdomain.NewAnalysis(analysisdomain.AnalysisInput{SnapshotID: snapshotID, Area: analysisdomain.AreaCalculation{Method: analysisdomain.AreaMethod, TotalSquareMeters: 100, InputReferences: []string{"geometry://zone-loss-1"}}, Zones: []analysisdomain.ZoneResult{unavailableZone("zone-loss-1", 100)}, CalculatedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), Limitations: []string{"人口、道路和 POI 数据尚未接入"}})
	if err != nil {
		t.Fatal(err)
	}
	return analysis
}

func unavailableZone(id string, area float64) analysisdomain.ZoneResult {
	return analysisdomain.ZoneResult{ZoneID: id, Area: analysisdomain.ZoneArea{SquareMeters: area, InputReferences: []string{"geometry://" + id}}, Population: analysisdomain.PopulationExposureMetric{Status: analysisdomain.MetricUnavailable, Unit: analysisdomain.PopulationUnit, Limitations: []string{"缺少人口基线"}}, Roads: analysisdomain.RoadExposureMetric{Status: analysisdomain.MetricUnavailable, Unit: analysisdomain.RoadUnit, Limitations: []string{"缺少道路基线"}}, POIs: analysisdomain.POIExposureMetric{Status: analysisdomain.MetricUnavailable, Unit: analysisdomain.POIUnit, Limitations: []string{"缺少 POI 基线"}}, Administration: analysisdomain.AdministrativeMatch{Status: analysisdomain.AdminMatchUnavailable, Limitations: []string{"缺少行政边界"}}}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
