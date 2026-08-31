package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

const clippedExposureBoundaryGeoJSON = `{"type":"MultiPolygon","coordinates":[[[[115.999,38.99],[116.004,38.99],[116.004,39.02],[115.999,39.02],[115.999,38.99]]]]}`

const fullExposureBoundaryGeoJSON = `{"type":"MultiPolygon","coordinates":[[[[115.99,38.99],[116.02,38.99],[116.02,39.02],[115.99,39.02],[115.99,38.99]]]]}`

func TestAdministrativeBoundaryFixturesContainValidJSON(t *testing.T) {
	for name, payload := range map[string]string{
		"clipped": clippedExposureBoundaryGeoJSON,
		"full":    fullExposureBoundaryGeoJSON,
	} {
		t.Run(name, func(t *testing.T) {
			if !json.Valid([]byte(payload)) {
				t.Fatal("行政边界夹具不是合法 JSON")
			}
		})
	}
}

func TestExposureUnionSQLUsesOnlySelectedSpatialZones(t *testing.T) {
	for _, query := range []string{exposureUnionBudgetSQL, exposureUnionSQL} {
		if !strings.Contains(query, "JOIN spatial_zone_results") ||
			!strings.Contains(query, "szr.analysis_id=sa.id") ||
			!strings.Contains(query, "ST_PointOnSurface") || !strings.Contains(query, "LIMIT 10") {
			t.Fatalf("联合几何 SQL 未绑定空间分析行集: %s", query)
		}
	}
	if strings.Contains(exposureUnionBudgetSQL, "ST_AsGeoJSON") {
		t.Fatal("联合几何预算不得在复杂度门禁前物化 GeoJSON")
	}
}

func TestReadExposureGeometrySelectsHighestRiskWindow(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveRankedExposureZones(t, ctx, repository, now)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "ranked-scope", false)

	value, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSeed := snapshot.ID + "-zone-11"
	if value.Scope.Policy != exposurecollection.ExposureScopePolicy || value.Scope.SeedZoneID != wantSeed ||
		value.Scope.SelectedZoneCount != exposurecollection.MaxScopedRiskZones ||
		value.Scope.TotalZoneCount != len(zones) || value.Scope.CompleteCoverage || len(value.Zones) != 10 {
		t.Fatalf("局部热点范围选择错误: scope=%+v zones=%+v", value.Scope, value.Zones)
	}
	if math.Abs(value.Scope.Window.East-value.Scope.Window.West-0.05) > 1e-9 ||
		math.Abs(value.Scope.Window.North-value.Scope.Window.South-0.05) > 1e-9 {
		t.Fatalf("局部热点窗口不是 0.05 度: %+v", value.Scope.Window)
	}
	assertScopedZoneIDs(t, value.Zones, snapshot.ID)
	assertScopedExposureReferences(t, value, analysis, zones)
}

func assertScopedExposureReferences(t *testing.T, value exposurecollection.GeometryInput,
	analysis spatialanalysis.Analysis, allZones []hazard.RiskZone,
) {
	t.Helper()
	want := map[string]struct{}{analysis.Area.InputReferences[0]: {}}
	for _, zone := range value.Zones {
		want["geometry://"+zone.ID+"/ranked-scope"] = struct{}{}
	}
	if len(value.Analysis.InputReferences) != len(want) {
		t.Fatalf("局部热点引用未限界: got=%d want=%d", len(value.Analysis.InputReferences), len(want))
	}
	for _, reference := range value.Analysis.InputReferences {
		if _, exists := want[reference]; !exists {
			t.Fatalf("局部热点引用包含范围外输入: %s", reference)
		}
	}
	for _, zone := range allZones {
		if slices.ContainsFunc(value.Zones, func(selected applicationloss.LossRiskZone) bool {
			return selected.ID == zone.ID
		}) {
			continue
		}
		if slices.Contains(value.Analysis.InputReferences, "geometry://"+zone.ID+"/ranked-scope") {
			t.Fatalf("范围外风险区引用未被移除: %s", zone.ID)
		}
	}
}

