package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestHazardRepositoryIntegration(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := storageFixture(time.Now().UTC())
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM hazard_snapshots WHERE id=$1`, snapshot.ID)
	})
	if err := repository.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveZones(ctx, snapshot.ID, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	assertStoredHazard(t, ctx, repository, snapshot.ID)
}

func TestHazardRepositorySaveAnalysisSurvivesRepositoryRestart(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-analysis")
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	restarted := NewHazardRepository(repository.pool)
	stored, zones, err := restarted.LatestAnalysis(ctx, storageSelector(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != snapshot.ID || len(zones) != 1 || zones[0].SnapshotID != snapshot.ID {
		t.Fatalf("LatestAnalysis() = %+v, zones=%+v", stored, zones)
	}
}

func TestHazardRepositorySaveAnalysisRollsBackOnZoneFailure(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-rollback")
	cleanupSnapshot(t, repository, snapshot.ID)
	zone.Geometry.Coordinates = json.RawMessage(`"invalid-polygon"`)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err == nil {
		t.Fatal("SaveAnalysis() 未拒绝无效空间数据")
	}
	if _, err := repository.GetSnapshot(ctx, snapshot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
}

func TestHazardRepositoryTreatsZeroZonesAsCompleteAnalysis(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-empty")
	snapshot.ModelName = "test-empty"
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	stored, zones, err := repository.LatestAnalysis(ctx, storageSelector(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != snapshot.ID || len(zones) != 0 {
		t.Fatalf("LatestAnalysis() = %+v, zones=%+v", stored, zones)
	}
}

func TestHazardRepositoryAreaCalculatedRoundTrip(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, calculated := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &calculated, snapshot.ID+"-area-calculated")
	cleanupSnapshot(t, repository, snapshot.ID)
	calculated.AreaCalculated = true
	uncalculated := calculated
	uncalculated.ID += "-pending"
	uncalculated.AreaSquareM = 0
	uncalculated.AreaCalculated = false
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{calculated, uncalculated}); err != nil {
		t.Fatal(err)
	}
	zones, err := repository.ZonesBySnapshot(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 2 || !zones[0].AreaCalculated || zones[1].AreaCalculated {
		t.Fatalf("AreaCalculated 往返结果错误: %+v", zones)
	}
}

func TestHazardRepositoryLatestRiskOnlyReturnsCompleteReadableAnalysis(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	hazardType := hazard.Type("integration_latest_" + riskTypeTimestamp(now))
	oldSnapshot, _ := saveRiskFixture(t, ctx, repository, now.Add(-2*time.Hour), "old",
		hazardType, hazard.SnapshotAvailable, true)
	staleSnapshot, staleZone := saveRiskFixture(t, ctx, repository, now.Add(-time.Hour), "stale",
		hazardType, hazard.SnapshotStale, true)
	saveRiskFixture(t, ctx, repository, now.Add(time.Hour), "incomplete",
		hazardType, hazard.SnapshotAvailable, false)
	failedSnapshot, _ := saveRiskFixture(t, ctx, repository, now.Add(2*time.Hour), "failed",
		hazardType, hazard.SnapshotAvailable, true)
	if _, err := repository.pool.Exec(ctx, `UPDATE hazard_snapshots SET status='failed' WHERE id=$1`,
		failedSnapshot.ID); err != nil {
		t.Fatal(err)
	}
	stored, zones, err := repository.LatestRisk(ctx, hazardType)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != staleSnapshot.ID || stored.ID == oldSnapshot.ID {
		t.Fatalf("LatestRisk() snapshot = %+v", stored)
	}
	if len(zones) != 1 || zones[0].ID != staleZone.ID {
		t.Fatalf("LatestRisk() zones = %+v", zones)
	}
}

func TestHazardRepositoryRiskDetailRejectsIncompleteUnavailableAndMissingSnapshots(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	hazardType := hazard.Type("integration_detail_" + riskTypeTimestamp(now))
	complete, zone := saveRiskFixture(t, ctx, repository, now, "complete",
		hazardType, hazard.SnapshotAvailable, true)
	incomplete, _ := saveRiskFixture(t, ctx, repository, now.Add(time.Second), "incomplete",
		hazardType, hazard.SnapshotAvailable, false)
	failed, _ := saveRiskFixture(t, ctx, repository, now.Add(2*time.Second), "failed",
		hazardType, hazard.SnapshotAvailable, true)
	if _, err := repository.pool.Exec(ctx, `UPDATE hazard_snapshots SET status='failed' WHERE id=$1`,
		failed.ID); err != nil {
		t.Fatal(err)
	}
	stored, zones, err := repository.RiskDetail(ctx, complete.ID)
	if err != nil || stored.ID != complete.ID || len(zones) != 1 || zones[0].ID != zone.ID {
		t.Fatalf("RiskDetail() = %+v, zones=%+v, err=%v", stored, zones, err)
	}
	for _, snapshotID := range []string{incomplete.ID, failed.ID, complete.ID + "-missing"} {
		if _, _, err = repository.RiskDetail(ctx, snapshotID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("RiskDetail(%q) error = %v", snapshotID, err)
		}
	}
	empty, emptyZone := storageFixture(now.Add(3 * time.Second))
	renameStorageFixture(&empty, &emptyZone, empty.ID+"-empty")
	empty.HazardType = hazardType
	cleanupSnapshot(t, repository, empty.ID)
	if err = repository.SaveAnalysis(ctx, empty, nil); err != nil {
		t.Fatal(err)
	}
	if _, zones, err = repository.RiskDetail(ctx, empty.ID); err != nil || zones == nil || len(zones) != 0 {
		t.Fatalf("零风险区 RiskDetail() zones=%+v, err=%v", zones, err)
	}
}

func TestHazardRepositoryRiskDetailKeepsConsistentViewDuringZoneReplacement(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, oldZone := saveRiskFixture(t, ctx, repository, now, "concurrent",
		hazard.Type("integration_concurrent_"+riskTypeTimestamp(now)),
		hazard.SnapshotAvailable, true)
	newZone := oldZone
	newZone.ID = snapshot.ID + "-zone-new"
	writer, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err = writer.Exec(ctx, `LOCK TABLE risk_zones IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err = replaceZones(ctx, writer, snapshot.ID, []hazard.RiskZone{newZone}); err != nil {
		t.Fatal(err)
	}
	queryStarted := make(chan struct{}, 1)
	reader := tracedRiskRepository(t, ctx, queryStarted)
	result := make(chan riskReadResult, 1)
	go readRiskDetail(ctx, reader, snapshot.ID, result)
	waitForRiskZoneQuery(t, queryStarted)
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertConcurrentRiskRead(t, result, oldZone.ID)
	_, currentZones, err := repository.RiskDetail(ctx, snapshot.ID)
	if err != nil || len(currentZones) != 1 || currentZones[0].ID != newZone.ID {
		t.Fatalf("替换后 RiskDetail() zones=%+v, err=%v", currentZones, err)
	}
}

