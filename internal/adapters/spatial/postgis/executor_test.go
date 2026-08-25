package postgis

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestSpatialSQLUsesGeographyAndUnion(t *testing.T) {
	if !strings.Contains(zoneAreaSQL, "ST_Area(geometry::geography)") {
		t.Fatalf("区级面积未使用 geography: %s", zoneAreaSQL)
	}
	if !strings.Contains(mergedAreaSQL, "ST_UnaryUnion(ST_Collect(geometry))::geography") {
		t.Fatalf("合并面积未先去重: %s", mergedAreaSQL)
	}
	if strings.Contains(zoneAreaSQL+mergedAreaSQL, "ST_MakeValid") {
		t.Fatal("空间分析不应静默修复无效风险区几何")
	}
}

func TestValidateRequest(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		pool       *pgxpool.Pool
		snapshotID string
		at         time.Time
	}{
		{name: "空连接池", snapshotID: "snapshot-1", at: now},
		{name: "空快照", pool: &pgxpool.Pool{}, snapshotID: "", at: now},
		{name: "空时间", pool: &pgxpool.Pool{}, snapshotID: "snapshot-1"},
		{name: "非UTC", pool: &pgxpool.Pool{}, snapshotID: "snapshot-1",
			at: now.In(time.FixedZone("CST", 8*60*60))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRequest(test.pool, test.snapshotID, test.at); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("validateRequest() error = %v", err)
			}
		})
	}
}

func TestAreaOnlyZoneDistinguishesMissingDataFromZero(t *testing.T) {
	zone := areaOnlyZone(calculatedZone{id: "zone-1", area: 100, reference: "risk-zone:1"})
	metrics := []struct {
		status   spatialanalysis.MetricStatus
		quantity *float64
		coverage *float64
	}{
		{zone.Population.Status, zone.Population.Quantity, zone.Population.CoverageRatio},
		{zone.Roads.Status, zone.Roads.Quantity, zone.Roads.CoverageRatio},
		{zone.POIs.Status, zone.POIs.Quantity, zone.POIs.CoverageRatio},
	}
	for _, metric := range metrics {
		if metric.status != spatialanalysis.MetricUnavailable || metric.quantity != nil || metric.coverage != nil {
			t.Fatalf("缺少数据被错误编码为零值: %+v", metric)
		}
	}
}