func TestAdministrativeProjectionBudgetDoesNotSerializeGeoJSON(t *testing.T) {
	if strings.Contains(administrativeProjectionBudgetSQL, "ST_AsGeoJSON") {
		t.Fatal("行政裁剪预算不得在联合复杂度门禁前物化 GeoJSON")
	}
	if strings.Count(administrativeUnionSQL, "ST_AsGeoJSON") != 1 {
		t.Fatal("行政裁剪联合几何通过门禁后必须只序列化一次")
	}
}

func TestInfrastructureBindingBudgetIncludesPopulationZoneBindings(t *testing.T) {
	for _, value := range []struct {
		infrastructure int64
		zones          int64
		valid          bool
	}{
		{infrastructure: 9_998, zones: 2, valid: true},
		{infrastructure: 9_999, zones: 2, valid: false},
		{infrastructure: 0, zones: 2, valid: false},
		{infrastructure: -1, zones: 2, valid: false},
		{infrastructure: 10, zones: 0, valid: false},
	} {
		if got := validInfrastructureBindingBudget(value.infrastructure, value.zones); got != value.valid {
			t.Fatalf("binding budget(%d,%d)=%v", value.infrastructure, value.zones, got)
		}
	}
}

func TestReadExposureGeometryExcludesUnboundRiskZones(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "exposure-selected", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones[:1],
		now.Add(-time.Minute), "selected", false)

	value, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Zones) != 1 || value.Zones[0].ID != zones[0].ID || value.Stats.ZoneCount != 1 ||
		value.Bounds.East > 116.0100001 {
		t.Fatalf("未绑定风险区污染联合几何: zones=%+v stats=%+v bounds=%+v", value.Zones, value.Stats, value.Bounds)
	}
}

func TestReadExposureGeometryRejectsConservativeUnionOverflow(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveManyExposureZones(t, ctx, repository, now, 4)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "large-union", false)
	for index, zone := range zones {
		_, err := repository.pool.Exec(ctx, `UPDATE risk_zones SET geometry=
			ST_Buffer(ST_SetSRID(ST_Point($2,$3),4326),0.001,2400) WHERE id=$1`,
			zone.ID, 116+float64(index)*0.01, 39.0)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("ReadExposureGeometry() error=%v", err)
	}
}

func TestReadExposureGeometryRejectsUnionExpansionBeforeGeoJSON(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "union-expansion", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "union-expansion", false)
	writeCrossingExposureStripes(t, ctx, repository, zones)
	inputPoints, unionPoints := exposureUnionPointCounts(t, ctx, repository, analysis.ID)
	if conservativeUnionBytes(inputPoints, int64(len(zones))) > exposurecollection.MaxUnionGeometryBytes ||
		conservativeUnionBytes(unionPoints, 1) <= exposurecollection.MaxUnionGeometryBytes {
		t.Fatalf("联合膨胀夹具无效: input=%d union=%d", inputPoints, unionPoints)
	}
	var geoJSONQueries atomic.Int32
	traced := tracedExposureRepository(t, ctx, &geoJSONQueries)
	_, err := traced.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if !errors.Is(err, domain.ErrInsufficientData) || geoJSONQueries.Load() != 0 {
		t.Fatalf("联合几何未在 GeoJSON 前拒绝: error=%v geojson_queries=%d", err, geoJSONQueries.Load())
	}
}

func writeCrossingExposureStripes(t *testing.T, ctx context.Context,
	repository *HazardRepository, zones []hazard.RiskZone,
) {
	t.Helper()
	queries := []string{
		`WITH stripes AS (SELECT ST_MakeEnvelope(116,39+i*0.0001,116.01,
			39+i*0.0001+0.00004,4326) AS geom FROM GENERATE_SERIES(0,79) AS values(i))
			UPDATE risk_zones SET geometry=(SELECT ST_Multi(ST_UnaryUnion(ST_Collect(geom))) FROM stripes)
			WHERE id=$1`,
		`WITH stripes AS (SELECT ST_MakeEnvelope(116+i*0.0001,39,
			116+i*0.0001+0.00004,39.01,4326) AS geom FROM GENERATE_SERIES(0,79) AS values(i))
			UPDATE risk_zones SET geometry=(SELECT ST_Multi(ST_UnaryUnion(ST_Collect(geom))) FROM stripes)
			WHERE id=$1`,
	}
	for index, query := range queries {
		if _, err := repository.pool.Exec(ctx, query, zones[index].ID); err != nil {
			t.Fatal(err)
		}
	}
}

