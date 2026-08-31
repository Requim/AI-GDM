package exposurecollection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestCollectorPersistsBoundedRealProjection(t *testing.T) {
	fixture := newCollectorFixture(t)
	value, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID, fixture.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	analysis := value.Input.Analysis
	if fixture.writer.calls != 1 || analysis.Status != spatialanalysis.AnalysisAvailable ||
		analysis.RegionCode != "CN" || analysis.ProjectionID == "" || analysis.ProjectionDigest == "" {
		t.Fatalf("Collect() = %+v, writer calls=%d", value, fixture.writer.calls)
	}
	if !analysis.ProjectionValidFrom.Equal(value.ValidFrom) || !analysis.ProjectionValidTo.Equal(value.ValidTo) {
		t.Fatalf("投影窗口未绑定进内容身份: %+v", analysis)
	}
	assertScopeAudit(t, value)
	assertFeatureKinds(t, analysis.Features)
}

func TestCollectorBindsScopeChangesIntoProjectionDigest(t *testing.T) {
	first := newCollectorFixture(t)
	firstValue, err := first.collector.Collect(context.Background(), first.input.Snapshot.ID, first.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := newCollectorFixture(t)
	second.geometries.value.Scope.TotalZoneCount = 2
	if err = BindExposureScopeIdentity(&second.geometries.value.Scope, second.geometries.value.Zones); err != nil {
		t.Fatal(err)
	}
	secondValue, err := second.collector.Collect(context.Background(), second.input.Snapshot.ID,
		second.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstValue.Input.Analysis.ProjectionDigest == secondValue.Input.Analysis.ProjectionDigest {
		t.Fatal("局部热点范围变化未进入暴露投影摘要")
	}
}

func TestCollectorRejectsGeometryFromDifferentSpatialAnalysis(t *testing.T) {
	fixture := newCollectorFixture(t)
	_, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID,
		fixture.input.Analysis.ID+"-other")
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("跨空间分析几何未 fail-closed: %v", err)
	}
}

func TestCollectorRejectsDisjointProviderWindow(t *testing.T) {
	fixture := newCollectorFixture(t)
	fixture.geometries.value.Snapshot.ValidTo = fixture.now.Add(-3 * time.Minute)
	_, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID, fixture.input.Analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) || fixture.writer.calls != 0 {
		t.Fatalf("Collect() error=%v writer calls=%d", err, fixture.writer.calls)
	}
}

func TestCollectorRejectsFutureCollectionTime(t *testing.T) {
	fixture := newCollectorFixture(t)
	fixture.population.value.CollectedAt = fixture.now.Add(time.Minute)
	_, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID, fixture.input.Analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) || fixture.writer.calls != 0 {
		t.Fatalf("Collect() error=%v writer calls=%d", err, fixture.writer.calls)
	}
}

func TestCollectorRejectsFutureBoundaryCollectionTime(t *testing.T) {
	fixture := newCollectorFixture(t)
	fixture.boundary.value.CollectedAt = fixture.now.Add(time.Minute)
	_, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID, fixture.input.Analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) || fixture.writer.calls != 0 {
		t.Fatalf("Collect() error=%v writer calls=%d", err, fixture.writer.calls)
	}
}

func TestCollectorRejectsBoundaryDifferentFromRiskSnapshot(t *testing.T) {
	fixture := newCollectorFixture(t)
	fixture.boundary.value.Digest = repeatedHex("c")
	_, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID,
		fixture.input.Analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) ||
		!strings.Contains(err.Error(), "行政边界版本不一致") || fixture.writer.calls != 0 {
		t.Fatalf("Collect() error=%v writer calls=%d", err, fixture.writer.calls)
	}
}

func TestCollectorUsesLatestCollectionTimeFromAllProviders(t *testing.T) {
	for _, latest := range []string{"boundary", "population", "infrastructure"} {
		t.Run(latest, func(t *testing.T) {
			fixture := newCollectorFixture(t)
			fixture.boundary.value.CollectedAt = fixture.now.Add(-3 * time.Minute)
			fixture.population.value.CollectedAt = fixture.now.Add(-3 * time.Minute)
			fixture.infrastructure.value.CollectedAt = fixture.now.Add(-3 * time.Minute)
			fixture.infrastructure.value.OSMBaseTimestamp = fixture.now.Add(-4 * time.Minute)
			want := fixture.now.Add(-time.Minute)
			switch latest {
			case "boundary":
				fixture.boundary.value.CollectedAt = want
			case "population":
				fixture.population.value.CollectedAt = want
			case "infrastructure":
				fixture.infrastructure.value.CollectedAt = want
			}
			value, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID,
				fixture.input.Analysis.ID)
			if err != nil || !value.Input.Analysis.ProjectionCollectedAt.Equal(want) {
				t.Fatalf("latest=%s collectedAt=%s error=%v", latest,
					value.Input.Analysis.ProjectionCollectedAt, err)
			}
		})
	}
}

