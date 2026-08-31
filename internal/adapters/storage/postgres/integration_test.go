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

func TestHazardRepositoryCoverageRoundTripAndImmutability(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, zone := storageFixture(now)
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-coverage")
	snapshot.Coverage = storageCoverage(now)
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	stored, err := NewHazardRepository(repository.pool).GetSnapshot(ctx, snapshot.ID)
	if err != nil || stored.Coverage == nil || stored.Coverage.Identity() != snapshot.Coverage.Identity() {
		t.Fatalf("GetSnapshot()=%+v error=%v", stored, err)
	}
	var isNull bool
	if err = repository.pool.QueryRow(ctx,
		`SELECT coverage IS NULL FROM hazard_snapshots WHERE id=$1`, snapshot.ID).Scan(&isNull); err != nil || isNull {
		t.Fatalf("coverage SQL NULL=%v error=%v", isNull, err)
	}
	changed := snapshot
	changed.Coverage = storageCoverage(now)
	changed.Coverage.SHA256 = strings.Repeat("b", 64)
	if err = repository.SaveAnalysis(ctx, changed,
		[]hazard.RiskZone{zone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同 ID 覆盖范围变化未拒绝: %v", err)
	}
}

func TestHazardRepositoryLegacyCoverageRemainsSQLNullAndIdempotent(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-legacy-coverage")
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := repository.pool.QueryRow(ctx,
		`SELECT coverage IS NULL FROM hazard_snapshots WHERE id=$1`, snapshot.ID).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("legacy coverage SQL NULL=%v error=%v", isNull, err)
	}
	stored, err := NewHazardRepository(repository.pool).GetSnapshot(ctx, snapshot.ID)
	if err != nil || stored.Coverage != nil {
		t.Fatalf("GetSnapshot()=%+v error=%v", stored, err)
	}
	if err = repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatalf("旧式空覆盖范围未保持幂等: %v", err)
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

func TestHazardRepositoryReconcilesAllAnalysesThroughAuditWaterline(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_superseded_" + riskTypeTimestamp(now))
	older, olderZone := saveSupersessionAnalysis(t, ctx, repository, now.Add(-time.Minute), "older", hazardType, "a")
	latest, latestZone := saveSupersessionAnalysis(t, ctx, repository, now, "latest", hazardType, "a")
	replacement := supersessionCoverage(now, "b")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(latest), replacement, now); err != nil {
		t.Fatal(err)
	}
	assertSupersededSnapshotHiddenFromLatest(t, ctx, repository, latest)
	for _, item := range []struct {
		snapshot hazard.Snapshot
		zone     hazard.RiskZone
	}{{older, olderZone}, {latest, latestZone}} {
		assertSupersededHistoryReadable(t, ctx, repository, item.snapshot, item.zone)
		assertSnapshotSuperseded(t, ctx, repository, item.snapshot.ID, true, replacement.SHA256)
	}
}

func TestHazardRepositoryGlobalLatestRejectsLegacyFallbackAfterCoverageChange(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_legacy_fallback_" + riskTypeTimestamp(now))
	legacy, legacyZone := storageFixture(now.Add(-2 * time.Hour))
	renameStorageFixture(&legacy, &legacyZone, legacy.ID+"-legacy-v1")
	legacy.HazardType, legacy.Source.TransformVersion = hazardType, "lhasa-gdal-1-gdal-3.13.3"
	cleanupSnapshot(t, repository, legacy.ID)
	if err := repository.SaveAnalysis(ctx, legacy, []hazard.RiskZone{legacyZone}); err != nil {
		t.Fatal(err)
	}

	current, currentZone := storageFixture(now.Add(-time.Hour))
	renameStorageFixture(&current, &currentZone, current.ID+"-current-v2")
	current.HazardType = hazardType
	current.Source.TransformVersion = "lhasa-gdal-2-gdal-3.13.3-china-adm0"
	current.Coverage = storageCoverage(current.RunAt)
	cleanupSnapshot(t, repository, current.ID)
	if err := repository.SaveAnalysis(ctx, current, []hazard.RiskZone{currentZone}); err != nil {
		t.Fatal(err)
	}
	replacement := supersessionCoverage(now, "b")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(current), replacement, now); err != nil {
		t.Fatal(err)
	}

	assertSupersededSnapshotHiddenFromLatest(t, ctx, repository, current)
	assertSnapshotSuperseded(t, ctx, repository, legacy.ID, false, "")
	stored, zones, err := repository.LatestAnalysis(ctx, storageSelector(legacy))
	if err != nil || stored.ID != legacy.ID || len(zones) != 1 || zones[0].ID != legacyZone.ID {
		t.Fatalf("旧转换版本审计读取错误: snapshot=%+v zones=%+v err=%v", stored, zones, err)
	}
}

