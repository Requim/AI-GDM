package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

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
