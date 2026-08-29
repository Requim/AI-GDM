package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestLossSpatialFixtureNormalizesNilStringSlices(t *testing.T) {
	for _, value := range []struct {
		name   string
		values []string
		want   string
	}{
		{name: "nil", want: "[]"},
		{name: "empty", values: []string{}, want: "[]"},
		{name: "values", values: []string{"fixture"}, want: `["fixture"]`},
	} {
		t.Run(value.name, func(t *testing.T) {
			if got := string(mustLossStringArrayJSON(t, value.values)); got != value.want {
				t.Fatalf("字符串数组 JSON = %s, want %s", got, value.want)
			}
		})
	}
}

func TestLossProjectionAccepts1000DeduplicatedFeaturesAndRejects1001(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "feature-budget", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-5*time.Minute), "v1", true)
	insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones, "v1", 1000,
		now.Add(-2*time.Minute), true)

	projection, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Analysis.Features) != 1000 || projection.Stats.FeatureCount != 1000 {
		t.Fatalf("1000 条 feature 投影不完整: stats=%+v", projection.Stats)
	}
	assertSharedLossFeature(t, projection, zones)
	assertLossAnalysisBinding(t, projection, analysis)

	insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones, "v2", 1001,
		now.Add(-time.Minute), true)
	if _, err = repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("1001 条 feature 未 fail-closed: %v", err)
	}
}

func TestLossProjectionRejectsOversizedGeometryAndSpatialJSON(t *testing.T) {
	t.Run("超大Polygon", func(t *testing.T) {
		ctx, repository := integrationHazardRepository(t)
		now := time.Now().UTC()
		snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "large-polygon", 1)
		analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-5*time.Minute), "v1", true)
		insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones, "v1", 3,
			now.Add(-time.Minute), true)
		_, err := repository.pool.Exec(ctx, `UPDATE risk_zones SET geometry=
			ST_Buffer(ST_SetSRID(ST_Point(116,39),4326),0.1,3000) WHERE id=$1`, zones[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertLossProjectionInsufficient(t, ctx, repository, snapshot.ID, now)
	})

	t.Run("超大空间JSON", func(t *testing.T) {
		ctx, repository := integrationHazardRepository(t)
		now := time.Now().UTC()
		snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "large-json", 1)
		analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-5*time.Minute), "v1", true)
		insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones, "v1", 3,
			now.Add(-time.Minute), true)
		_, err := repository.pool.Exec(ctx, `UPDATE spatial_zone_results
			SET exposures=JSONB_BUILD_OBJECT('blob',REPEAT('x',17*1024*1024))
			WHERE analysis_id=$1 AND zone_id=$2`, analysis.ID, zones[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertLossProjectionInsufficient(t, ctx, repository, snapshot.ID, now)
	})
}

func TestLossProjectionUsesOneLatestAvailableAnalysis(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "analysis-binding", 2)
	oldAnalysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-5*time.Minute), "old", true)
	insertLossExposureProjection(t, ctx, repository, snapshot, oldAnalysis, zones, "old", 3,
		now.Add(-3*time.Minute), true)
	latest := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-4*time.Minute), "latest", true)
	insertLossExposureProjection(t, ctx, repository, snapshot, latest, zones, "latest", 3,
		now.Add(-2*time.Minute), true)

	projection, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertLossAnalysisBinding(t, projection, latest)
	for _, feature := range projection.Analysis.Features {
		if !reflect.DeepEqual(feature.InputReferences, []string{"dataset://features/latest"}) {
			t.Fatalf("混入其他空间分析 feature: %+v", feature)
		}
	}
	if projection.Analysis.ID == oldAnalysis.ID {
		t.Fatal("损失投影错误绑定旧空间分析")
	}
}

func TestLossProjectionUsesRealExposureOnAreaOnlyAnalysis(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "area-only-real", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "area-only", false)
	want := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"worldpop-overpass", 3, now.Add(-time.Minute), true, "跳过非闭合设施 way 42")

	got, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Analysis.Status != spatialanalysis.AnalysisAvailable || got.Analysis.ProjectionID != want.Analysis.ProjectionID ||
		got.Analysis.RegionCode != "CN" || !reflect.DeepEqual(got.Zones[0].AdminCodes, []string{"CN"}) {
		t.Fatalf("真实 area_only 暴露投影绑定异常: %+v zones=%+v", got.Analysis, got.Zones)
	}
	if !reflect.DeepEqual(got.Analysis.ProjectionLimitations, []string{"跳过非闭合设施 way 42"}) ||
		got.Stats.ProjectionLimitationCount != 1 {
		t.Fatalf("空间投影限制未完整回读: analysis=%v stats=%+v", got.Analysis.ProjectionLimitations, got.Stats)
	}
}

