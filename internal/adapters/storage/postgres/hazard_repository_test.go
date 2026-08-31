package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	original := hazard.Snapshot{
		Thresholds: []hazard.RiskThreshold{{Level: hazard.RiskLow, Minimum: 0, Maximum: 1}},
		Coverage: &hazard.Coverage{Mode: hazard.CoverageAdministrativeBoundary, RegionCode: "CN",
			BoundaryID: "CHN-ADM0-1", BoundaryType: "ADM0", BoundaryVersion: "2024",
			Source: "fixture", License: "Public Domain", Reference: "https://example.test/china.geojson",
			SHA256: strings.Repeat("a", 64), GeometrySHA256: strings.Repeat("b", 64),
			CollectedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
		Limitations: []string{"辅助研判"},
	}
	thresholds, source, coverage, limitations, err := snapshotJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded hazard.Snapshot
	if err = decodeSnapshotJSON(&decoded, thresholds, source, coverage, limitations); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Thresholds) != 1 || decoded.Limitations[0] != "辅助研判" ||
		decoded.Coverage == nil || decoded.Coverage.BoundaryID != "CHN-ADM0-1" {
		value, _ := json.Marshal(decoded)
		t.Fatalf("往返结果错误: %s", value)
	}
}

func TestSnapshotJSONKeepsMissingCoverageAsSQLNull(t *testing.T) {
	_, _, coverage, _, err := snapshotJSON(hazard.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if coverage != nil {
		t.Fatalf("缺失覆盖范围应绑定 SQL NULL，实际为 %q", coverage)
	}
}

func TestRiskZoneSQLPersistsAreaCalculated(t *testing.T) {
	for name, query := range map[string]string{"save": saveZoneSQL, "select": selectZonesSQL} {
		if strings.Count(query, "area_calculated") != 1 {
			t.Fatalf("%s SQL 未唯一包含 area_calculated: %s", name, query)
		}
	}
	if !strings.Contains(saveZoneSQL, "$12") {
		t.Fatalf("saveZoneSQL 参数数量未同步: %s", saveZoneSQL)
	}
}

func TestSnapshotSQLPersistsNullableImmutableCoverage(t *testing.T) {
	if !strings.Contains(saveSnapshotSQL, "source,coverage,limitations") ||
		!strings.Contains(saveSnapshotSQL, "$15") ||
		!strings.Contains(saveSnapshotSQL, "coverage IS NOT DISTINCT FROM EXCLUDED.coverage") ||
		!strings.Contains(selectSnapshotSQL, "source,coverage,limitations") {
		t.Fatalf("快照覆盖范围 SQL 契约无效: save=%s select=%s", saveSnapshotSQL, selectSnapshotSQL)
	}
}

func TestRiskReadersRejectInvalidInputsBeforeDatabaseAccess(t *testing.T) {
	repository := NewHazardRepository(nil)
	for _, value := range []hazard.Type{
		"", " landslide", "landslide ", "land-slide", "Landslide", hazard.Type(strings.Repeat("x", 65)),
	} {
		_, _, err := repository.LatestRisk(context.Background(), value)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("LatestRisk(%q) error = %v", value, err)
		}
		if _, err = repository.LatestMapRisk(context.Background(), value, 100000); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("LatestMapRisk(%q) error = %v", value, err)
		}
	}
	if _, err := repository.LatestMapRisk(context.Background(), hazard.TypeLandslide, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("LatestMapRisk(max=0) error = %v", err)
	}
	for _, value := range []string{"", " snapshot", "snapshot ", strings.Repeat("x", 257)} {
		_, _, err := repository.RiskDetail(context.Background(), value)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("RiskDetail(%q) error = %v", value, err)
		}
		_, _, _, err = repository.RiskDetailBounded(context.Background(), value, 500)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("RiskDetailBounded(%q) error = %v", value, err)
		}
	}
	if _, _, _, err := repository.RiskDetailBounded(context.Background(), "snapshot", 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("RiskDetailBounded(max=0) error = %v", err)
	}
}

