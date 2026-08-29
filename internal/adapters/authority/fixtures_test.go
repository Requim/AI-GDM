package authority

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/storage/memory"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

type resolverFixture struct {
	resolver   *Resolver
	risk       *riskReaderStub
	spatial    *spatialReaderStub
	loss       *lossReaderStub
	catalog    applicationsurvival.CatalogService
	survival   applicationsurvival.AssessmentService
	cache      *jsonCache
	now        time.Time
	snapshot   hazard.Snapshot
	route      evacuation.Route
	survivalID string
}

func newResolverFixture(t *testing.T) resolverFixture {
	t.Helper()
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	riskResult, spatialResult := validHazardResults(t, now)
	lossResult := validLossAssessment(t, now)
	catalogStore, err := memory.NewDefaultSurvivalCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := applicationsurvival.NewCatalogService(catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	survival, err := applicationsurvival.NewAssessmentService(catalogStore, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := survival.AssessCase(context.Background(), "case-oso-2014")
	if err != nil {
		t.Fatal(err)
	}
	return assembleFixture(t, now, riskResult, spatialResult, lossResult, catalog, survival, replay.AssessmentID)
}

func assembleFixture(t *testing.T, now time.Time, riskResult ports.HazardAuthorityRead,
	spatialResult spatialanalysis.Analysis, lossResult loss.Assessment,
	catalog applicationsurvival.CatalogService, survival applicationsurvival.AssessmentService,
	survivalID string,
) resolverFixture {
	t.Helper()
	riskReader := &riskReaderStub{value: riskResult}
	spatialReader := &spatialReaderStub{value: spatialResult}
	lossReader := &lossReaderStub{value: lossResult}
	cache := newJSONCache()
	resolver, err := New(riskReader, spatialReader, lossReader, catalog, survival, cache, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	return resolverFixture{
		resolver: resolver, risk: riskReader, spatial: spatialReader, loss: lossReader,
		catalog: catalog, survival: survival, cache: cache, now: now,
		snapshot: validRouteSnapshot(now), route: validRoute(), survivalID: survivalID,
	}
}

func validHazardResults(t *testing.T, now time.Time) (ports.HazardAuthorityRead, spatialanalysis.Analysis) {
	t.Helper()
	const snapshotID = "snapshot-authority-1"
	spatialResult, err := spatialanalysis.NewAnalysis(spatialanalysis.AnalysisInput{
		SnapshotID: snapshotID,
		Area: spatialanalysis.AreaCalculation{
			Method: spatialanalysis.AreaMethod, TotalSquareMeters: 100,
			InputReferences: []string{"geometry://zone-1"},
		},
		Zones:        []spatialanalysis.ZoneResult{unavailableSpatialZone("zone-1")},
		CalculatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	validTo := now.Add(2 * time.Hour)
	result := ports.HazardAuthorityRead{
		Snapshot: hazard.Snapshot{
			ID: snapshotID, HazardType: hazard.TypeLandslide, Status: hazard.SnapshotAvailable,
			ValidTo: validTo, Source: provenance.Provenance{ValidTo: validTo},
		},
		Zones: []ports.HazardZoneSummary{{ID: "zone-1", SnapshotID: snapshotID, Level: hazard.RiskHigh}},
		Assessment: risk.Assessment{
			ID: "risk-authority-1", HazardType: hazard.TypeLandslide, SnapshotID: snapshotID,
			Decision: &risk.Decision{
				Level: hazard.RiskHigh, ZoneCount: 1,
				HighestZoneIDs: []string{"zone-1"}, Basis: "highest_zone_level",
			},
			Status: risk.AssessmentAvailable, DataStatus: risk.DataCurrent,
			Confidence: risk.Confidence{Level: risk.ConfidenceHigh}, RuleVersion: risk.RuleVersion,
			EvaluatedAt: now,
		},
		TotalZoneCount: 1, TotalGeometryPoints: 5, TotalGeometryBytes: 256,
	}
	return result, spatialResult
}

func unavailableSpatialZone(id string) spatialanalysis.ZoneResult {
	return spatialanalysis.ZoneResult{
		ZoneID: id,
		Area:   spatialanalysis.ZoneArea{SquareMeters: 100, InputReferences: []string{"geometry://" + id}},
		Population: spatialanalysis.PopulationExposureMetric{
			Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.PopulationUnit,
			Limitations: []string{"缺少人口基线"},
		},
		Roads: spatialanalysis.RoadExposureMetric{
			Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.RoadUnit,
			Limitations: []string{"缺少道路基线"},
		},
		POIs: spatialanalysis.POIExposureMetric{
			Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.POIUnit,
			Limitations: []string{"缺少 POI 基线"},
		},
		Administration: spatialanalysis.AdministrativeMatch{
			Status: spatialanalysis.AdminMatchUnavailable, Limitations: []string{"缺少行政边界"},
		},
	}
}

func validLossAssessment(t *testing.T, now time.Time) loss.Assessment {
	t.Helper()
	evidence := validLossEvidence(now)
	value := loss.Assessment{
		SnapshotID: evidence.Snapshot.ID, FormulaVersion: loss.FormulaVersion,
		ScenarioMethod: "conditional_physical_damage", HazardType: evidence.Snapshot.HazardType,
		RegionCode:          evidence.SpatialAnalysis.RegionCode,
		ConditionalLowCents: 100, ConditionalMidCents: 200, ConditionalHighCents: 300,
		ImpactAreaSquareM: 100, AffectedPopulation: 10, AffectedRoadMeters: 100,
		InputReferences: loss.EvidenceReferences(evidence),
		IncludedAssets:  []loss.AssetType{loss.AssetFacility, loss.AssetRoad},
		Status:          loss.AssessmentAvailable, Confidence: 0.8, ConfidenceBand: "high",
		CalculatedAt: now, Evidence: evidence,
	}
	bound, err := loss.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func validLossEvidence(now time.Time) loss.AssessmentEvidence {
	baseline := baselineSource(now)
	projectionDigest := strings.Repeat("c", 64)
	return loss.AssessmentEvidence{
		Version: loss.EvidenceVersion,
		Snapshot: loss.SnapshotEvidence{
			ID: "snapshot-loss-1", HazardType: "landslide", ModelName: "LHASA",
			ModelVersion: "v2", Status: "available", RunAt: now.Add(-time.Hour),
			ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Source: riskSource(now),
		},
		SpatialAnalysis: loss.SpatialAnalysisEvidence{
			ID: "spatial-loss-1", Version: spatialanalysis.AnalysisVersion,
			Digest: strings.Repeat("b", 64), ProjectionID: "exposure-" + projectionDigest,
			ProjectionVersion: loss.RiskProjectionVersion, ProjectionDigest: projectionDigest,
			ProjectionCollectedAt: now.Add(-30 * time.Minute),
			ProjectionValidFrom:   now.Add(-time.Hour), ProjectionValidTo: now.Add(time.Hour),
			SourceReferenceDigests: []string{strings.Repeat("d", 64)},
			ProjectionLimitations:  []string{},
			AdminBoundaryID:        "CHN-ADM0-2026", AdminBoundaryDigest: strings.Repeat("e", 64),
			Status:     "available",
			RegionCode: "CN", TotalAreaSquareM: 100, CalculatedAt: now,
			InputReferences: []string{"geometry://zone-1"}, DatasetReferences: []string{"dataset://roads/v1"},
		},
		BaselineSet:   loss.BaselineSetEvidence{Provider: baseline.Provider, Dataset: baseline.Dataset, Version: baseline.DatasetVersion},
		IntensityBand: "high",
		RiskZones:     []loss.RiskZoneEvidence{{ID: "zone-1", Level: "high", AreaSquareMeters: 100, AdminCodes: []string{"CN"}}},
		Population: []loss.PopulationEvidence{{
			FeatureID: "population-zone-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"},
			Quantity: 10, Unit: "people", CoverageRatio: 1, Provided: true,
			MetricStatus: "available", InputReferences: []string{"dataset://population/v1"},
		}},
		Exposures: []loss.Exposure{
			validExposure(loss.AssetFacility, "facility-zone-1", "count", 0, "dataset://facilities/v1"),
			validExposure(loss.AssetRoad, "road-zone-1", "meters", 100, "dataset://roads/v1"),
		},
		Costs: []loss.CostBaseline{
			validCostBaseline(now, baseline, loss.AssetFacility, "count"),
			validCostBaseline(now, baseline, loss.AssetRoad, "meters"),
		},
		Vulnerabilities: []loss.Vulnerability{
			validVulnerability(baseline, loss.AssetFacility),
			validVulnerability(baseline, loss.AssetRoad),
		},
	}
}

func validExposure(asset loss.AssetType, featureID, unit string, quantity float64,
	reference string,
) loss.Exposure {
	return loss.Exposure{
		FeatureID: featureID, ZoneID: "zone-1", ZoneIDs: []string{"zone-1"},
		AssetType: asset, Quantity: quantity, Unit: unit, CoverageRatio: 1, Provided: true,
		MetricStatus: "available", IntensityBand: "high", AnalysisID: "spatial-loss-1",
		AnalysisVersion: spatialanalysis.AnalysisVersion, InputReferences: []string{reference},
	}
}

func validCostBaseline(now time.Time, source provenance.Provenance, asset loss.AssetType,
	unit string,
) loss.CostBaseline {
	return loss.CostBaseline{
		ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: unit,
		LowCents: 1, CentralCents: 2, HighCents: 3, Currency: "CNY",
		PriceBaseDate: now.Add(-24 * time.Hour), Status: loss.BaselineApproved,
		Provided: true, BaselineLevel: loss.BaselineNational,
		ApprovedBy: "QA 审核", Source: source,
	}
}

func validVulnerability(source provenance.Provenance, asset loss.AssetType) loss.Vulnerability {
	return loss.Vulnerability{
		ID: "vulnerability-" + string(asset) + "-high", AssetType: asset, HazardType: "landslide",
		IntensityBand: "high", ImpactFractionLow: 0.4, ImpactFractionMid: 0.5,
		ImpactFractionHigh: 0.6, DamageRatioLow: 0.1, DamageRatioMid: 0.2,
		DamageRatioHigh: 0.3, CalibrationRegion: "CN", Status: loss.BaselineApproved,
		Provided: true, BaselineLevel: loss.BaselineNational,
		ApprovedBy: "QA 审核", Source: source,
	}
}

func baselineSource(now time.Time) provenance.Provenance {
	return provenance.Provenance{
		Provider: "public-baseline", Dataset: "loss-baseline", DatasetVersion: "v1",
		SourceRevision: "revision-1", SourceURI: "https://example.test/baseline",
		Citation: "公开审核基线", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline,
		FetchedAt: now.Add(-time.Hour), ValidFrom: now.Add(-24 * time.Hour), ValidTo: now.Add(24 * time.Hour),
		TransformVersion: "baseline-transform-v1", QualityFlags: []string{"approved"},
		SHA256: strings.Repeat("a", 64),
	}
}

func riskSource(now time.Time) provenance.Provenance {
	return provenance.Provenance{
		Provider: "NASA", Dataset: "LHASA", SourceURI: "https://example.test/lhasa",
		DataKind: provenance.DataKindNowcast, FetchedAt: now.Add(-time.Hour),
		ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour),
	}
}

func validRouteSnapshot(now time.Time) hazard.Snapshot {
	validTo := now.Add(2 * time.Hour)
	return hazard.Snapshot{
		ID: "snapshot-route-1", HazardType: hazard.TypeLandslide,
		Status: hazard.SnapshotAvailable, ValidTo: validTo,
		Source: provenance.Provenance{ValidTo: validTo},
	}
}

func validRoute() evacuation.Route {
	return evacuation.Route{
		ID: "provider-route-1", Origin: spatial.Point{Longitude: 116.4, Latitude: 39.9},
		Destination: spatial.Point{Longitude: 116.5, Latitude: 39.8}, Mode: evacuation.TravelDriving,
		DistanceMeters: 1200, DurationSeconds: 360, RiskScore: 2.5,
		RiskScoreProvided: true, Rank: 1,
	}
}

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

type riskReaderStub struct {
	value      ports.HazardAuthorityRead
	err        error
	lastLimits ports.HazardAuthorityLimits
	calls      int
}

func (s *riskReaderStub) ReadAuthority(_ context.Context, id string,
	limits ports.HazardAuthorityLimits,
) (ports.HazardAuthorityRead, error) {
	s.calls++
	s.lastLimits = limits
	if s.err != nil {
		return ports.HazardAuthorityRead{}, s.err
	}
	if id != s.value.Snapshot.ID {
		return ports.HazardAuthorityRead{}, fmt.Errorf("%w: 风险快照不存在", domain.ErrNotFound)
	}
	return s.value, nil
}

type spatialReaderStub struct {
	value spatialanalysis.Analysis
	err   error
}

func (s *spatialReaderStub) Get(context.Context, string) (spatialanalysis.Analysis, error) {
	return s.value, s.err
}

func (s *spatialReaderStub) LatestBySnapshot(_ context.Context, id string) (spatialanalysis.Analysis, error) {
	if s.err != nil {
		return spatialanalysis.Analysis{}, s.err
	}
	if id != s.value.SnapshotID {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 空间分析不存在", domain.ErrNotFound)
	}
	return s.value, nil
}

type lossReaderStub struct {
	value loss.Assessment
	err   error
}

type mutatingSurvivalService struct {
	base   applicationsurvival.AssessmentService
	mutate func(*applicationsurvival.ReplayAssessment)
}

func (s *mutatingSurvivalService) AssessCase(ctx context.Context,
	id string,
) (applicationsurvival.ReplayAssessment, error) {
	value, err := s.base.AssessCase(ctx, id)
	if err == nil && s.mutate != nil {
		s.mutate(&value)
	}
	return value, err
}

func (s *lossReaderStub) GetAssessment(_ context.Context, id string) (loss.Assessment, error) {
	if s.err != nil {
		return loss.Assessment{}, s.err
	}
	if id != s.value.ID {
		return loss.Assessment{}, fmt.Errorf("%w: 损失评估不存在", domain.ErrNotFound)
	}
	return s.value, nil
}

type jsonCache struct {
	data   map[string][]byte
	ttls   map[string]time.Duration
	getErr error
	setErr error
}

func newJSONCache() *jsonCache {
	return &jsonCache{data: make(map[string][]byte), ttls: make(map[string]time.Duration)}
}

func (c *jsonCache) Get(_ context.Context, key string, destination any) (bool, error) {
	if c.getErr != nil {
		return false, c.getErr
	}
	payload, exists := c.data[key]
	if !exists {
		return false, nil
	}
	return true, json.Unmarshal(payload, destination)
}

func (c *jsonCache) Set(_ context.Context, key string, value any, ttl time.Duration) error {
	if c.setErr != nil {
		return c.setErr
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.data[key], c.ttls[key] = payload, ttl
	return nil
}

func (c *jsonCache) Delete(_ context.Context, key string) error {
	delete(c.data, key)
	return nil
}