func TestHazardRepositoryCoverageReconciliationIsIdempotent(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_supersession_retry_" + riskTypeTimestamp(now))
	snapshot, _ := saveSupersessionAnalysis(t, ctx, repository, now, "anchor", hazardType, "a")
	replacements := []hazard.Coverage{supersessionCoverage(now.Add(-time.Minute), "b"),
		supersessionCoverage(now, "b")}
	for _, replacement := range replacements {
		if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(snapshot), replacement, now); err != nil {
			t.Fatalf("同一边界身份重复协调失败: %v", err)
		}
	}
	assertSnapshotSuperseded(t, ctx, repository, snapshot.ID, true, replacements[0].SHA256)
}

func TestHazardRepositoryCoverageReconciliationDoesNotCrossSelector(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_supersession_scope_" + riskTypeTimestamp(now))
	selected, _ := saveSupersessionAnalysis(t, ctx, repository, now.Add(-time.Minute), "selected", hazardType, "a")
	otherType := hazard.Type(string(hazardType) + "_other")
	other, _ := saveSupersessionAnalysis(t, ctx, repository, now, "other", otherType, "a")
	replacement := supersessionCoverage(now, "b")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(selected), replacement, now); err != nil {
		t.Fatal(err)
	}
	stored, _, err := repository.LatestAnalysis(ctx, storageSelector(other))
	if err != nil || stored.ID != other.ID {
		t.Fatalf("其他分析族被误伤: snapshot=%+v err=%v", stored, err)
	}
	assertSnapshotSuperseded(t, ctx, repository, selected.ID, true, replacement.SHA256)
	assertSnapshotSuperseded(t, ctx, repository, other.ID, false, "")
}

func TestHazardRepositorySupersessionReactivatesMatchingCoverage(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_supersession_return_" + riskTypeTimestamp(now))
	oldB, _ := saveSupersessionAnalysis(t, ctx, repository, now.Add(-2*time.Minute), "old-b", hazardType, "b")
	coverageA := supersessionCoverage(now.Add(-time.Minute), "a")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(oldB),
		coverageA, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	latestA, _ := saveSupersessionAnalysis(t, ctx, repository, now.Add(-time.Minute), "latest-a", hazardType, "a")
	coverageB := supersessionCoverage(now, "b")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(latestA), coverageB, now); err != nil {
		t.Fatal(err)
	}
	stored, _, err := repository.LatestAnalysis(ctx, storageSelector(latestA))
	if err != nil || stored.ID != oldB.ID {
		t.Fatalf("回切边界后未恢复匹配快照: snapshot=%+v err=%v", stored, err)
	}
	assertSnapshotSuperseded(t, ctx, repository, oldB.ID, false, "")
	assertSnapshotSuperseded(t, ctx, repository, latestA.ID, true, coverageB.SHA256)
}