func TestCompleteRiskQueriesHideIncompleteOrUnavailableSnapshots(t *testing.T) {
	for name, where := range map[string]string{
		"latest":          latestSnapshotWhere,
		"latest-analysis": latestAnalysisWhere,
		"detail":          riskDetailWhere,
	} {
		if !strings.Contains(where, "analysis_complete=TRUE") {
			t.Fatalf("%s 查询未限制完整分析: %s", name, where)
		}
		if !strings.Contains(where, "status IN ('available','stale')") {
			t.Fatalf("%s 查询未限制可用状态: %s", name, where)
		}
	}
	for name, where := range map[string]string{
		"latest": latestSnapshotWhere, "latest-analysis": latestAnalysisWhere,
	} {
		if !strings.Contains(where, "superseded_at IS NULL") {
			t.Fatalf("%s 查询未排除已被新边界取代的快照: %s", name, where)
		}
	}
	if !strings.Contains(latestSnapshotWhere, "coverage IS NOT NULL") {
		t.Fatalf("全局最新查询未排除旧矩形覆盖快照: %s", latestSnapshotWhere)
	}
	if strings.Contains(riskDetailWhere, "superseded_at") {
		t.Fatalf("历史详情查询不应隐藏已取代快照: %s", riskDetailWhere)
	}
}

func TestReconcileAnalysisCoverageSQLRestoresMatchingBoundaryAndScopesAuditWaterline(t *testing.T) {
	for _, fragment := range []string{
		"SELECT $6::jsonb AS coverage",
		"ORDER BY run_at DESC,created_at DESC,id DESC LIMIT 1",
		"ROW(candidate.run_at,candidate.created_at,candidate.id) <= ROW(anchor.run_at,anchor.created_at,anchor.id)",
		"COALESCE(candidate.superseded_at,$7)",
		"candidate.coverage->>'boundaryId'",
		"THEN NULL ELSE COALESCE(candidate.superseded_at,$7) END",
		"superseded_by_coverage->>'geometrySha256'",
		"IS NOT DISTINCT FROM ROW",
		"IS DISTINCT FROM ROW",
	} {
		if !strings.Contains(reconcileAnalysisCoverageSQL, fragment) {
			t.Fatalf("覆盖范围协调 SQL 缺少 %q: %s", fragment, reconcileAnalysisCoverageSQL)
		}
	}
}

func TestCompleteRiskReadsUseReadOnlyRepeatableReadTransaction(t *testing.T) {
	if completeRiskReadOptions.IsoLevel != pgx.RepeatableRead {
		t.Fatalf("IsoLevel = %v", completeRiskReadOptions.IsoLevel)
	}
	if completeRiskReadOptions.AccessMode != pgx.ReadOnly {
		t.Fatalf("AccessMode = %v", completeRiskReadOptions.AccessMode)
	}
}

func TestBoundedMapReadStopsBeforeLoadingOverLimitZones(t *testing.T) {
	queryer := &mapCountOnlyQueryer{total: 100001}
	zones, total, err := boundedZonesBySnapshot(context.Background(), queryer,
		"snapshot-map", 100000)
	if !errors.Is(err, domain.ErrInsufficientData) || total != 100001 || zones != nil {
		t.Fatalf("boundedZonesBySnapshot() zones=%v total=%d error=%v", zones, total, err)
	}
	if queryer.zoneQueryCalled {
		t.Fatal("超过上限后仍加载了风险区明细")
	}
}

func TestMapRiskSQLCountsThenOrdersAndLimits(t *testing.T) {
	if !strings.Contains(countZonesSQL, "COUNT(*)") ||
		!strings.Contains(selectMapZonesSQL, "CASE risk_level") ||
		!strings.Contains(selectMapZonesSQL, "LIMIT $2") {
		t.Fatalf("地图风险 SQL 缺少计数、优先级或限制: %s / %s", countZonesSQL, selectMapZonesSQL)
	}
}

type mapCountOnlyQueryer struct {
	total           int
	zoneQueryCalled bool
}

func (q *mapCountOnlyQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	q.zoneQueryCalled = true
	return nil, errors.New("不应执行风险区查询")
}

func (q *mapCountOnlyQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return integerRow{value: q.total}
}

type integerRow struct{ value int }

func (r integerRow) Scan(dest ...any) error {
	value, ok := dest[0].(*int)
	if !ok {
		return errors.New("计数目标类型错误")
	}
	*value = r.value
	return nil
}
