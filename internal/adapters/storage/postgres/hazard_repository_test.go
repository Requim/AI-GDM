package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	original := hazard.Snapshot{
		Thresholds:  []hazard.RiskThreshold{{Level: hazard.RiskLow, Minimum: 0, Maximum: 1}},
		Limitations: []string{"辅助研判"},
	}
	thresholds, source, limitations, err := snapshotJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded hazard.Snapshot
	if err = decodeSnapshotJSON(&decoded, thresholds, source, limitations); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Thresholds) != 1 || decoded.Limitations[0] != "辅助研判" {
		value, _ := json.Marshal(decoded)
		t.Fatalf("往返结果错误: %s", value)
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
	}
}

func TestCompleteRiskQueriesHideIncompleteOrUnavailableSnapshots(t *testing.T) {
	for name, where := range map[string]string{
		"latest": latestSnapshotWhere,
		"detail": riskDetailWhere,
	} {
		if !strings.Contains(where, "analysis_complete=TRUE") {
			t.Fatalf("%s 查询未限制完整分析: %s", name, where)
		}
		if !strings.Contains(where, "status IN ('available','stale')") {
			t.Fatalf("%s 查询未限制可用状态: %s", name, where)
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
