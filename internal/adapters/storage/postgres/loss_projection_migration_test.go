package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestLossProjectionMigrationStoresOnlyExplicitSpatialFeatures(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/008_loss_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(content))
	required := []string{
		"create table spatial_exposure_projections",
		"id = 'exposure-' || projection_digest",
		"projection_status text not null check (projection_status = 'available')",
		"region_code text not null check (region_code = 'cn')",
		"admin_boundary_digest text not null",
		"valid_exposure_reference_array",
		"input_references jsonb not null check (valid_exposure_reference_array(input_references))",
		"dataset_references jsonb not null check (valid_exposure_reference_array(dataset_references))",
		"collate \"c\"",
		"create table spatial_exposure_projection_zones",
		"primary key (projection_id, zone_id)",
		"create table spatial_exposure_features",
		"primary key (projection_id, feature_id)",
		"feature_kind in ('population', 'road', 'facility')",
		"feature_kind <> 'facility' or quantity = trunc(quantity)",
		"jsonb_typeof(input_references) = 'array'",
		"create table spatial_exposure_feature_zones",
		"references spatial_exposure_features(projection_id, analysis_id, feature_id) on delete cascade",
		"references spatial_exposure_projection_zones(projection_id, analysis_id, zone_id) on delete cascade",
		"completed exposure projection content is immutable",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("损失投影迁移缺少契约片段 %q", fragment)
		}
	}
	if strings.Contains(sql, "insert into spatial_exposure_features") ||
		strings.Contains(sql, "from spatial_zone_results") {
		t.Fatal("迁移不得把区级汇总或缺失指标伪造成可用 feature")
	}
	if strings.Contains(sql, "analysis_id text not null references spatial_analyses(id) on delete cascade") ||
		strings.Contains(sql, "references spatial_zone_results(analysis_id, zone_id) on delete cascade") ||
		strings.Contains(sql, "pg_trigger_depth") {
		t.Fatal("完整投影不得随上游空间分析或 zone result 级联删除")
	}
}

func TestLossProjectionPreflightNeverSerializesGeometry(t *testing.T) {
	for _, fragment := range []string{"ST_NPoints", "ST_MemSize", "OCTET_LENGTH", "reference_count"} {
		if !strings.Contains(lossProjectionBudgetSQL, fragment) {
			t.Errorf("损失投影前置统计缺少 %q", fragment)
		}
	}
	if strings.Contains(lossProjectionBudgetSQL+lossProjectionZonesSQL, "ST_AsGeoJSON") {
		t.Fatal("损失投影不得物化风险区几何 JSON")
	}
	if lossProjectionReadOptions.IsoLevel != pgx.RepeatableRead ||
		lossProjectionReadOptions.AccessMode != pgx.ReadOnly {
		t.Fatalf("损失投影事务隔离级别错误: %+v", lossProjectionReadOptions)
	}
}