func TestCollectorBindsProviderCollectionTimeIntoDigest(t *testing.T) {
	first := newCollectorFixture(t)
	first.boundary.value.CollectedAt = first.now.Add(-2 * time.Minute)
	firstValue, err := first.collector.Collect(context.Background(), first.input.Snapshot.ID, first.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := newCollectorFixture(t)
	second.boundary.value.CollectedAt = second.now.Add(-30 * time.Second)
	secondValue, err := second.collector.Collect(context.Background(), second.input.Snapshot.ID,
		second.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstValue.Input.Analysis.ProjectionDigest == secondValue.Input.Analysis.ProjectionDigest {
		t.Fatal("三方采集时间变化未进入暴露投影摘要")
	}
}

func TestCollectorRecordsMissingInfrastructureKindsAsAuditedZero(t *testing.T) {
	for _, item := range []struct {
		name   string
		values []applicationloss.LossExposureFeature
		kind   applicationloss.LossFeatureKind
		text   string
	}{
		{name: "road", values: infrastructureFeatures(geometryFixture(time.Now()).Zones)[:1],
			kind: applicationloss.LossFeatureRoad, text: "未发现道路要素"},
		{name: "facility", values: infrastructureFeatures(geometryFixture(time.Now()).Zones)[1:],
			kind: applicationloss.LossFeatureFacility, text: "未发现设施要素"},
		{name: "both", values: nil, kind: applicationloss.LossFeatureRoad, text: "未发现道路要素"},
	} {
		t.Run(item.name, func(t *testing.T) {
			fixture := newCollectorFixture(t)
			fixture.projector.values = item.values
			if item.name == "both" {
				fixture.infrastructure.value.Features = nil
			}
			value, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID,
				fixture.input.Analysis.ID)
			if err != nil || fixture.writer.calls != 1 {
				t.Fatalf("Collect() error=%v writer calls=%d", err, fixture.writer.calls)
			}
			assertZeroInfrastructureFeature(t, value.Input.Analysis.Features, item.kind)
			if !containsText(value.Input.Analysis.ProjectionLimitations, item.text) {
				t.Fatalf("零值限制未进入投影: %+v", value.Input.Analysis.ProjectionLimitations)
			}
			if item.name == "both" {
				assertZeroInfrastructureFeature(t, value.Input.Analysis.Features,
					applicationloss.LossFeatureFacility)
				if !containsText(value.Input.Analysis.ProjectionLimitations, "未发现设施要素") {
					t.Fatalf("设施零值限制未进入投影: %+v", value.Input.Analysis.ProjectionLimitations)
				}
			}
		})
	}
}

func TestCollectorCanonicalizesTimesBeforeIdentityBinding(t *testing.T) {
	base := newCollectorFixture(t)
	nanos := 789 * time.Nanosecond
	input := base.input
	input.Snapshot.RunAt = input.Snapshot.RunAt.Add(nanos)
	input.Snapshot.ValidFrom = input.Snapshot.ValidFrom.Add(nanos)
	input.Snapshot.ValidTo = input.Snapshot.ValidTo.Add(nanos)
	input.Snapshot.Source.FetchedAt = input.Snapshot.Source.FetchedAt.Add(nanos)
	input.Snapshot.Source.ValidFrom = input.Snapshot.Source.ValidFrom.Add(nanos)
	input.Snapshot.Source.ValidTo = input.Snapshot.Source.ValidTo.Add(nanos)
	input.Analysis.CalculatedAt = input.Analysis.CalculatedAt.Add(nanos)
	population := populationFixture(base.now)
	population.CollectedAt = population.CollectedAt.Add(nanos)
	population.ValidFrom = population.ValidFrom.Add(nanos)
	population.ValidTo = population.ValidTo.Add(nanos)
	infrastructure := infrastructureFixture(base.now)
	infrastructure.CollectedAt = infrastructure.CollectedAt.Add(nanos)
	infrastructure.ValidFrom = infrastructure.ValidFrom.Add(nanos)
	infrastructure.ValidTo = infrastructure.ValidTo.Add(nanos)
	writer := &writerStub{}
	collector, err := New(geometryStub{value: input}, boundaryStub{value: boundaryFixture(base.now.Add(nanos))},
		&populationStub{value: population}, infrastructureStub{value: infrastructure},
		administratorStub{value: administrationFixture(input)},
		&projectorStub{values: infrastructureFeatures(input.Zones)}, writer, fixedClock{now: base.now.Add(nanos)})
	if err != nil {
		t.Fatal(err)
	}
	value, err := collector.Collect(context.Background(), input.Snapshot.ID, input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMicrosecondTime(t, value.Input.Snapshot.RunAt)
	assertMicrosecondTime(t, value.Input.Snapshot.Source.FetchedAt)
	assertMicrosecondTime(t, value.Input.Analysis.CalculatedAt)
	assertMicrosecondTime(t, value.Input.Analysis.ProjectionCollectedAt)
	assertMicrosecondTime(t, value.ValidFrom)
	assertMicrosecondTime(t, value.ValidTo)
}

func TestCollectorPersistsProviderLimitationsInProjectionIdentity(t *testing.T) {
	fixture := newCollectorFixture(t)
	fixture.population.value.Limitations = []string{"WorldPop 数据集 URN 未由响应直接验证"}
	fixture.infrastructure.value.Limitations = []string{"已跳过非闭合设施 way"}
	value, err := fixture.collector.Collect(context.Background(), fixture.input.Snapshot.ID, fixture.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Input.Analysis.ProjectionLimitations) != 3 ||
		!slices.Contains(value.Input.Analysis.ProjectionLimitations, "WorldPop 数据集 URN 未由响应直接验证") ||
		!slices.Contains(value.Input.Analysis.ProjectionLimitations, "已跳过非闭合设施 way") ||
		fixture.writer.value.Input.Analysis.ProjectionDigest != value.Input.Analysis.ProjectionDigest {
		t.Fatalf("provider limitation 未进入持久投影身份: %+v", value.Input.Analysis)
	}
}

type collectorFixture struct {
	now            time.Time
	input          GeometryInput
	geometries     *geometryStub
	boundary       *boundaryStub
	population     *populationStub
	infrastructure *infrastructureStub
	projector      *projectorStub
	writer         *writerStub
	collector      *Collector
}

func newCollectorFixture(t *testing.T) collectorFixture {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 5, 0, 0, time.UTC)
	input := geometryFixture(now)
	geometries := &geometryStub{value: input}
	boundary := &boundaryStub{value: boundaryFixture(now)}
	population := &populationStub{value: populationFixture(now)}
	infrastructure := &infrastructureStub{value: infrastructureFixture(now)}
	projector := &projectorStub{values: infrastructureFeatures(input.Zones)}
	writer := &writerStub{}
	collector, err := New(geometries, boundary,
		population, infrastructure,
		administratorStub{value: administrationFixture(input)}, projector, writer, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	return collectorFixture{now: now, input: input, geometries: geometries, boundary: boundary, population: population,
		infrastructure: infrastructure,
		projector:      projector, writer: writer, collector: collector}
}

func geometryFixture(now time.Time) GeometryInput {
	geometry := json.RawMessage(`{"type":"Polygon","coordinates":[[[116,39],[116.01,39],[116.01,39.01],[116,39]]]}`)
	boundary := boundaryFixture(now)
	geometryDigest := boundaryGeometryDigest(boundary.Geometry)
	snapshot := hazard.Snapshot{ID: "snapshot-real", HazardType: hazard.TypeLandslide,
		ModelName: "LHASA", ModelVersion: "2", RunAt: now.Add(-time.Hour),
		ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Status: hazard.SnapshotAvailable,
		Source: provenance.Provenance{Provider: "NASA", Dataset: "LHASA", SourceURI: "https://example.test/lhasa",
			DataKind: provenance.DataKindNowcast, FetchedAt: now.Add(-time.Hour),
			ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour)},
		Coverage: &hazard.Coverage{Mode: hazard.CoverageAdministrativeBoundary,
			RegionCode: boundary.RegionCode, BoundaryID: boundary.BoundaryID,
			BoundaryType: boundary.BoundaryType, BoundaryVersion: boundary.BoundaryYear,
			Source: boundary.Source, License: boundary.License, Reference: boundary.Reference,
			SHA256: boundary.Digest, GeometrySHA256: geometryDigest,
			CollectedAt: boundary.CollectedAt}}
	zones := []applicationloss.LossRiskZone{{ID: "zone-a", SnapshotID: snapshot.ID,
		Level: hazard.RiskHigh, AreaSquareM: 1_000_000, AreaCalculated: true}}
	analysis := applicationloss.LossSpatialProjection{ID: "spatial-" + repeatedHex("a"),
		Version: "spatial-v1", Digest: repeatedHex("a"), SnapshotID: snapshot.ID,
		Status: spatialanalysis.AnalysisAreaOnly, TotalAreaSquareMeters: 1_000_000,
		CalculatedAt: now.Add(-30 * time.Minute), InputReferences: []string{"risk-zone:zone-a"},
		DatasetReferences: []string{"https://example.test/lhasa"}}
	value := GeometryInput{Snapshot: snapshot, Zones: zones, Analysis: analysis,
		UnionGeometry: geometry, Bounds: Bounds{South: 39, West: 116, North: 39.01, East: 116.01},
		Stats: GeometryStats{ZoneCount: 1, UnionGeometryBytes: int64(len(geometry)),
			MaxZonePoints: 4, TotalZonePoints: 4}, Scope: ExposureScope{Policy: ExposureScopePolicy,
			SeedZoneID: "zone-a", Window: Bounds{South: 38.98, West: 115.98, North: 39.03, East: 116.03},
			SelectedZoneCount: 1, TotalZoneCount: 1, SelectedAreaSquareMeters: 1_000_000,
			TotalAreaSquareMeters: 1_000_000}}
	if err := BindExposureScopeIdentity(&value.Scope, value.Zones); err != nil {
		panic(err)
	}
	return value
}

func boundaryGeometryDigest(raw json.RawMessage) string {
	var geometry spatial.Geometry
	if err := json.Unmarshal(raw, &geometry); err != nil {
		panic(err)
	}
	digest, err := hazard.BoundaryGeometryDigest(geometry)
	if err != nil {
		panic(err)
	}
	return digest
}

func boundaryFixture(now time.Time) AdministrativeBoundary {
	geometry := json.RawMessage(`{"type":"MultiPolygon","coordinates":[[[[116,39],[116.1,39],[116.1,39.1],[116,39]]]]}`)
	return AdministrativeBoundary{BoundaryID: "CHN-ADM0-351020", RegionCode: "CN",
		BoundaryType: "ADM0", BoundaryYear: "2019", Source: "geoBoundaries",
		License: "Public Domain", Digest: repeatedHex("b"), Reference: "https://github.com/wmgeolab/boundary.geojson",
		Geometry: geometry, CollectedAt: now.Add(-time.Minute),
		InputReferences: []string{"https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/"}}
}

func administrationFixture(input GeometryInput) AdministrativeProjection {
	zones := append([]applicationloss.LossRiskZone(nil), input.Zones...)
	zones[0].AreaSquareM, zones[0].AdminCodes = 900_000, []string{"CN"}
	boundary := boundaryFixture(input.Snapshot.ValidFrom.Add(time.Hour))
	return AdministrativeProjection{AnalysisID: input.Analysis.ID, SnapshotID: input.Snapshot.ID,
		RegionCode: "CN", BoundaryID: boundary.BoundaryID, BoundaryDigest: boundary.Digest,
		BoundaryReference: boundary.Reference, BoundaryGeometry: boundary.Geometry,
		UnionGeometry: input.UnionGeometry, Bounds: input.Bounds,
		TotalAreaSquareMeters: 900_000, Zones: zones}
}

func populationFixture(now time.Time) PopulationResult {
	return PopulationResult{TaskID: "123e4567-e89b-42d3-a456-426614174000", Total: 1234.5,
		AreaKM2: 0.9, DataYear: 2026, DataSource: "worldpop_R2025A_2026_100m",
		DatasetIdentity: defaultWorldPopDatasetIdentity,
		CollectedAt:     now.Add(-time.Minute), ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ValidTo:         time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		InputReferences: []string{"https://api.worldpop.org/v2/tasks/123e4567-e89b-42d3-a456-426614174000"}}
}

const defaultWorldPopDatasetIdentity = "urn:worldpop:global-annual-population:100m:v2"

func infrastructureFixture(now time.Time) InfrastructureResult {
	return InfrastructureResult{OSMBaseTimestamp: now.Add(-2 * time.Minute),
		CollectedAt: now.Add(-time.Minute), ValidFrom: now.Add(-2 * time.Minute), ValidTo: now.Add(24 * time.Hour),
		InputReferences: []string{"https://www.openstreetmap.org", "urn:openstreetmap:osm-base:2026-08-28T12:03:00Z"},
		Features: []RawInfrastructureFeature{{FeatureID: "osm-road-way-1", Kind: applicationloss.LossFeatureRoad,
			Geometry:        json.RawMessage(`{"type":"LineString","coordinates":[[116,39],[116.1,39.1]]}`),
			InputReferences: []string{"https://www.openstreetmap.org/way/1"}}}}
}

func infrastructureFeatures(zones []applicationloss.LossRiskZone) []applicationloss.LossExposureFeature {
	zoneIDs := []string{zones[0].ID}
	return []applicationloss.LossExposureFeature{
		{FeatureID: "osm-facility-node-2", Kind: applicationloss.LossFeatureFacility,
			ZoneIDs: zoneIDs, Quantity: 1, Unit: "count", CoverageRatio: 1,
			Status: spatialanalysis.MetricAvailable, Provided: true,
			InputReferences: []string{"https://www.openstreetmap.org/node/2"}},
		{FeatureID: "osm-road-way-1", Kind: applicationloss.LossFeatureRoad,
			ZoneIDs: zoneIDs, Quantity: 120, Unit: "meters", CoverageRatio: 1,
			Status: spatialanalysis.MetricAvailable, Provided: true,
			InputReferences: []string{"https://www.openstreetmap.org/way/1"}},
	}
}

type geometryStub struct{ value GeometryInput }

func (s geometryStub) ReadExposureGeometry(context.Context, string, string) (GeometryInput, error) {
	return s.value, nil
}

type boundaryStub struct{ value AdministrativeBoundary }

func (s boundaryStub) Boundary(context.Context) (AdministrativeBoundary, error) { return s.value, nil }

type populationStub struct{ value PopulationResult }

func (s *populationStub) Population(context.Context, PopulationQuery) (PopulationResult, error) {
	return s.value, nil
}

type infrastructureStub struct{ value InfrastructureResult }

func (s infrastructureStub) Infrastructure(context.Context, InfrastructureQuery) (InfrastructureResult, error) {
	return s.value, nil
}

type administratorStub struct{ value AdministrativeProjection }

func (s administratorStub) ProjectAdministration(context.Context, GeometryInput, AdministrativeBoundary,
	GeometryProjectionLimits) (AdministrativeProjection, error) {
	return s.value, nil
}

type projectorStub struct {
	values []applicationloss.LossExposureFeature
}

func (s *projectorStub) ProjectInfrastructure(context.Context, AdministrativeProjection,
	[]RawInfrastructureFeature, GeometryProjectionLimits) ([]applicationloss.LossExposureFeature, error) {
	return append([]applicationloss.LossExposureFeature(nil), s.values...), nil
}

type writerStub struct {
	calls int
	value ExposureProjection
}

func (s *writerStub) SaveExposureProjection(_ context.Context, value ExposureProjection) error {
	s.calls++
	s.value = value
	return nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func repeatedHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

func assertFeatureKinds(t *testing.T, values []applicationloss.LossExposureFeature) {
	t.Helper()
	kinds := map[applicationloss.LossFeatureKind]bool{}
	for _, value := range values {
		kinds[value.Kind] = true
	}
	if !kinds[applicationloss.LossFeaturePopulation] || !kinds[applicationloss.LossFeatureRoad] ||
		!kinds[applicationloss.LossFeatureFacility] {
		t.Fatalf("features=%+v", values)
	}
}

func assertZeroInfrastructureFeature(t *testing.T, values []applicationloss.LossExposureFeature,
	kind applicationloss.LossFeatureKind,
) {
	t.Helper()
	for _, value := range values {
		if value.Kind == kind && value.Quantity == 0 && value.Provided &&
			value.Status == spatialanalysis.MetricAvailable && len(value.InputReferences) > 0 {
			return
		}
	}
	t.Fatalf("未找到可审计的 %s 零值 feature: %+v", kind, values)
}

func assertScopeAudit(t *testing.T, value ExposureProjection) {
	t.Helper()
	analysis := value.Input.Analysis
	referencePrefix := "urn:ai-gdm:exposure-scope:" + ExposureScopePolicy + ":"
	if !containsPrefix(analysis.InputReferences, referencePrefix) ||
		!slices.Contains(analysis.DatasetReferences, "urn:ai-gdm:exposure-scope-policy:"+ExposureScopePolicy) ||
		!containsText(analysis.ProjectionLimitations, "不得解释为全国完整暴露") {
		t.Fatalf("局部范围审计未进入投影: %+v", analysis)
	}
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func containsText(values []string, text string) bool {
	for _, value := range values {
		if strings.Contains(value, text) {
			return true
		}
	}
	return false
}

func assertMicrosecondTime(t *testing.T, value time.Time) {
	t.Helper()
	if value.IsZero() || value.Nanosecond()%1_000 != 0 {
		t.Fatalf("时间未规范到 UTC 微秒: %s", value.Format(time.RFC3339Nano))
	}
	_, offset := value.Zone()
	if offset != 0 {
		t.Fatalf("时间不是 UTC: %s", value.Format(time.RFC3339Nano))
	}
}
