package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestRiskAssessmentMigrationAndSQLContracts(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/007_risk_assessments.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(content))
	for _, fragment := range []string{
		"create table risk_assessments", "snapshot_id text primary key",
		"references hazard_snapshots(id) on delete restrict",
		"assessment_id text not null unique", "snapshot jsonb not null",
		"snapshot_digest text not null", "assessment jsonb not null",
		"assessment_digest text not null",
		"assessment->>'id' = assessment_id", "assessment->>'snapshotid' = snapshot_id",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("风险评估迁移缺少契约片段 %q", fragment)
		}
	}
	if strings.Contains(sql, "on delete cascade") {
		t.Fatal("风险评估外键不得级联删除")
	}
	if !strings.Contains(insertRiskAssessmentSQL, "ON CONFLICT (snapshot_id)") ||
		!strings.Contains(insertRiskAssessmentSQL, "risk_assessments.snapshot=EXCLUDED.snapshot") ||
		!strings.Contains(insertRiskAssessmentSQL, "risk_assessments.assessment=EXCLUDED.assessment") ||
		!strings.Contains(insertRiskAssessmentSQL,
			"RETURNING snapshot,snapshot_digest,assessment,assessment_digest") {
		t.Fatalf("风险评估幂等 SQL 缺少冲突比较或 RETURNING: %s", insertRiskAssessmentSQL)
	}
}

func TestHazardAuthorityQueriesAreBoundedAndGeometryFree(t *testing.T) {
	countsQuery := strings.ToLower(selectAuthorityCountsSQL)
	zonesQuery := strings.ToLower(selectAuthorityZonesSQL)
	for _, fragment := range []string{"count(*)", "st_npoints", "octet_length", "st_asewkb"} {
		if !strings.Contains(countsQuery, fragment) {
			t.Errorf("权威负载统计缺少 %q", fragment)
		}
	}
	if strings.Contains(zonesQuery, "geometry") || strings.Contains(zonesQuery, "st_as") ||
		!strings.Contains(zonesQuery, "limit $2") {
		t.Fatalf("权威区摘要查询包含几何或缺少 LIMIT: %s", selectAuthorityZonesSQL)
	}
	queryer := &authorityCountQueryer{row: tripleIntegerRow{100_001, 200_000, 8 << 20}}
	_, err := readAuthorityCounts(context.Background(), queryer, "snapshot-1",
		ports.HazardAuthorityLimits{MaxZones: 1_000, MaxGeometryPoints: 200_000, MaxGeometryBytes: 8 << 20})
	if !errors.Is(err, domain.ErrInsufficientData) || queryer.queryCalled {
		t.Fatalf("超过数量上限未在明细查询前拒绝: called=%v err=%v", queryer.queryCalled, err)
	}
}

func TestHazardAuthorityRepositoryIntegration(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	if _, err := repository.ReadAuthority(ctx, snapshot.ID, ports.HazardAuthorityLimits{
		MaxZones: 10, MaxGeometryPoints: 1_000, MaxGeometryBytes: 1 << 20,
	}); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("缺少固化评估未按服务端数据不足拒绝: %v", err)
	}
	assessment := authorityRiskAssessment(snapshot, zone, "primary")
	assertConcurrentSameAssessment(t, ctx, repository, snapshot, assessment)
	read, err := repository.ReadAuthority(ctx, snapshot.ID, ports.HazardAuthorityLimits{
		MaxZones: 10, MaxGeometryPoints: 1_000, MaxGeometryBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if read.Assessment.ID != assessment.ID || len(read.Zones) != 1 ||
		read.Zones[0].ID != zone.ID || read.TotalGeometryPoints <= 0 || read.TotalGeometryBytes <= 0 {
		t.Fatalf("风险权威无几何读取结果错误: %+v", read)
	}
	conflict := assessment
	conflict.ID += "-conflict"
	if err = repository.SaveRiskAssessment(ctx, snapshot, conflict); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同快照不同评估未被拒绝: %v", err)
	}
	if err = repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatalf("相同快照和风险区未幂等复用: %v", err)
	}
	reused, err := repository.ReuseRiskAuthority(ctx, snapshot, []hazard.RiskZone{zone})
	if err != nil || !reused {
		t.Fatalf("完整相同权威未在空间重算前复用: reused=%v err=%v", reused, err)
	}
}