func TestHazardRepositoryCoverageReconciliationRecoversWithoutVisibleLatest(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	hazardType := hazard.Type("integration_supersession_hidden_" + riskTypeTimestamp(now))
	hiddenB, _ := saveSupersessionAnalysis(t, ctx, repository, now, "hidden-b", hazardType, "b")
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(hiddenB),
		supersessionCoverage(now, "a"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.LatestAnalysis(ctx, storageSelector(hiddenB)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("旧边界快照未隐藏: %v", err)
	}
	if err := repository.ReconcileAnalysisCoverage(ctx, storageSelector(hiddenB),
		supersessionCoverage(now, "b"), now); err != nil {
		t.Fatal(err)
	}
	stored, _, err := repository.LatestAnalysis(ctx, storageSelector(hiddenB))
	if err != nil || stored.ID != hiddenB.ID {
		t.Fatalf("无可见 latest 时未恢复匹配边界: snapshot=%+v err=%v", stored, err)
	}
}

func TestHazardRepositoryAnalysisRefreshLockSerializesSelector(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, _ := storageFixture(time.Now().UTC())
	selector := storageSelector(snapshot)
	first, err := repository.LockAnalysisRefresh(ctx, selector)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	acquired, releaseSecond := make(chan error, 1), make(chan struct{})
	go func() {
		second, lockErr := repository.LockAnalysisRefresh(waitCtx, selector)
		acquired <- lockErr
		if lockErr == nil {
			<-releaseSecond
			_ = second.Release()
		}
	}()
	select {
	case lockErr := <-acquired:
		t.Fatalf("第二个刷新未被串行化: %v", lockErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err = first.Release(); err != nil {
		t.Fatal(err)
	}
	if err = <-acquired; err != nil {
		t.Fatalf("释放首个锁后第二个刷新未获取锁: %v", err)
	}
	close(releaseSecond)
}

func TestHazardRepositoryAnalysisRefreshLockKeepsSingleConnectionPoolUsable(t *testing.T) {
	ctx, repository := integrationHazardRepositoryWithMaxConns(t, 1)
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-single-connection")
	cleanupSnapshot(t, repository, snapshot.ID)

	lease, err := repository.LockAnalysisRefresh(ctx, storageSelector(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err = repository.SaveAnalysis(operationCtx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatalf("持有刷新锁时单连接池无法保存分析: %v", err)
	}
	stored, zones, err := repository.LatestAnalysis(operationCtx, storageSelector(snapshot))
	if err != nil {
		t.Fatalf("持有刷新锁时单连接池无法读取分析: %v", err)
	}
	if stored.ID != snapshot.ID || len(zones) != 1 || zones[0].ID != zone.ID {
		t.Fatalf("单连接池分析往返不完整: snapshot=%+v zones=%+v", stored, zones)
	}
	if err = lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func assertSupersededSnapshotHiddenFromLatest(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshot hazard.Snapshot,
) {
	t.Helper()
	if _, err := repository.Latest(ctx, snapshot.HazardType); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Latest() 未隐藏已取代快照: %v", err)
	}
	if _, _, err := repository.LatestRisk(ctx, snapshot.HazardType); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestRisk() 未隐藏已取代快照: %v", err)
	}
	if _, err := repository.LatestMapRisk(ctx, snapshot.HazardType, 100); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestMapRisk() 未隐藏已取代快照: %v", err)
	}
	if _, _, err := repository.LatestAnalysis(ctx, storageSelector(snapshot)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestAnalysis() 未隐藏已取代快照: %v", err)
	}
}

func saveSupersessionAnalysis(t *testing.T, ctx context.Context, repository *HazardRepository,
	runAt time.Time, suffix string, hazardType hazard.Type, coverageDigest string,
) (hazard.Snapshot, hazard.RiskZone) {
	t.Helper()
	snapshot, zone := storageFixture(runAt)
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-"+suffix)
	snapshot.HazardType = hazardType
	snapshot.Coverage = storageCoverage(runAt)
	snapshot.Coverage.SHA256 = strings.Repeat(coverageDigest, 64)
	cleanupSnapshot(t, repository, snapshot.ID)
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	return snapshot, zone
}

func supersessionCoverage(collectedAt time.Time, digest string) hazard.Coverage {
	value := *storageCoverage(collectedAt.Add(-time.Minute))
	value.SHA256 = strings.Repeat(digest, 64)
	return value
}

func assertSupersededHistoryReadable(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshot hazard.Snapshot, zone hazard.RiskZone,
) {
	t.Helper()
	stored, zones, err := repository.RiskDetail(ctx, snapshot.ID)
	if err != nil || stored.ID != snapshot.ID || len(zones) != 1 || zones[0].ID != zone.ID {
		t.Fatalf("已取代快照历史详情丢失: snapshot=%+v zones=%+v err=%v", stored, zones, err)
	}
	stored, err = repository.GetSnapshot(ctx, snapshot.ID)
	if err != nil || stored.Coverage == nil || stored.Coverage.Identity() != snapshot.Coverage.Identity() {
		t.Fatalf("已取代快照审计读取错误: snapshot=%+v err=%v", stored, err)
	}
}

func assertSnapshotSuperseded(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshotID string, expected bool, expectedSHA string,
) {
	t.Helper()
	var superseded bool
	var replacementSHA string
	err := repository.pool.QueryRow(ctx, `SELECT superseded_at IS NOT NULL,
		COALESCE(superseded_by_coverage->>'sha256','') FROM hazard_snapshots WHERE id=$1`, snapshotID).
		Scan(&superseded, &replacementSHA)
	if err != nil || superseded != expected || replacementSHA != expectedSHA {
		t.Fatalf("覆盖范围失效状态错误: id=%s superseded=%v sha=%v err=%v",
			snapshotID, superseded, replacementSHA, err)
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
	snapshot.Coverage = storageCoverage(runAt)
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
	return integrationHazardRepositoryWithMaxConns(t, 0)
}

func integrationHazardRepositoryWithMaxConns(t *testing.T, maxConns int32) (context.Context, *HazardRepository) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); cancel() })
	if err = pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
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

func storageCoverage(now time.Time) *hazard.Coverage {
	return &hazard.Coverage{
		Mode: hazard.CoverageAdministrativeBoundary, RegionCode: "CN",
		BoundaryID: "CHN-ADM0-351020", BoundaryType: "ADM0", BoundaryVersion: "2019",
		Source: "geoBoundaries", License: "Public Domain",
		Reference: "https://example.test/china.geojson", SHA256: strings.Repeat("a", 64),
		GeometrySHA256: strings.Repeat("c", 64),
		CollectedAt:    now.Add(-time.Hour),
	}
}
