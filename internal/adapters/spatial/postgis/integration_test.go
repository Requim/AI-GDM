package postgis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	storagepg "github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestExecutorCalculatesMergedAreaAndPersistsResults(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	snapshot := spatialSnapshot(now, uniqueID("overlap"))
	zones := []hazard.RiskZone{
		spatialZone(snapshot.ID, "zone-a", polygon(0, 0, 0.02, 0.02)),
		spatialZone(snapshot.ID, "zone-b", polygon(0.01, 0, 0.03, 0.02)),
	}
	cleanupSpatialSnapshot(t, pool, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, zones); err != nil {
		t.Fatal(err)
	}
	first, err := executor.Execute(ctx, snapshot.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	assertAreaOnlyAnalysis(t, first, 2)
	sum := first.Zones[0].Area.SquareMeters + first.Zones[1].Area.SquareMeters
	if first.Area.TotalSquareMeters >= sum || first.Area.TotalSquareMeters <= first.Zones[0].Area.SquareMeters {
		t.Fatalf("重叠风险区合并面积未去重: total=%f sum=%f", first.Area.TotalSquareMeters, sum)
	}
	assertCalculatedZones(t, ctx, repository, snapshot.ID)
	assertSpatialRoundTrip(t, ctx, executor, first)
	second, err := executor.Execute(ctx, snapshot.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || !second.CalculatedAt.Equal(first.CalculatedAt) {
		t.Fatalf("相同输入未幂等复用: first=%+v second=%+v", first, second)
	}
}

func TestExecutorSupportsMultiPolygonAndZeroZones(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	multiSnapshot := spatialSnapshot(now, uniqueID("multi"))
	multiZone := spatialZone(multiSnapshot.ID, "zone-multi", multiPolygon())
	multiZone.Geometry.Type = "MultiPolygon"
	cleanupSpatialSnapshot(t, pool, multiSnapshot.ID)
	if err := repository.SaveAnalysis(ctx, multiSnapshot, []hazard.RiskZone{multiZone}); err != nil {
		t.Fatal(err)
	}
	multi, err := executor.Execute(ctx, multiSnapshot.ID, now)
	if err != nil || len(multi.Zones) != 1 || multi.Area.TotalSquareMeters <= 0 {
		t.Fatalf("MultiPolygon 分析 = %+v, err=%v", multi, err)
	}
	emptySnapshot := spatialSnapshot(now.Add(time.Second), uniqueID("empty"))
	cleanupSpatialSnapshot(t, pool, emptySnapshot.ID)
	if err = repository.SaveAnalysis(ctx, emptySnapshot, nil); err != nil {
		t.Fatal(err)
	}
	empty, err := executor.Execute(ctx, emptySnapshot.ID, now)
	if err != nil || empty.Status != spatialanalysis.AnalysisAreaOnly || len(empty.Zones) != 0 ||
		empty.Area.TotalSquareMeters != 0 {
		t.Fatalf("零风险区分析 = %+v, err=%v", empty, err)
	}
}

func TestExecutorRejectsNonPolygonWithoutSaving(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	snapshot := spatialSnapshot(now, uniqueID("line"))
	zone := spatialZone(snapshot.ID, "zone-line", json.RawMessage(`[[0,0],[0.01,0.01]]`))
	zone.Geometry.Type = "LineString"
	cleanupSpatialSnapshot(t, pool, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, snapshot.ID, now); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Execute() error = %v", err)
	}
	assertNoSpatialAnalysis(t, ctx, pool, snapshot.ID)
}