func TestRiskAuthorityRepeatedArtifactKeepsAuthorityStable(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	assessment := authorityRiskAssessment(snapshot, zone, "stable")
	if err := repository.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		t.Fatal(err)
	}
	before := readAuthorityFixture(t, ctx, repository, snapshot.ID)
	upstreamZone := zone
	upstreamZone.AreaSquareM, upstreamZone.AreaCalculated = 0, false
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{upstreamZone}); err != nil {
		t.Fatalf("相同 artifact 重复保存失败: %v", err)
	}
	reused, err := repository.ReuseRiskAuthority(ctx, snapshot, []hazard.RiskZone{upstreamZone})
	if err != nil || !reused {
		t.Fatalf("相同 artifact 未短路重算: reused=%v err=%v", reused, err)
	}
	after := readAuthorityFixture(t, ctx, repository, snapshot.ID)
	beforeJSON, beforeDigest, err := payloadDigest(before)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, afterDigest, err := payloadDigest(after)
	if err != nil || beforeDigest != afterDigest || string(beforeJSON) != string(afterJSON) {
		t.Fatalf("重复 artifact 改变权威内容: before=%s after=%s err=%v",
			beforeDigest, afterDigest, err)
	}
}

func TestRiskAuthorityBindsCoverageIntoImmutableSnapshot(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC()
	snapshot, zone := storageFixture(now)
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-authority-coverage")
	snapshot.Coverage = storageCoverage(now)
	cleanupSnapshot(t, repository, snapshot.ID)
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(),
			`DELETE FROM risk_assessments WHERE snapshot_id=$1`, snapshot.ID)
	})
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	persistAuthoritySpatialFixture(t, repository, snapshot, &zone)
	assessment := authorityRiskAssessment(snapshot, zone, "coverage")
	if err := repository.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		t.Fatal(err)
	}
	read := readAuthorityFixture(t, ctx, repository, snapshot.ID)
	if read.Snapshot.Coverage == nil ||
		read.Snapshot.Coverage.Identity() != snapshot.Coverage.Identity() {
		t.Fatalf("权威快照覆盖范围丢失: %+v", read.Snapshot.Coverage)
	}
	changed := snapshot
	changed.Coverage = storageCoverage(now)
	changed.Coverage.SHA256 = strings.Repeat("b", 64)
	if _, err := repository.ReuseRiskAuthority(ctx, changed,
		[]hazard.RiskZone{zone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("权威快照覆盖范围漂移未拒绝: %v", err)
	}
}

