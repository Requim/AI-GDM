package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestLatestHazardWriterReplacesOnlyAfterSuccess(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	old, oldZone := storageFixture(now)
	old.ModelName = old.ID + "-retention"
	old.Coverage = storageCoverage(now)
	cleanupSnapshot(t, repository, old.ID)
	if err := repository.SaveAnalysis(ctx, old, []hazard.RiskZone{oldZone}); err != nil {
		t.Fatal(err)
	}
	next, nextZone := storageFixture(now.Add(time.Minute))
	next.ModelName, next.Coverage = old.ModelName, storageCoverage(now)
	cleanupSnapshot(t, repository, next.ID)
	writer := NewLatestHazardWriter(repository)
	bad := nextZone
	bad.ID = oldZone.ID
	if err := writer.SaveAnalysis(ctx, next, []hazard.RiskZone{bad}); err == nil {
		t.Fatal("未触发入库失败")
	}
	if _, err := repository.GetSnapshot(ctx, old.ID); err != nil {
		t.Fatalf("失败删除了旧快照: %v", err)
	}
	if _, err := repository.GetSnapshot(ctx, next.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("半成品可见: %v", err)
	}
	if err := writer.SaveAnalysis(ctx, next, []hazard.RiskZone{nextZone}); err != nil {
		t.Fatal(err)
	}
	assertRetainedSnapshot(t, ctx, repository, old.ID, next.ID)
}

func TestLatestHazardWriterRejectsOlderSource(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	current, zone := storageFixture(now)
	current.ModelName = current.ID + "-ordering"
	cleanupSnapshot(t, repository, current.ID)
	if err := repository.SaveAnalysis(ctx, current, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	older, olderZone := storageFixture(now.Add(-time.Hour))
	older.ModelName = current.ModelName
	cleanupSnapshot(t, repository, older.ID)
	err := NewLatestHazardWriter(repository).SaveAnalysis(ctx, older, []hazard.RiskZone{olderZone})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("未拒绝倒退: %v", err)
	}
	if _, err = repository.GetSnapshot(ctx, current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.GetSnapshot(ctx, older.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("旧版本残留")
	}
}

func assertRetainedSnapshot(t *testing.T, ctx context.Context, repository *HazardRepository, old, next string) {
	t.Helper()
	if _, err := repository.GetSnapshot(ctx, old); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("旧快照未清理: %v", err)
	}
	if _, err := repository.GetSnapshot(ctx, next); err != nil {
		t.Fatal(err)
	}
	zones, err := repository.ZonesBySnapshot(ctx, old)
	if err != nil || len(zones) != 0 {
		t.Fatalf("旧风险区残留: %v %v", zones, err)
	}
}

func TestLatestHazardWriterCascadesCompletedExposure(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	old, _ := storageFixture(now.Add(-10 * time.Minute))
	old.ModelName = old.ID + "-exposure-retention"
	cleanupSnapshot(t, repository, old.ID)
	_, zone := storageFixture(now)
	zone.ID, zone.SnapshotID, zone.AreaCalculated = old.ID+"-zone", old.ID, true
	zones := []hazard.RiskZone{zone}
	if err := repository.SaveAnalysis(ctx, old, zones); err != nil {
		t.Fatal(err)
	}
	analysis := insertLossSpatialAnalysis(t, ctx, repository, old, zones, now.Add(-5*time.Minute), "retention", false)
	value := exposureProjectionFixture(t, old, analysis, zones, "retention", now.Add(-time.Minute))
	if err := repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `DELETE FROM spatial_exposure_projections WHERE id=$1`,
		value.Input.Analysis.ProjectionID); err == nil {
		t.Fatal("直接删除完整投影未受保护")
	}
	next, nextZone := storageFixture(now)
	next.ModelName = old.ModelName
	cleanupSnapshot(t, repository, next.ID)
	if err := NewLatestHazardWriter(repository).SaveAnalysis(ctx, next, []hazard.RiskZone{nextZone}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.pool.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_exposure_projections WHERE analysis_id=$1`,
		analysis.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("投影未级联清理: %d %v", count, err)
	}
	assertRetainedSnapshot(t, ctx, repository, old.ID, next.ID)
}