func TestLossProjectionRejectsFutureCollectedProjection(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "future-collected", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "area-only", false)
	insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"future", 3, now.Add(time.Hour), true)
	assertLossProjectionInsufficient(t, ctx, repository, snapshot.ID, now)
	if _, err := repository.ReadLossInput(ctx, snapshot.ID, now.Add(2*time.Hour),
		productionLossProjectionLimits()); err != nil {
		t.Fatalf("显式应用时间已进入投影窗口仍未选中: %v", err)
	}
}

func TestLossProjectionDoesNotInventUnavailableFeatures(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "unavailable", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-5*time.Minute), "area-only", false)
	assertLossProjectionInsufficient(t, ctx, repository, snapshot.ID, now)

	var count int
	if err := repository.pool.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_exposure_features
		WHERE analysis_id=$1`, analysis.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("读取 area_only 分析后生成了 %d 条伪造 feature", count)
	}
}

func TestLossProjectionPinsCompleteProjectionInRepeatableRead(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "repeatable-read", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "area-only", false)
	oldProjection := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"old", 3, now.Add(-3*time.Minute), true)
	pending := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"new", 3, now.Add(-2*time.Minute), false)

	tx, err := repository.pool.BeginTx(ctx, lossProjectionReadOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	first, err := preflightLossProjection(ctx, tx, snapshot.ID, analysis.ID, now,
		productionLossProjectionLimits())
	if err != nil || first.projectionID != oldProjection.Analysis.ProjectionID {
		t.Fatalf("首次预检投影=%s error=%v", first.projectionID, err)
	}
	if _, err = repository.pool.Exec(ctx, `UPDATE spatial_exposure_projections SET complete=TRUE WHERE id=$1`,
		pending.Analysis.ProjectionID); err != nil {
		t.Fatal(err)
	}
	second, err := preflightLossProjection(ctx, tx, snapshot.ID, analysis.ID, now,
		productionLossProjectionLimits())
	if err != nil || second.projectionID != oldProjection.Analysis.ProjectionID {
		t.Fatalf("可重复读事务观察到迟到 complete: projection=%s error=%v", second.projectionID, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil || fresh.Analysis.ProjectionID != pending.Analysis.ProjectionID {
		t.Fatalf("新事务未选择最新 complete 投影: projection=%s error=%v", fresh.Analysis.ProjectionID, err)
	}
}

func TestLossProjectionContentAddressAndFacilityIntegerAreImmutable(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "content-address", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "area-only", false)
	first := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"first", 3, now.Add(-3*time.Minute), true)
	second := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"second", 3, now.Add(-2*time.Minute), true)
	if first.Analysis.ProjectionID == second.Analysis.ProjectionID ||
		first.Analysis.ProjectionDigest == second.Analysis.ProjectionDigest {
		t.Fatal("feature 来源变化未改变内容寻址投影")
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE spatial_exposure_features SET quantity=2
		WHERE projection_id=$1 AND feature_id='facility-shared'`, first.Analysis.ProjectionID); err == nil {
		t.Fatal("完整投影 feature 被静默改写")
	}
	incomplete := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"fractional", 3, now.Add(-time.Minute), false)
	if _, err := repository.pool.Exec(ctx, `UPDATE spatial_exposure_features SET quantity=1.5
		WHERE projection_id=$1 AND feature_id='facility-shared'`, incomplete.Analysis.ProjectionID); err == nil {
		t.Fatal("数据库接受了小数 facility/count")
	}
}