func TestRiskAuthorityRejectsSameIDContentDrift(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	assessment := authorityRiskAssessment(snapshot, zone, "drift")
	if err := repository.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		t.Fatal(err)
	}
	changedSnapshot := snapshot
	changedSnapshot.Limitations = append([]string(nil), snapshot.Limitations...)
	changedSnapshot.Limitations = append(changedSnapshot.Limitations, "同 ID 非法变化")
	if _, err := repository.ReuseRiskAuthority(ctx, changedSnapshot,
		[]hazard.RiskZone{zone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同 ID 快照变化未拒绝: %v", err)
	}
	changedZone := zone
	changedZone.Maximum += 0.01
	if err := repository.SaveAnalysis(ctx, snapshot, []hazard.RiskZone{changedZone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同 ID 风险区变化未拒绝: %v", err)
	}
	if _, err := repository.ReuseRiskAuthority(ctx, snapshot,
		[]hazard.RiskZone{changedZone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("复用路径未拒绝同 ID 风险区变化: %v", err)
	}
}

func TestRiskAuthorityReuseAllowsOnlyFixedFallbackOverlay(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	assessment := authorityRiskAssessment(snapshot, zone, "fallback")
	if err := repository.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		t.Fatal(err)
	}
	fallback := riskAuthorityFallbackOverlay(snapshot, false)
	reused, err := repository.ReuseRiskAuthority(ctx, fallback, []hazard.RiskZone{zone})
	if err != nil || !reused {
		t.Fatalf("固定 fallback 覆盖未复用权威: reused=%v err=%v", reused, err)
	}
	boundaryFallback := riskAuthorityFallbackOverlay(snapshot, true)
	reused, err = repository.ReuseRiskAuthority(ctx, boundaryFallback, []hazard.RiskZone{zone})
	if err != nil || !reused {
		t.Fatalf("边界身份 fallback 覆盖未复用权威: reused=%v err=%v", reused, err)
	}
	fallback.Limitations[len(fallback.Limitations)-1] = "任意降级说明"
	if _, err = repository.ReuseRiskAuthority(ctx, fallback,
		[]hazard.RiskZone{zone}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非白名单 fallback 覆盖未拒绝: %v", err)
	}
}

func TestValidFallbackOverlayUsesStrictBoundaryWhitelist(t *testing.T) {
	stored, _ := storageFixture(time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC))
	standard := riskAuthorityFallbackOverlay(stored, false)
	boundary := riskAuthorityFallbackOverlay(stored, true)
	if !validFallbackOverlay(stored, standard) {
		t.Fatal("标准 fallback 覆盖应通过")
	}
	if !validFallbackOverlay(stored, boundary) {
		t.Fatal("成对边界身份 fallback 覆盖应通过")
	}
	if !validFallbackOverlay(standard, boundary) {
		t.Fatal("已固化标准 fallback 应允许追加成对边界身份覆盖")
	}
	if validFallbackOverlay(boundary, standard) {
		t.Fatal("已固化边界身份 fallback 不应允许移除边界覆盖")
	}
	tests := []struct {
		name   string
		mutate func(hazard.Snapshot) hazard.Snapshot
	}{
		{"仅边界标记", keepBoundaryQualityOnly},
		{"仅边界限制", keepBoundaryLimitationOnly},
		{"额外质量标记", addUnexpectedQualityFlag},
		{"额外快照限制", addUnexpectedSnapshotLimitation},
		{"额外来源限制", addUnexpectedSourceLimitation},
		{"边界标记乱序", reverseFallbackQualityFlags},
		{"边界限制乱序", reverseFallbackSnapshotLimitations},
		{"权威内容漂移", driftFallbackModelVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incoming := riskAuthorityFallbackOverlay(stored, true)
			if validFallbackOverlay(stored, test.mutate(incoming)) {
				t.Fatal("非白名单 fallback 覆盖未拒绝")
			}
		})
	}
}

func riskAuthorityFallbackOverlay(snapshot hazard.Snapshot, boundary bool) hazard.Snapshot {
	fallback := snapshot
	fallback.Status, fallback.Source.Stale = hazard.SnapshotStale, true
	fallback.Source.QualityFlags = append(append([]string(nil), snapshot.Source.QualityFlags...),
		fallbackQualityFlag)
	fallback.Source.Limitations = append(append([]string(nil), snapshot.Source.Limitations...),
		fallbackSourceLimitation)
	fallback.Limitations = append(append([]string(nil), snapshot.Limitations...),
		fallbackSnapshotLimitation)
	if boundary {
		fallback.Source.QualityFlags = append(fallback.Source.QualityFlags, fallbackBoundaryQualityFlag)
		fallback.Limitations = append(fallback.Limitations, fallbackBoundarySnapshotLimitation)
	}
	return fallback
}

func keepBoundaryQualityOnly(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.Limitations = snapshot.Limitations[:len(snapshot.Limitations)-1]
	return snapshot
}

func keepBoundaryLimitationOnly(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.Source.QualityFlags = snapshot.Source.QualityFlags[:len(snapshot.Source.QualityFlags)-1]
	return snapshot
}

func addUnexpectedQualityFlag(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.Source.QualityFlags = append(snapshot.Source.QualityFlags, "unexpected_quality")
	return snapshot
}