func TestExecutorRollsBackOnZoneResultFailure(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	snapshot := spatialSnapshot(now, uniqueID("rollback"))
	zone := spatialZone(snapshot.ID, "zone-rollback", polygon(1, 1, 1.01, 1.01))
	cleanupSpatialSnapshot(t, pool, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	installFailureTrigger(t, ctx, pool)
	if _, err := executor.Execute(ctx, snapshot.ID, now); err == nil {
		t.Fatal("Execute() 未返回测试触发器错误")
	}
	assertNoSpatialAnalysis(t, ctx, pool, snapshot.ID)
	stored, err := repository.ZonesBySnapshot(ctx, snapshot.ID)
	if err != nil || len(stored) != 1 || stored[0].AreaCalculated {
		t.Fatalf("失败事务修改了风险区面积: %+v, err=%v", stored, err)
	}
}

func TestReplacingRiskZonesInvalidatesPreviousSpatialAnalysis(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	snapshot := spatialSnapshot(now, uniqueID("invalidate"))
	firstZone := spatialZone(snapshot.ID, "zone-first", polygon(2, 2, 2.01, 2.01))
	cleanupSpatialSnapshot(t, pool, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{firstZone}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, snapshot.ID, now); err != nil {
		t.Fatal(err)
	}
	secondZone := spatialZone(snapshot.ID, "zone-second", polygon(3, 3, 3.01, 3.01))
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{secondZone}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.LatestBySnapshot(ctx, snapshot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestBySnapshot() error = %v", err)
	}
}

func TestConsistentReadSurvivesConcurrentRiskZoneReplacement(t *testing.T) {
	ctx, executor, repository, pool := integrationSpatialExecutor(t)
	now := time.Now().UTC()
	snapshot := spatialSnapshot(now, uniqueID("consistent-read"))
	firstZone := spatialZone(snapshot.ID, "zone-first", polygon(4, 4, 4.01, 4.01))
	cleanupSpatialSnapshot(t, pool, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{firstZone}); err != nil {
		t.Fatal(err)
	}
	analysis, err := executor.Execute(ctx, snapshot.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	assertRepeatableReadDuringReplacement(t, ctx, executor, repository, snapshot, analysis.ID)
}

func integrationSpatialExecutor(t *testing.T) (context.Context, *Executor,
	*storagepg.HazardRepository, *pgxpool.Pool,
) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	pool, err := storagepg.Open(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); cancel() })
	if err = storagepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, New(pool), storagepg.NewHazardRepository(pool), pool
}

func spatialSnapshot(now time.Time, id string) hazard.Snapshot {
	return hazard.Snapshot{
		ID: id, HazardType: hazard.TypeLandslide, ModelName: "spatial-test", ModelVersion: "v1",
		RunAt: now, ValidFrom: now, ValidTo: now.Add(time.Hour), RasterReference: "fixture.tif",
		ProbabilitySemantics: "测试概率", Status: hazard.SnapshotAvailable,
		Thresholds: []hazard.RiskThreshold{
			{Level: hazard.RiskLow, Minimum: 0, Maximum: 1, Description: "测试阈值"},
		},
		Source: provenance.Provenance{
			Provider: "integration", Dataset: "spatial-fixture", SourceURI: "https://example.test/spatial",
			DataKind: provenance.DataKindNowcast, ObservedAt: now, FetchedAt: now,
			ValidFrom: now, ValidTo: now.Add(time.Hour), TransformVersion: "spatial-test-v1",
		},
		Limitations: []string{"仅用于集成测试"},
	}
}

func spatialZone(snapshotID, id string, coordinates json.RawMessage) hazard.RiskZone {
	return hazard.RiskZone{
		ID: snapshotID + "-" + id, SnapshotID: snapshotID,
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: coordinates},
		Minimum:  0.2, Mean: 0.3, Maximum: 0.4, Level: hazard.RiskModerate,
		InputReferences: []string{"fixture.tif"}, Limitations: []string{"仅用于集成测试"},
	}
}

func polygon(minX, minY, maxX, maxY float64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`[[[%f,%f],[%f,%f],[%f,%f],[%f,%f],[%f,%f]]]`,
		minX, minY, maxX, minY, maxX, maxY, minX, maxY, minX, minY))
}

func multiPolygon() json.RawMessage {
	return json.RawMessage(`[[[[0,0],[0.01,0],[0.01,0.01],[0,0.01],[0,0]]],` +
		`[[[0.02,0],[0.03,0],[0.03,0.01],[0.02,0.01],[0.02,0]]]]`)
}

