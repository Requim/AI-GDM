package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestHazardRepositoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	repository := NewHazardRepository(pool)
	snapshot, zone := storageFixture(time.Now().UTC())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hazard_snapshots WHERE id=$1`, snapshot.ID)
	})
	if err = repository.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err = repository.SaveZones(ctx, snapshot.ID, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	assertStoredHazard(t, ctx, repository, snapshot.ID)
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
		DataKind: provenance.DataKindNowcast, FetchedAt: now,
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