func addUnexpectedSnapshotLimitation(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.Limitations = append(snapshot.Limitations, "任意额外边界限制")
	return snapshot
}

func addUnexpectedSourceLimitation(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.Source.Limitations = append(snapshot.Source.Limitations, "任意额外来源限制")
	return snapshot
}

func reverseFallbackQualityFlags(snapshot hazard.Snapshot) hazard.Snapshot {
	last := len(snapshot.Source.QualityFlags) - 1
	snapshot.Source.QualityFlags[last-1], snapshot.Source.QualityFlags[last] =
		snapshot.Source.QualityFlags[last], snapshot.Source.QualityFlags[last-1]
	return snapshot
}

func reverseFallbackSnapshotLimitations(snapshot hazard.Snapshot) hazard.Snapshot {
	last := len(snapshot.Limitations) - 1
	snapshot.Limitations[last-1], snapshot.Limitations[last] =
		snapshot.Limitations[last], snapshot.Limitations[last-1]
	return snapshot
}

func driftFallbackModelVersion(snapshot hazard.Snapshot) hazard.Snapshot {
	snapshot.ModelVersion += "-drift"
	return snapshot
}

func TestRiskAuthorityReadRejectsLiveSnapshotTamper(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	assessment := authorityRiskAssessment(snapshot, zone, "tamper")
	if err := repository.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE hazard_snapshots
        SET model_version=model_version || '-tampered' WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	_, err := repository.ReadAuthority(ctx, snapshot.ID, ports.HazardAuthorityLimits{
		MaxZones: 10, MaxGeometryPoints: 1_000, MaxGeometryBytes: 1 << 20,
	})
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("当前快照与固化 JSON 不一致未 fail-closed: %v", err)
	}
}

func TestCanonicalStoredAuthorityRejectsDigestTamper(t *testing.T) {
	snapshot, zone := storageFixture(time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC))
	snapshot = normalizeSnapshotForStorage(snapshot)
	assessment := authorityRiskAssessment(snapshot, zone, "digest")
	snapshotJSON, snapshotDigest, err := payloadDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	assessmentJSON, assessmentDigest, err := payloadDigest(assessment)
	if err != nil {
		t.Fatal(err)
	}
	stored := storedRiskAuthority{
		snapshotJSON: snapshotJSON, snapshotDigest: strings.Repeat("0", 64),
		assessmentJSON: assessmentJSON, assessmentDigest: assessmentDigest,
	}
	if _, _, _, _, err = canonicalStoredAuthority(stored); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("快照摘要篡改未拒绝: digest=%s err=%v", snapshotDigest, err)
	}
}

func TestRiskAssessmentConcurrentConflictKeepsOneImmutableWinner(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	snapshot, zone := authorityStorageFixture(t, repository)
	left := authorityRiskAssessment(snapshot, zone, "left")
	right := authorityRiskAssessment(snapshot, zone, "right")
	right.Limitations = []string{"并发冲突夹具"}
	errorsByWriter := concurrentRiskAssessmentWrites(ctx, repository, snapshot, left, right)
	successes, conflicts := 0, 0
	for _, err := range errorsByWriter {
		if err == nil {
			successes++
		} else if errors.Is(err, domain.ErrInvalidInput) {
			conflicts++
		} else {
			t.Fatalf("并发写入返回意外错误: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("并发冲突结果错误: success=%d conflict=%d errors=%v", successes, conflicts, errorsByWriter)
	}
}

func authorityStorageFixture(t *testing.T,
	repository *HazardRepository,
) (hazard.Snapshot, hazard.RiskZone) {
	t.Helper()
	snapshot, zone := storageFixture(time.Now().UTC())
	renameStorageFixture(&snapshot, &zone, snapshot.ID+"-authority")
	cleanupSnapshot(t, repository, snapshot.ID)
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(),
			`DELETE FROM risk_assessments WHERE snapshot_id=$1`, snapshot.ID)
	})
	if err := repository.SaveAnalysis(context.Background(), snapshot, []hazard.RiskZone{zone}); err != nil {
		t.Fatal(err)
	}
	persistAuthoritySpatialFixture(t, repository, snapshot, &zone)
	return snapshot, zone
}