type riskReadResult struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
}

type zoneQueryTracer struct {
	started chan<- struct{}
}

func (t zoneQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "FROM risk_zones WHERE snapshot_id") {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	return ctx
}

func (zoneQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func saveRiskFixture(t *testing.T, ctx context.Context, repository *HazardRepository,
	runAt time.Time, suffix string, hazardType hazard.Type, status hazard.SnapshotStatus,
	complete bool,
) (hazard.Snapshot, hazard.RiskZone) {
	t.Helper()
	snapshot, zone := storageFixture(runAt)
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-"+suffix)
	snapshot.HazardType, snapshot.Status = hazardType, status
	cleanupSnapshot(t, repository, snapshot.ID)
	var err error
	if complete {
		err = repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone})
	} else {
		err = repository.SaveSnapshot(ctx, snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, zone
}

func readRiskDetail(ctx context.Context, repository *HazardRepository, snapshotID string,
	result chan<- riskReadResult,
) {
	snapshot, zones, err := repository.RiskDetail(ctx, snapshotID)
	result <- riskReadResult{snapshot: snapshot, zones: zones, err: err}
}

func riskTypeTimestamp(value time.Time) string {
	return strings.ReplaceAll(value.Format("150405.000000000"), ".", "")
}

func tracedRiskRepository(t *testing.T, ctx context.Context,
	started chan<- struct{},
) *HazardRepository {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = zoneQueryTracer{started: started}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return NewHazardRepository(pool)
}

func waitForRiskZoneQuery(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("风险区查询未进入并发窗口")
	}
}