func exposureUnionPointCounts(t *testing.T, ctx context.Context, repository *HazardRepository,
	analysisID string,
) (int64, int64) {
	t.Helper()
	var input, union int64
	query := exposureSelectedZonesCTE + `SELECT COALESCE((SELECT SUM(ST_NPoints(geometry)) FROM selected_zones),0),
		COALESCE(ST_NPoints(geom),0) FROM merged`
	if err := repository.pool.QueryRow(ctx, query, analysisID).Scan(&input, &union); err != nil {
		t.Fatal(err)
	}
	return input, union
}

type exposureQueryTracer struct{ geoJSON *atomic.Int32 }

func (t exposureQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "ST_AsGeoJSON") {
		t.geoJSON.Add(1)
	}
	return ctx
}

func (exposureQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func tracedExposureRepository(t *testing.T, ctx context.Context,
	geoJSON *atomic.Int32,
) *HazardRepository {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = exposureQueryTracer{geoJSON: geoJSON}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewHazardRepository(pool)
}

func TestProjectAdministrationClipsAndOmitsOutsideZones(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "admin-clip", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "admin-clip", false)
	input, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	boundary := exposurecollection.AdministrativeBoundary{BoundaryID: "CHN-ADM0-test",
		RegionCode: "CN", BoundaryType: "ADM0", Digest: strings.Repeat("b", 64),
		Reference: "https://example.test/chn-adm0.geojson",
		Geometry:  json.RawMessage(clippedExposureBoundaryGeoJSON)}
	assertPostGISBoundary(t, ctx, repository, boundary)
	result, err := repository.ProjectAdministration(ctx, input, boundary,
		exposurecollection.GeometryProjectionLimits{MaxFeatures: 900, MaxGeometryBytes: 256 << 10,
			MaxPointsPerItem: 10_000, MaxTotalPoints: 250_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Zones) != 1 || result.Zones[0].ID != zones[0].ID ||
		result.Bounds.West < 115.998999 || result.Bounds.East > 116.004001 {
		t.Fatalf("行政裁剪未按精确边界执行: zones=%+v bounds=%+v", result.Zones, result.Bounds)
	}
	for _, zone := range result.Zones {
		if !zone.AreaCalculated || len(zone.AdminCodes) != 1 || zone.AdminCodes[0] != "CN" {
			t.Fatalf("行政区绑定无效: %+v", zone)
		}
	}
}

func TestProjectAdministrationDoesNotExpandBeyondExposureScope(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveSeparatedExposureZones(t, ctx, repository, now)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "scope-no-expand", false)
	input, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	boundary := fullChinaLikeExposureBoundary()
	result, err := repository.ProjectAdministration(ctx, input, boundary, testExposureGeometryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Zones) != 1 || len(result.Zones) != 1 || result.Zones[0].ID != input.Zones[0].ID {
		t.Fatalf("行政投影扩回局部范围外: input=%+v result=%+v", input.Zones, result.Zones)
	}
}

func TestProjectAdministrationRejectsIntersectionExpansionBeforeGeoJSON(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "admin-union-expansion", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "admin-union-expansion", false)
	writeExposureStripes(t, ctx, repository, zones[0].ID, true)
	input, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	boundary := verticalExposureStripeBoundary(t, ctx, repository)
	var geoJSONQueries atomic.Int32
	traced := tracedExposureRepository(t, ctx, &geoJSONQueries)
	_, err = traced.ProjectAdministration(ctx, input, boundary,
		exposurecollection.GeometryProjectionLimits{MaxFeatures: 900, MaxGeometryBytes: 256 << 10,
			MaxPointsPerItem: 100_000, MaxTotalPoints: 250_000})
	if !errors.Is(err, domain.ErrInsufficientData) || geoJSONQueries.Load() != 0 {
		t.Fatalf("行政交点膨胀未在 GeoJSON 前拒绝: error=%v geojson_queries=%d", err, geoJSONQueries.Load())
	}
}

