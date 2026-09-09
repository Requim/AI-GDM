package postgres

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
)

func TestRegionalGeometryIncludesAllSelectedRegionZones(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveRankedExposureZones(t, ctx, repository, now)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-time.Minute), "regional-full", false)
	boundary := fullExposureBoundary()
	boundary.RegionCode, boundary.BoundaryType, boundary.BoundaryID = "CN-110000", "ADM1", "CHN-ADM1-AMAP-110000"
	value, err := repository.ReadExposureGeometryForRegion(ctx, snapshot.ID, analysis.ID, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Zones) != 12 || value.Scope.Policy != exposurecollection.RegionalScopePolicy ||
		!value.Scope.CompleteCoverage || value.Scope.RegionCode != boundary.RegionCode {
		t.Fatalf("行政区被截断成全国前十热点: %+v", value.Scope)
	}
	var expected float64
	if err = repository.pool.QueryRow(ctx, `SELECT ST_Area(ST_UnaryUnion(ST_Collect(geometry))::geography)
		FROM risk_zones WHERE snapshot_id=$1`, snapshot.ID).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if math.Abs(value.Analysis.TotalAreaSquareMeters-expected) > 0.01 {
		t.Fatal("重叠风险区面积未去重")
	}
}

func TestRegionalImpactClipsBeforeReadingAndAllowsEmptyRegion(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveSeparatedExposureZones(t, ctx, repository, now)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones, now.Add(-time.Minute), "regional-separated", false)
	boundary := fullExposureBoundary()
	boundary.RegionCode, boundary.BoundaryType, boundary.BoundaryID = "CN-110000", "ADM1", "CHN-ADM1-AMAP-110000"
	boundary.Geometry = json.RawMessage(clippedExposureBoundaryGeoJSON)
	value, err := repository.ReadRegionalImpact(ctx, snapshot.ID, analysis.ID, boundary)
	if err != nil || value.ZoneCount != 1 || value.AreaSquareMeters <= 0 {
		t.Fatalf("区域交集: %+v %v", value, err)
	}
	var outside float64
	if err = repository.pool.QueryRow(ctx, `SELECT ST_Area(ST_Difference(
		ST_SetSRID(ST_GeomFromGeoJSON($1),4326),ST_SetSRID(ST_GeomFromGeoJSON($2),4326))::geography)`,
		string(value.Geometry), string(boundary.Geometry)).Scan(&outside); err != nil || outside > 0.01 {
		t.Fatalf("风险越过行政区: %v %v", outside, err)
	}
	boundary.Geometry = json.RawMessage(`{"type":"Polygon","coordinates":[[[100,20],[101,20],[101,21],[100,20]]]}`)
	value, err = repository.ReadRegionalImpact(ctx, snapshot.ID, analysis.ID, boundary)
	if err != nil || value.ZoneCount != 0 || value.AreaSquareMeters != 0 || string(value.Geometry) != "null" {
		t.Fatalf("无相交风险不得返回全国数据: %+v %v", value, err)
	}
	if _, err = repository.ReadRegionalImpact(ctx, snapshot.ID+"-wrong", analysis.ID, boundary); err == nil {
		t.Fatal("接受了不匹配的快照")
	}
}

func TestRegionalSQLDoesNotContainNationalHotspotLimit(t *testing.T) {
	if strings.Contains(regionalZonesCTE, "LIMIT 10") || !strings.Contains(regionalZonesCTE, "ST_Intersection") ||
		!strings.Contains(regionalZonesCTE, "szr.analysis_id=$1") {
		t.Fatal("区域风险 SQL 未先按边界裁剪")
	}
}