func TestCompletedLossProjectionRestrictsUpstreamDeletion(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "restrict-delete", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "area-only", false)
	projection := insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
		"complete", 3, now.Add(-time.Minute), true)

	if _, err := repository.pool.Exec(ctx, `DELETE FROM spatial_zone_results
		WHERE analysis_id=$1 AND zone_id=$2`, analysis.ID, zones[0].ID); err == nil {
		t.Fatal("删除上游 zone result 未被 complete 投影外键拒绝")
	}
	if _, err := repository.pool.Exec(ctx, `DELETE FROM spatial_analyses WHERE id=$1`, analysis.ID); err == nil {
		t.Fatal("删除上游 spatial analysis 未被 complete 投影外键拒绝")
	}
	var headers, projectedZones, features int
	if err := repository.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM spatial_exposure_projections WHERE id=$1),
		(SELECT COUNT(*) FROM spatial_exposure_projection_zones WHERE projection_id=$1),
		(SELECT COUNT(*) FROM spatial_exposure_features WHERE projection_id=$1)`,
		projection.Analysis.ProjectionID).Scan(&headers, &projectedZones, &features); err != nil {
		t.Fatal(err)
	}
	if headers != 1 || projectedZones != 1 || features != 3 {
		t.Fatalf("上游删除失败后 complete 投影不完整: header=%d zones=%d features=%d",
			headers, projectedZones, features)
	}
}

func TestLossProjectionRejectsInvalidLimitsBeforeDatabaseAccess(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	repository := NewHazardRepository(nil)
	_, err := repository.ReadLossInput(context.Background(), "snapshot", now, productionLossProjectionLimits())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("空数据库错误 = %v", err)
	}
	limits := productionLossProjectionLimits()
	limits.MaxProjectionBytes++
	repository = &HazardRepository{pool: &pgxpool.Pool{}}
	_, err = repository.ReadLossInput(context.Background(), "snapshot", now, limits)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("越过 1MiB 硬上限错误 = %v", err)
	}
}

func saveLossProjectionRisk(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time, suffix string, zoneCount int,
) (hazard.Snapshot, []hazard.RiskZone) {
	now = now.UTC().Truncate(time.Microsecond)
	return saveLossProjectionRiskWithWindow(t, ctx, repository, now, suffix, zoneCount,
		now.Add(-10*time.Minute), now.Add(50*time.Minute))
}

func saveLossProjectionRiskWithWindow(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time, suffix string, zoneCount int, validFrom, validTo time.Time,
) (hazard.Snapshot, []hazard.RiskZone) {
	t.Helper()
	now = now.UTC().Truncate(time.Microsecond)
	snapshot, _ := storageFixture(now.Add(-10 * time.Minute))
	snapshot.ValidFrom, snapshot.Source.ValidFrom = postgresTime(validFrom), postgresTime(validFrom)
	snapshot.ValidTo, snapshot.Source.ValidTo = postgresTime(validTo), postgresTime(validTo)
	snapshot.ID = fmt.Sprintf("loss-projection-%s-%d", suffix, time.Now().UnixNano())
	snapshot = normalizeSnapshotForStorage(snapshot)
	zones := []hazard.RiskZone{lossProjectionRiskZone(snapshot.ID, "zone-a", hazard.RiskModerate,
		json.RawMessage(`[[[116,39],[116.01,39],[116.01,39.01],[116,39.01],[116,39]]]`))}
	if zoneCount == 2 {
		zones = append(zones, lossProjectionRiskZone(snapshot.ID, "zone-b", hazard.RiskVeryHigh,
			json.RawMessage(`[[[116.005,39],[116.015,39],[116.015,39.01],[116.005,39.01],[116.005,39]]]`)))
	}
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, zones); err != nil {
		t.Fatal(err)
	}
	return snapshot, zones
}

func lossProjectionRiskZone(snapshotID, suffix string, level hazard.RiskLevel,
	coordinates json.RawMessage,
) hazard.RiskZone {
	return hazard.RiskZone{ID: snapshotID + "-" + suffix, SnapshotID: snapshotID,
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: coordinates},
		Minimum:  0.2, Mean: 0.5, Maximum: 0.9, Level: level,
		AreaSquareM: 100, AreaCalculated: true, AdminCodes: []string{"510000", "510100"},
		InputReferences: []string{"risk://" + suffix}, Limitations: []string{"仅用于仓储集成测试"}}
}

func insertLossSpatialAnalysis(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshot hazard.Snapshot, zones []hazard.RiskZone, calculatedAt time.Time, revision string, available bool,
) spatialanalysis.Analysis {
	t.Helper()
	value := newLossSpatialAnalysis(t, snapshot, zones, postgresTime(calculatedAt), revision, available)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertLossAnalysisHeader(t, ctx, tx, value)
	for _, zone := range value.Zones {
		insertLossZoneResult(t, ctx, tx, value, zone)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return value
}

func newLossSpatialAnalysis(t *testing.T, snapshot hazard.Snapshot, zones []hazard.RiskZone,
	calculatedAt time.Time, revision string, available bool,
) spatialanalysis.Analysis {
	t.Helper()
	results := make([]spatialanalysis.ZoneResult, 0, len(zones))
	for _, zone := range zones {
		if available {
			results = append(results, availableLossZoneResult(zone, revision))
		} else {
			results = append(results, unavailableLossZoneResult(zone, revision))
		}
	}
	total := float64(len(zones)) * 100
	if len(zones) > 1 {
		total -= 50
	}
	datasets := []string{"dataset://risk-zones/" + revision}
	limitations := []string{"真实图层 feature 由独立关系表显式提供"}
	if available {
		datasets = append(datasets, "dataset://admin/"+revision, "dataset://facility/"+revision,
			"dataset://population/"+revision, "dataset://roads/"+revision)
	}
	value, err := spatialanalysis.NewAnalysis(spatialanalysis.AnalysisInput{SnapshotID: snapshot.ID,
		Area: spatialanalysis.AreaCalculation{Method: spatialanalysis.AreaMethod, TotalSquareMeters: total,
			InputReferences: []string{"geometry://union/" + revision}},
		Zones: results, CalculatedAt: calculatedAt.UTC(), DatasetReferences: datasets, Limitations: limitations})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func availableLossZoneResult(zone hazard.RiskZone, revision string) spatialanalysis.ZoneResult {
	full := lossProjectionFloat(1)
	return spatialanalysis.ZoneResult{ZoneID: zone.ID,
		Area: spatialanalysis.ZoneArea{SquareMeters: zone.AreaSquareM,
			InputReferences: []string{"geometry://" + zone.ID + "/" + revision}},
		Population: spatialanalysis.PopulationExposureMetric{Status: spatialanalysis.MetricAvailable,
			Quantity: lossProjectionFloat(50), Unit: spatialanalysis.PopulationUnit, CoverageRatio: full,
			InputReferences: []string{"dataset://population/" + revision}},
		Roads: spatialanalysis.RoadExposureMetric{Status: spatialanalysis.MetricAvailable,
			Quantity: lossProjectionFloat(10), Unit: spatialanalysis.RoadUnit, CoverageRatio: full,
			InputReferences: []string{"dataset://roads/" + revision}},
		POIs: spatialanalysis.POIExposureMetric{Status: spatialanalysis.MetricAvailable,
			Quantity: lossProjectionFloat(2), Unit: spatialanalysis.POIUnit, CoverageRatio: full,
			InputReferences: []string{"dataset://facility/" + revision}},
		Administration: spatialanalysis.AdministrativeMatch{Status: spatialanalysis.AdminMatchAvailable,
			AdminCodes: []string{"510000", "510100"}, CoverageRatio: full,
			InputReferences: []string{"dataset://admin/" + revision}}}
}

func unavailableLossZoneResult(zone hazard.RiskZone, revision string) spatialanalysis.ZoneResult {
	return spatialanalysis.ZoneResult{ZoneID: zone.ID,
		Area: spatialanalysis.ZoneArea{SquareMeters: zone.AreaSquareM,
			InputReferences: []string{"geometry://" + zone.ID + "/" + revision}},
		Population: spatialanalysis.PopulationExposureMetric{Status: spatialanalysis.MetricUnavailable,
			Unit: spatialanalysis.PopulationUnit, Limitations: []string{"缺少真实人口图层"}},
		Roads: spatialanalysis.RoadExposureMetric{Status: spatialanalysis.MetricUnavailable,
			Unit: spatialanalysis.RoadUnit, Limitations: []string{"缺少真实道路图层"}},
		POIs: spatialanalysis.POIExposureMetric{Status: spatialanalysis.MetricUnavailable,
			Unit: spatialanalysis.POIUnit, Limitations: []string{"缺少真实设施图层"}},
		Administration: spatialanalysis.AdministrativeMatch{Status: spatialanalysis.AdminMatchUnavailable,
			Limitations: []string{"缺少真实行政边界"}}}
}

func lossProjectionFloat(value float64) *float64 { return &value }

func insertLossAnalysisHeader(t *testing.T, ctx context.Context, tx pgx.Tx,
	value spatialanalysis.Analysis,
) {
	t.Helper()
	_, err := tx.Exec(ctx, testInsertLossAnalysisSQL, value.ID, value.SnapshotID, value.Version,
		value.Area.Method, value.Status, len(value.Zones), value.Area.TotalSquareMeters, value.CalculatedAt,
		mustLossStringArrayJSON(t, value.DatasetReferences), mustLossStringArrayJSON(t, value.Area.InputReferences),
		mustLossStringArrayJSON(t, value.InputReferences), mustLossStringArrayJSON(t, value.Limitations))
	if err != nil {
		t.Fatal(err)
	}
}

func insertLossZoneResult(t *testing.T, ctx context.Context, tx pgx.Tx,
	analysis spatialanalysis.Analysis, zone spatialanalysis.ZoneResult,
) {
	t.Helper()
	exposures := map[string]any{"population": zone.Population, "roads": zone.Roads, "pois": zone.POIs}
	_, err := tx.Exec(ctx, testInsertLossZoneResultSQL, analysis.ID, analysis.SnapshotID, zone.ZoneID,
		zone.Area.SquareMeters, mustLossJSON(t, zone.Administration), mustLossJSON(t, exposures),
		mustLossStringArrayJSON(t, zone.Area.InputReferences), mustLossStringArrayJSON(t, zone.Limitations))
	if err != nil {
		t.Fatal(err)
	}
}

func insertLossExposureProjection(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshot hazard.Snapshot, analysis spatialanalysis.Analysis, zones []hazard.RiskZone,
	revision string, count int, collectedAt time.Time, complete bool, limitations ...string,
) applicationloss.LossInputProjection {
	t.Helper()
	collectedAt = postgresTime(collectedAt)
	value := newLossExposureProjection(t, snapshot, analysis, zones, revision, count, collectedAt)
	if len(limitations) > 0 {
		value.Analysis.ProjectionLimitations = append([]string{}, limitations...)
		if err := applicationloss.BindRiskProjectionIdentity(&value); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertLossProjectionHeader(t, ctx, tx, value)
	for _, zone := range value.Zones {
		if _, err = tx.Exec(ctx, testInsertLossProjectionZoneSQL, value.Analysis.ProjectionID,
			analysis.ID, zone.ID, zone.AreaSquareM, mustLossJSON(t, zone.AdminCodes)); err != nil {
			t.Fatal(err)
		}
	}
	reference := "dataset://features/" + revision
	if _, err = tx.Exec(ctx, testInsertLossFeaturesSQL, value.Analysis.ProjectionID,
		analysis.ID, 0, count-1, reference); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, testInsertLossFeatureZonesSQL, value.Analysis.ProjectionID,
		analysis.ID, 0, count-1, zones[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(zones) > 1 {
		if _, err = tx.Exec(ctx, `INSERT INTO spatial_exposure_feature_zones
			(projection_id,analysis_id,feature_id,zone_id) VALUES($1,$2,'facility-shared',$3)`,
			value.Analysis.ProjectionID, analysis.ID, zones[1].ID); err != nil {
			t.Fatal(err)
		}
	}
	if complete {
		if _, err = tx.Exec(ctx, `UPDATE spatial_exposure_projections SET complete=TRUE WHERE id=$1`,
			value.Analysis.ProjectionID); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return value
}

func newLossExposureProjection(t *testing.T, snapshot hazard.Snapshot, analysis spatialanalysis.Analysis,
	zones []hazard.RiskZone, revision string, count int, collectedAt time.Time,
) applicationloss.LossInputProjection {
	t.Helper()
	projectedZones := make([]applicationloss.LossRiskZone, len(zones))
	for index, zone := range zones {
		projectedZones[index] = applicationloss.LossRiskZone{ID: zone.ID, SnapshotID: snapshot.ID,
			Level: zone.Level, AreaSquareM: zone.AreaSquareM, AreaCalculated: true, AdminCodes: []string{"CN"}}
	}
	projection := applicationloss.LossSpatialProjection{ID: analysis.ID, Version: analysis.Version,
		Digest: strings.TrimPrefix(analysis.ID, "spatial-"), SnapshotID: snapshot.ID,
		Status:     spatialanalysis.AnalysisAvailable,
		RegionCode: "CN", TotalAreaSquareMeters: analysis.Area.TotalSquareMeters, CalculatedAt: analysis.CalculatedAt,
		ProjectionCollectedAt: collectedAt, ProjectionValidFrom: collectedAt.Add(-time.Hour),
		ProjectionValidTo: collectedAt.Add(24 * time.Hour), AdminBoundaryID: "CHN-ADM0-geoboundaries-v6",
		AdminBoundaryDigest:    strings.Repeat("d", 64),
		AdminBoundaryReference: "dataset://boundary/CN/" + revision,
		InputReferences:        analysis.InputReferences, DatasetReferences: analysis.DatasetReferences,
		Features: lossExposureFeatures(zones, revision, count)}
	value := applicationloss.LossInputProjection{Snapshot: snapshot, Zones: projectedZones, Analysis: projection}
	if err := applicationloss.BindRiskProjectionIdentity(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func lossExposureFeatures(zones []hazard.RiskZone, revision string,
	count int,
) []applicationloss.LossExposureFeature {
	result := make([]applicationloss.LossExposureFeature, 0, count)
	for item := 0; item < count; item++ {
		featureID, kind, unit := lossFeatureIdentity(item)
		zoneIDs := []string{zones[0].ID}
		if item == 0 && len(zones) > 1 {
			zoneIDs = []string{zones[0].ID, zones[1].ID}
		}
		result = append(result, applicationloss.LossExposureFeature{FeatureID: featureID, Kind: kind,
			ZoneIDs: zoneIDs, Quantity: float64(item + 1), Unit: unit, CoverageRatio: 1,
			Status: spatialanalysis.MetricAvailable, Provided: true,
			InputReferences: []string{"dataset://features/" + revision}})
	}
	return result
}

func lossFeatureIdentity(item int) (string, applicationloss.LossFeatureKind, string) {
	if item == 0 {
		return "facility-shared", applicationloss.LossFeatureFacility, "count"
	}
	if item == 1 {
		return "population-0001", applicationloss.LossFeaturePopulation, "people"
	}
	return fmt.Sprintf("road-%04d", item), applicationloss.LossFeatureRoad, "meters"
}

func insertLossProjectionHeader(t *testing.T, ctx context.Context, tx pgx.Tx,
	value applicationloss.LossInputProjection,
) {
	t.Helper()
	analysis := value.Analysis
	_, err := tx.Exec(ctx, testInsertLossProjectionHeaderSQL, analysis.ProjectionID, analysis.ID,
		analysis.ProjectionVersion, analysis.ProjectionDigest, analysis.Status, analysis.ProjectionCollectedAt,
		analysis.ProjectionValidFrom, analysis.ProjectionValidTo, analysis.RegionCode,
		analysis.TotalAreaSquareMeters, analysis.AdminBoundaryID, analysis.AdminBoundaryDigest,
		analysis.AdminBoundaryReference, false, len(value.Zones), len(analysis.Features),
		mustLossJSON(t, analysis.InputReferences), mustLossJSON(t, analysis.DatasetReferences),
		mustLossJSON(t, analysis.SourceReferenceDigests), mustLossJSON(t, analysis.ProjectionLimitations))
	if err != nil {
		t.Fatal(err)
	}
}

func mustLossJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustLossStringArrayJSON(t *testing.T, values []string) []byte {
	t.Helper()
	if values == nil {
		values = []string{}
	}
	return mustLossJSON(t, values)
}

func assertSharedLossFeature(t *testing.T, projection applicationloss.LossInputProjection,
	zones []hazard.RiskZone,
) {
	t.Helper()
	for _, feature := range projection.Analysis.Features {
		if feature.FeatureID == "facility-shared" {
			want := []string{zones[0].ID, zones[1].ID}
			if !reflect.DeepEqual(feature.ZoneIDs, want) {
				t.Fatalf("共享 feature 风险区绑定 = %v, want %v", feature.ZoneIDs, want)
			}
			return
		}
	}
	t.Fatal("损失投影缺少共享 feature")
}

func assertLossAnalysisBinding(t *testing.T, projection applicationloss.LossInputProjection,
	want spatialanalysis.Analysis,
) {
	t.Helper()
	digest := strings.TrimPrefix(want.ID, "spatial-")
	if projection.Analysis.ID != want.ID || projection.Analysis.Digest != digest ||
		projection.Stats.AnalysisID != want.ID || projection.Stats.AnalysisDigest != digest {
		t.Fatalf("空间分析 ID/digest 绑定异常: analysis=%+v stats=%+v", projection.Analysis, projection.Stats)
	}
	if projection.Analysis.RegionCode != "CN" ||
		projection.Analysis.TotalAreaSquareMeters != want.Area.TotalSquareMeters ||
		!reflect.DeepEqual(projection.Analysis.InputReferences, want.InputReferences) ||
		!reflect.DeepEqual(projection.Analysis.DatasetReferences, want.DatasetReferences) {
		t.Fatalf("空间分析区域、面积或引用绑定异常: %+v", projection.Analysis)
	}
}

func assertLossProjectionInsufficient(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshotID string, now time.Time,
) {
	t.Helper()
	_, err := repository.ReadLossInput(ctx, snapshotID, now, productionLossProjectionLimits())
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("损失投影未按数据不足 fail-closed: %v", err)
	}
}

func productionLossProjectionLimits() applicationloss.RiskProjectionLimits {
	return applicationloss.RiskProjectionLimits{MaxZones: hardMaxLossZones,
		MaxGeometryPointsPerZone: hardMaxLossGeometryPointsPerZone,
		MaxGeometryBytesPerZone:  hardMaxLossGeometryBytesPerZone,
		MaxTotalGeometryPoints:   hardMaxLossTotalGeometryPoints,
		MaxTotalGeometryBytes:    hardMaxLossTotalGeometryBytes,
		MaxSpatialJSONBytes:      hardMaxLossSpatialJSONBytes, MaxFeatures: hardMaxLossFeatures,
		MaxReferences: hardMaxLossReferences, MaxUniqueReferences: hardMaxLossUniqueReferences,
		MaxProjectionBytes:                hardMaxLossProjectionBytes,
		MaxProjectionLimitations:          hardMaxLossProjectionLimitations,
		MaxProjectionLimitationBytes:      hardMaxLossProjectionLimitationBytes,
		MaxProjectionLimitationTotalBytes: hardMaxLossProjectionLimitationTotalBytes}
}

const testInsertLossAnalysisSQL = `INSERT INTO spatial_analyses (
    id,snapshot_id,algorithm_version,area_method,status,zone_count,merged_area_square_meters,
    calculated_at,dataset_references,area_input_references,input_references,limitations
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`

const testInsertLossZoneResultSQL = `INSERT INTO spatial_zone_results (
    analysis_id,snapshot_id,zone_id,area_square_meters,admin_matches,exposures,input_references,limitations
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`

const testInsertLossProjectionHeaderSQL = `INSERT INTO spatial_exposure_projections (
    id,analysis_id,projection_version,projection_digest,projection_status,collected_at,valid_from,valid_to,
	region_code,union_area_square_meters,admin_boundary_id,admin_boundary_digest,
	admin_boundary_reference,complete,zone_count,feature_count,input_references,dataset_references,
	source_reference_digests,limitations
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`

const testInsertLossProjectionZoneSQL = `INSERT INTO spatial_exposure_projection_zones (
    projection_id,analysis_id,zone_id,area_square_meters,admin_codes
) VALUES ($1,$2,$3,$4,$5)`

const testInsertLossFeaturesSQL = `WITH generated AS (
    SELECT item,CASE WHEN item=0 THEN 'facility-shared'
        WHEN item=1 THEN 'population-0001' ELSE 'road-'||LPAD(item::TEXT,4,'0') END AS feature_id,
        CASE WHEN item=0 THEN 'facility' WHEN item=1 THEN 'population' ELSE 'road' END AS feature_kind,
        CASE WHEN item=0 THEN 'count' WHEN item=1 THEN 'people' ELSE 'meters' END AS unit
	FROM GENERATE_SERIES($3::INTEGER,$4::INTEGER) AS values(item)
) INSERT INTO spatial_exposure_features (
    projection_id,analysis_id,feature_id,feature_kind,quantity,unit,coverage_ratio,status,provided,input_references
) SELECT $1,$2,feature_id,feature_kind,(item+1)::DOUBLE PRECISION,unit,1,'available',TRUE,
    JSONB_BUILD_ARRAY($5::TEXT) FROM generated`

const testInsertLossFeatureZonesSQL = `WITH generated AS (
    SELECT CASE WHEN item=0 THEN 'facility-shared'
        WHEN item=1 THEN 'population-0001' ELSE 'road-'||LPAD(item::TEXT,4,'0') END AS feature_id
	FROM GENERATE_SERIES($3::INTEGER,$4::INTEGER) AS values(item)
) INSERT INTO spatial_exposure_feature_zones (projection_id,analysis_id,feature_id,zone_id)
    SELECT $1,$2,feature_id,$5 FROM generated`
