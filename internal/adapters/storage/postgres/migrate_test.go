package postgres

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreOrdered(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("没有嵌入任何迁移")
	}
	for index, value := range migrations {
		if strings.TrimSpace(value.sql) == "" {
			t.Fatalf("迁移 %s 为空", value.name)
		}
		if index > 0 && migrations[index-1].version >= value.version {
			t.Fatalf("迁移版本无序: %+v", migrations)
		}
	}
}

func TestSpatialAnalysisMigrationFollowsHazardCompletion(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	positions := make(map[int64]int, len(migrations))
	for index, value := range migrations {
		positions[value.version] = index
	}
	hazardPosition, hasHazard := positions[3]
	spatialPosition, hasSpatial := positions[4]
	if !hasHazard || !hasSpatial || spatialPosition <= hazardPosition {
		t.Fatalf("空间分析迁移未排在完整灾害分析之后: %+v", migrations)
	}
}

func TestSpatialAnalysisMigrationContract(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/004_spatial_analysis.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(content))
	required := []string{
		"add column area_calculated boolean not null default false",
		"create table spatial_analyses",
		"create table spatial_zone_results",
		"references hazard_snapshots(id) on delete cascade",
		"references spatial_analyses(id, snapshot_id) on delete cascade",
		"references risk_zones(id, snapshot_id) on delete cascade",
		"dataset_references jsonb not null",
		"area_input_references jsonb not null",
		"input_references jsonb not null",
		"limitations jsonb not null",
		"jsonb_typeof(limitations) = 'array'",
		"admin_matches jsonb not null",
		"exposures jsonb not null",
		"jsonb_typeof(exposures) = 'object'",
		"check (zone_count >= 0)",
		"check (merged_area_square_meters >= 0",
		"check (area_square_meters >= 0",
		"calculated_at timestamptz not null",
		"spatial_analyses_snapshot_latest_idx",
		"spatial_zone_results_zone_idx",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("空间分析迁移缺少契约片段 %q", fragment)
		}
	}
	if strings.Contains(sql, "analysis_complete") {
		t.Error("空间分析迁移不应复用灾害分析完成标记")
	}
}