func writeExposureStripes(t *testing.T, ctx context.Context, repository *HazardRepository,
	zoneID string, horizontal bool,
) {
	t.Helper()
	query := `WITH stripes AS (SELECT ST_MakeEnvelope(116+i*0.0001,39,
		116+i*0.0001+0.00004,39.01,4326) AS geom FROM GENERATE_SERIES(0,79) AS values(i))
		UPDATE risk_zones SET geometry=(SELECT ST_Multi(ST_UnaryUnion(ST_Collect(geom))) FROM stripes)
		WHERE id=$1`
	if horizontal {
		query = `WITH stripes AS (SELECT ST_MakeEnvelope(116,39+i*0.0001,116.01,
			39+i*0.0001+0.00004,4326) AS geom FROM GENERATE_SERIES(0,79) AS values(i))
			UPDATE risk_zones SET geometry=(SELECT ST_Multi(ST_UnaryUnion(ST_Collect(geom))) FROM stripes)
			WHERE id=$1`
	}
	if _, err := repository.pool.Exec(ctx, query, zoneID); err != nil {
		t.Fatal(err)
	}
}

func verticalExposureStripeBoundary(t *testing.T, ctx context.Context,
	repository *HazardRepository,
) exposurecollection.AdministrativeBoundary {
	t.Helper()
	var payload string
	query := `WITH stripes AS (SELECT ST_MakeEnvelope(116+i*0.0001,39,
		116+i*0.0001+0.00004,39.01,4326) AS geom FROM GENERATE_SERIES(0,79) AS values(i))
		SELECT ST_AsGeoJSON(ST_Multi(ST_UnaryUnion(ST_Collect(geom))),9,0) FROM stripes`
	if err := repository.pool.QueryRow(ctx, query).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return exposurecollection.AdministrativeBoundary{BoundaryID: "CHN-ADM0-intersection-expansion",
		RegionCode: "CN", BoundaryType: "ADM0", Digest: strings.Repeat("c", 64),
		Reference: "https://example.test/boundary/intersection-expansion", Geometry: json.RawMessage(payload)}
}

func TestProjectInfrastructureDeduplicatesOverlappingZones(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "infrastructure-dedup", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "infrastructure-dedup", false)
	input, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	boundary := fullExposureBoundary()
	assertPostGISBoundary(t, ctx, repository, boundary)
	administration, err := repository.ProjectAdministration(ctx, input, boundary, testExposureGeometryLimits())
	if err != nil {
		t.Fatal(err)
	}
	roadGeometry := json.RawMessage(`{"type":"LineString","coordinates":[[115.999,39.005],[116.016,39.005]]}`)
	features := []exposurecollection.RawInfrastructureFeature{
		{FeatureID: "osm-facility-node-1", Kind: applicationloss.LossFeatureFacility,
			Geometry:        json.RawMessage(`{"type":"Point","coordinates":[116.007,39.005]}`),
			InputReferences: []string{"https://www.openstreetmap.org/node/1"}},
		{FeatureID: "osm-road-way-2", Kind: applicationloss.LossFeatureRoad, Geometry: roadGeometry,
			InputReferences: []string{"https://www.openstreetmap.org/way/2"}},
	}
	got, err := repository.ProjectInfrastructure(ctx, administration, features, testExposureGeometryLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertProjectedInfrastructure(t, ctx, repository, analysis.ID, zones, roadGeometry, got)
}

func TestProjectInfrastructureUsesAdministrativeUnionGeometry(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "infrastructure-scope", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-time.Minute), "infrastructure-scope", false)
	input, err := repository.ReadExposureGeometry(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	administration, err := repository.ProjectAdministration(ctx, input, fullExposureBoundary(),
		testExposureGeometryLimits())
	if err != nil {
		t.Fatal(err)
	}
	administration.UnionGeometry = json.RawMessage(`{"type":"Polygon","coordinates":[[[116,39],[116.002,39],[116.002,39.01],[116,39.01],[116,39]]]}`)
	features := []exposurecollection.RawInfrastructureFeature{{FeatureID: "osm-facility-node-outside-scope",
		Kind:            applicationloss.LossFeatureFacility,
		Geometry:        json.RawMessage(`{"type":"Point","coordinates":[116.008,39.005]}`),
		InputReferences: []string{"https://www.openstreetmap.org/node/99"}}}
	_, err = repository.ProjectInfrastructure(ctx, administration, features, testExposureGeometryLimits())
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("基础设施投影越过行政联合几何: %v", err)
	}
}