func authorityRiskAssessment(snapshot hazard.Snapshot, zone hazard.RiskZone,
	suffix string,
) risk.Assessment {
	return risk.Assessment{
		ID: "risk-" + suffix + "-" + snapshot.ID, HazardType: snapshot.HazardType,
		SnapshotID: snapshot.ID, Decision: &risk.Decision{
			Level: zone.Level, ZoneCount: 1,
			HighestZoneIDs: []string{zone.ID}, Basis: "highest_zone_level",
		},
		Status: risk.AssessmentAvailable, DataStatus: risk.DataCurrent,
		Confidence: risk.Confidence{Level: risk.ConfidenceHigh}, RuleVersion: risk.RuleVersion,
		EvaluatedAt: snapshot.RunAt.Add(time.Minute),
	}
}

func persistAuthoritySpatialFixture(t *testing.T, repository *HazardRepository,
	snapshot hazard.Snapshot, zone *hazard.RiskZone,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	zone.AreaSquareM, zone.AreaCalculated = 100, true
	if _, err = tx.Exec(ctx, `UPDATE risk_zones SET area_square_meters=$1,
        area_calculated=TRUE WHERE id=$2 AND snapshot_id=$3`, zone.AreaSquareM,
		zone.ID, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	analysisID := "spatial-" + snapshot.ID
	if _, err = tx.Exec(ctx, `INSERT INTO spatial_analyses (
        id,snapshot_id,algorithm_version,area_method,status,zone_count,
        merged_area_square_meters,calculated_at,dataset_references,area_input_references,
        input_references,limitations
    ) VALUES ($1,$2,'fixture-v1','postgis','available',1,$3,$4,'[]','[]','[]','[]')`,
		analysisID, snapshot.ID, zone.AreaSquareM, snapshot.RunAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO spatial_zone_results (
        analysis_id,snapshot_id,zone_id,area_square_meters,admin_matches,exposures,
        input_references,limitations
    ) VALUES ($1,$2,$3,$4,'{}','{}','[]','[]')`, analysisID, snapshot.ID,
		zone.ID, zone.AreaSquareM); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func readAuthorityFixture(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshotID string,
) ports.HazardAuthorityRead {
	t.Helper()
	value, err := repository.ReadAuthority(ctx, snapshotID, ports.HazardAuthorityLimits{
		MaxZones: 10, MaxGeometryPoints: 1_000, MaxGeometryBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertConcurrentSameAssessment(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshot hazard.Snapshot, assessment risk.Assessment,
) {
	t.Helper()
	errorsByWriter := concurrentRiskAssessmentWrites(ctx, repository, snapshot, assessment, assessment)
	for _, err := range errorsByWriter {
		if err != nil {
			t.Fatalf("相同风险评估并发幂等写入失败: %v", err)
		}
	}
}

func concurrentRiskAssessmentWrites(ctx context.Context, repository *HazardRepository,
	snapshot hazard.Snapshot, values ...risk.Assessment,
) []error {
	start := make(chan struct{})
	result := make([]error, len(values))
	var wait sync.WaitGroup
	for index := range values {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result[index] = repository.SaveRiskAssessment(ctx, snapshot, values[index])
		}(index)
	}
	close(start)
	wait.Wait()
	return result
}

type authorityCountQueryer struct {
	row         pgx.Row
	queryCalled bool
}

func (q *authorityCountQueryer) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

func (q *authorityCountQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	q.queryCalled = true
	return nil, errors.New("不应加载风险区摘要")
}

type tripleIntegerRow [3]int64

func (r tripleIntegerRow) Scan(dest ...any) error {
	for index, value := range r {
		pointer, ok := dest[index].(*int64)
		if !ok {
			return errors.New("统计目标类型错误")
		}
		*pointer = value
	}
	return nil
}