func uniqueID(prefix string) string {
	return fmt.Sprintf("spatial-%s-%d", prefix, time.Now().UnixNano())
}

func cleanupSpatialSnapshot(t *testing.T, pool *pgxpool.Pool,
	snapshotID string,
) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hazard_snapshots WHERE id=$1`, snapshotID)
	})
}

func assertAreaOnlyAnalysis(t *testing.T, value spatialanalysis.Analysis, zones int) {
	t.Helper()
	if value.Status != spatialanalysis.AnalysisAreaOnly || len(value.Zones) != zones || value.ID == "" {
		t.Fatalf("空间分析状态错误: %+v", value)
	}
	for _, zone := range value.Zones {
		if zone.Population.Quantity != nil || zone.Roads.Quantity != nil || zone.POIs.Quantity != nil ||
			zone.Administration.CoverageRatio != nil {
			t.Fatalf("缺失图层被编码成零值: %+v", zone)
		}
	}
}

func assertCalculatedZones(t *testing.T, ctx context.Context,
	repository *storagepg.HazardRepository, snapshotID string,
) {
	t.Helper()
	zones, err := repository.ZonesBySnapshot(ctx, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range zones {
		if !zone.AreaCalculated || zone.AreaSquareM <= 0 {
			t.Fatalf("风险区面积未持久化: %+v", zone)
		}
	}
}

func assertSpatialRoundTrip(t *testing.T, ctx context.Context, executor *Executor,
	want spatialanalysis.Analysis,
) {
	t.Helper()
	got, err := executor.Get(ctx, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() = %+v, err=%v, want=%+v", got, err, want)
	}
	latest, err := executor.LatestBySnapshot(ctx, want.SnapshotID)
	if err != nil || !reflect.DeepEqual(latest, want) {
		t.Fatalf("LatestBySnapshot() = %+v, err=%v", latest, err)
	}
}

func assertNoSpatialAnalysis(t *testing.T, ctx context.Context,
	pool *pgxpool.Pool, snapshotID string,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM spatial_analyses WHERE snapshot_id=$1`, snapshotID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("失败分析留下 %d 条头记录", count)
	}
}

func installFailureTrigger(t *testing.T, ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	const setup = `CREATE OR REPLACE FUNCTION test_fail_spatial_zone_insert()
RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced spatial failure'; END $$;
DROP TRIGGER IF EXISTS test_fail_spatial_zone_insert ON spatial_zone_results;
CREATE TRIGGER test_fail_spatial_zone_insert BEFORE INSERT OR UPDATE ON spatial_zone_results
FOR EACH ROW EXECUTE FUNCTION test_fail_spatial_zone_insert();`
	if _, err := pool.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DROP TRIGGER IF EXISTS test_fail_spatial_zone_insert ON spatial_zone_results;
             DROP FUNCTION IF EXISTS test_fail_spatial_zone_insert();`)
	})
}

func assertRepeatableReadDuringReplacement(t *testing.T, ctx context.Context, executor *Executor,
	repository *storagepg.HazardRepository, snapshot hazard.Snapshot, analysisID string,
) {
	t.Helper()
	_, err := executor.readConsistently(ctx, func(tx pgx.Tx) (spatialanalysis.Analysis, error) {
		var before int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_zone_results
            WHERE analysis_id=$1`, analysisID).Scan(&before); err != nil {
			return spatialanalysis.Analysis{}, err
		}
		replacement := spatialZone(snapshot.ID, "zone-replacement", polygon(5, 5, 5.01, 5.01))
		if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{replacement}); err != nil {
			return spatialanalysis.Analysis{}, err
		}
		var after int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_zone_results
            WHERE analysis_id=$1`, analysisID).Scan(&after); err != nil {
			return spatialanalysis.Analysis{}, err
		}
		if before != 1 || after != before {
			return spatialanalysis.Analysis{}, fmt.Errorf("一致性快照前后区级结果数量不同: %d/%d", before, after)
		}
		return spatialanalysis.Analysis{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