func assertConcurrentRiskRead(t *testing.T, result <-chan riskReadResult, expectedZoneID string) {
	t.Helper()
	select {
	case value := <-result:
		if value.err != nil || value.snapshot.ID == "" || len(value.zones) != 1 ||
			value.zones[0].ID != expectedZoneID {
			t.Fatalf("并发 RiskDetail() = %+v, zones=%+v, err=%v", value.snapshot, value.zones, value.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("并发 RiskDetail() 未返回")
	}
}

func integrationHazardRepository(t *testing.T) (context.Context, *HazardRepository) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); cancel() })
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, NewHazardRepository(pool)
}

func cleanupSnapshot(t *testing.T, repository *HazardRepository, snapshotID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM hazard_snapshots WHERE id=$1`, snapshotID)
	})
}

func renameStorageFixture(snapshot *hazard.Snapshot, zone *hazard.RiskZone, id string) {
	snapshot.ID = id
	zone.ID, zone.SnapshotID = id+"-zone", id
}

func storageSelector(snapshot hazard.Snapshot) hazard.AnalysisSelector {
	return hazard.AnalysisSelector{
		HazardType: snapshot.HazardType, ModelName: snapshot.ModelName,
		TransformVersion: snapshot.Source.TransformVersion,
		Provider:         snapshot.Source.Provider, Dataset: snapshot.Source.Dataset,
	}
}

func assertStoredHazard(t *testing.T, ctx context.Context, repository *HazardRepository, snapshotID string) {
	t.Helper()
	stored, err := repository.GetSnapshot(ctx, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ModelVersion != "integration" {
		t.Fatalf("ModelVersion = %q", stored.ModelVersion)
	}
	zones, err := repository.ZonesBySnapshot(ctx, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 1 || zones[0].Geometry.Type != "Polygon" {
		t.Fatalf("ZonesBySnapshot() = %+v", zones)
	}
}

func storageFixture(now time.Time) (hazard.Snapshot, hazard.RiskZone) {
	id := "integration-" + now.Format("20060102150405.000000000")
	source := provenance.Provenance{
		Provider: "integration", Dataset: "fixture", SourceURI: "https://example.test/fixture",
		DataKind: provenance.DataKindNowcast, ObservedAt: now, FetchedAt: now,
		ValidFrom: now, ValidTo: now.Add(time.Hour), TransformVersion: "integration-transform",
	}
	snapshot := hazard.Snapshot{
		ID: id, HazardType: hazard.TypeLandslide, ModelName: "test", ModelVersion: "integration",
		RunAt: now, ValidFrom: now, ValidTo: now.Add(time.Hour), RasterReference: "fixture.tif",
		ProbabilitySemantics: "测试概率", Thresholds: []hazard.RiskThreshold{
			{Level: hazard.RiskLow, Minimum: 0, Maximum: 1},
		}, Status: hazard.SnapshotAvailable, Source: source, Limitations: []string{"仅用于测试"},
	}
	coordinates := json.RawMessage(`[[[116.0,39.0],[116.1,39.0],[116.1,39.1],[116.0,39.0]]]`)
	zone := hazard.RiskZone{
		ID: id + "-zone", SnapshotID: id, Geometry: spatial.Geometry{Type: "Polygon", Coordinates: coordinates},
		Minimum: 0.1, Mean: 0.2, Maximum: 0.3, Level: hazard.RiskModerate,
		AreaSquareM: 100, InputReferences: []string{"fixture.tif"}, Limitations: []string{"仅用于测试"},
	}
	return snapshot, zone
}