func assertProjectedInfrastructure(t *testing.T, ctx context.Context, repository *HazardRepository,
	analysisID string, zones []hazard.RiskZone, roadGeometry json.RawMessage,
	values []applicationloss.LossExposureFeature,
) {
	t.Helper()
	byKind := make(map[applicationloss.LossFeatureKind]applicationloss.LossExposureFeature, len(values))
	for _, value := range values {
		byKind[value.Kind] = value
	}
	road, facility := byKind[applicationloss.LossFeatureRoad], byKind[applicationloss.LossFeatureFacility]
	if len(values) != 2 || len(road.ZoneIDs) != 2 || len(facility.ZoneIDs) != 2 || facility.Quantity != 1 ||
		road.ZoneIDs[0] != zones[0].ID || road.ZoneIDs[1] != zones[1].ID {
		t.Fatalf("跨重叠区 feature 未全局去重: %+v", values)
	}
	var unionLength, summedZoneLength float64
	if err := repository.pool.QueryRow(ctx, exposureRoadDedupExpectationSQL,
		analysisID, string(roadGeometry)).Scan(&unionLength, &summedZoneLength); err != nil {
		t.Fatal(err)
	}
	if math.Abs(road.Quantity-unionLength) > 0.01 || !(road.Quantity < summedZoneLength) {
		t.Fatalf("道路未按 union 只计一次: got=%v union=%v zoneSum=%v", road.Quantity, unionLength, summedZoneLength)
	}
}

func fullExposureBoundary() exposurecollection.AdministrativeBoundary {
	return exposurecollection.AdministrativeBoundary{BoundaryID: "CHN-ADM0-test",
		RegionCode: "CN", BoundaryType: "ADM0", Digest: strings.Repeat("b", 64),
		Reference: "https://example.test/chn-adm0.geojson",
		Geometry:  json.RawMessage(fullExposureBoundaryGeoJSON)}
}

func assertPostGISBoundary(t *testing.T, ctx context.Context, repository *HazardRepository,
	boundary exposurecollection.AdministrativeBoundary,
) {
	t.Helper()
	var geometryType string
	err := repository.pool.QueryRow(ctx, `SELECT ST_GeometryType(
		ST_SetSRID(ST_GeomFromGeoJSON($1),4326))`, string(boundary.Geometry)).Scan(&geometryType)
	if err != nil {
		t.Fatalf("PostGIS 无法解析行政边界夹具: %v", err)
	}
	if geometryType != "ST_MultiPolygon" {
		t.Fatalf("行政边界夹具几何类型 = %s, want ST_MultiPolygon", geometryType)
	}
}

func testExposureGeometryLimits() exposurecollection.GeometryProjectionLimits {
	return exposurecollection.GeometryProjectionLimits{MaxFeatures: 900, MaxGeometryBytes: 256 << 10,
		MaxPointsPerItem: 10_000, MaxTotalPoints: 250_000}
}

func saveManyExposureZones(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time, count int,
) (hazard.Snapshot, []hazard.RiskZone) {
	t.Helper()
	snapshot, _ := storageFixture(now.Add(-10 * time.Minute))
	snapshot.ID = fmt.Sprintf("exposure-many-%d", time.Now().UnixNano())
	snapshot = normalizeSnapshotForStorage(snapshot)
	zones := make([]hazard.RiskZone, count)
	for index := range zones {
		west := 116 + float64(index)*0.01
		coordinates := json.RawMessage(fmt.Sprintf(
			`[[[%[1]f,39],[%[2]f,39],[%[2]f,39.005],[%[1]f,39.005],[%[1]f,39]]]`, west, west+0.005))
		zones[index] = lossProjectionRiskZone(snapshot.ID, fmt.Sprintf("zone-%02d", index),
			hazard.RiskModerate, coordinates)
	}
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, zones); err != nil {
		t.Fatal(err)
	}
	return snapshot, zones
}

func saveRankedExposureZones(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time,
) (hazard.Snapshot, []hazard.RiskZone) {
	t.Helper()
	snapshot, _ := storageFixture(now.Add(-10 * time.Minute))
	snapshot.ID = fmt.Sprintf("exposure-ranked-%d", time.Now().UnixNano())
	snapshot = normalizeSnapshotForStorage(snapshot)
	zones := make([]hazard.RiskZone, 12)
	for index := range zones {
		west := 116 + float64(index%3)*0.001
		coordinates := json.RawMessage(fmt.Sprintf(
			`[[[%[1]f,39],[%[2]f,39],[%[2]f,39.01],[%[1]f,39.01],[%[1]f,39]]]`, west, west+0.008))
		level := hazard.RiskHigh
		if index >= 10 {
			level = hazard.RiskVeryHigh
		}
		zones[index] = lossProjectionRiskZone(snapshot.ID, fmt.Sprintf("zone-%02d", index), level, coordinates)
		zones[index].AreaSquareM = float64(100 + index)
	}
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, zones); err != nil {
		t.Fatal(err)
	}
	return snapshot, zones
}

func assertScopedZoneIDs(t *testing.T, zones []applicationloss.LossRiskZone, snapshotID string) {
	t.Helper()
	seen := make(map[string]bool, len(zones))
	for _, zone := range zones {
		seen[zone.ID] = true
	}
	for index := 2; index < 12; index++ {
		if !seen[fmt.Sprintf("%s-zone-%02d", snapshotID, index)] {
			t.Fatalf("排序后应保留 zone-%02d: %+v", index, zones)
		}
	}
}

func saveSeparatedExposureZones(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time,
) (hazard.Snapshot, []hazard.RiskZone) {
	t.Helper()
	snapshot, _ := storageFixture(now.Add(-10 * time.Minute))
	snapshot.ID = fmt.Sprintf("exposure-separated-%d", time.Now().UnixNano())
	snapshot = normalizeSnapshotForStorage(snapshot)
	zones := []hazard.RiskZone{
		lossProjectionRiskZone(snapshot.ID, "near", hazard.RiskVeryHigh,
			json.RawMessage(`[[[116,39],[116.01,39],[116.01,39.01],[116,39.01],[116,39]]]`)),
		lossProjectionRiskZone(snapshot.ID, "far", hazard.RiskLow,
			json.RawMessage(`[[[117,40],[117.01,40],[117.01,40.01],[117,40.01],[117,40]]]`)),
	}
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, zones); err != nil {
		t.Fatal(err)
	}
	return snapshot, zones
}

func fullChinaLikeExposureBoundary() exposurecollection.AdministrativeBoundary {
	return exposurecollection.AdministrativeBoundary{BoundaryID: "CHN-ADM0-wide", RegionCode: "CN",
		BoundaryType: "ADM0", Digest: strings.Repeat("d", 64),
		Reference: "https://example.test/chn-adm0-wide.geojson",
		Geometry:  json.RawMessage(`{"type":"MultiPolygon","coordinates":[[[[115,38],[118,38],[118,41],[115,41],[115,38]]]]}`)}
}

const exposureRoadDedupExpectationSQL = `WITH selected AS (
    SELECT rz.geometry FROM spatial_zone_results szr JOIN risk_zones rz ON rz.id=szr.zone_id
    WHERE szr.analysis_id=$1
), merged AS (
    SELECT ST_UnaryUnion(ST_Collect(geometry)) AS geometry FROM selected
), road AS (
    SELECT ST_SetSRID(ST_GeomFromGeoJSON($2),4326) AS geometry
)
SELECT ST_Length(ST_Intersection(r.geometry,m.geometry)::geography),
    (SELECT SUM(ST_Length(ST_Intersection(r.geometry,s.geometry)::geography)) FROM selected s)
FROM merged m CROSS JOIN road r`
