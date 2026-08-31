package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestMigrateUpgradesLegacyHazardSnapshotCoverage(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := newIsolatedMigrationPool(t, ctx, databaseURL)
	if err := applyMigrationsThrough(ctx, pool, 9); err != nil {
		t.Fatal(err)
	}
	assertCoverageColumnAbsent(t, ctx, pool)
	snapshot, _ := storageFixture(time.Date(2026, 8, 31, 8, 0, 0, 123456000, time.UTC))
	snapshot = normalizeSnapshotForStorage(snapshot)
	insertLegacyHazardSnapshot(t, ctx, pool, snapshot)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCoverageUpgrade(t, ctx, pool, snapshot.ID)
	repository := NewHazardRepository(pool)
	assertLegacySnapshotReadable(t, ctx, repository, snapshot.ID)
	for attempt := 0; attempt < 2; attempt++ {
		if err := repository.SaveSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("第 %d 次幂等保存旧快照失败: %v", attempt+1, err)
		}
	}
	assertLegacySnapshotReadable(t, ctx, repository, snapshot.ID)
	assertCoverageIsSQLNull(t, ctx, pool, snapshot.ID)
}

func newIsolatedMigrationPool(t *testing.T, ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()
	admin, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("ai_gdm_coverage_upgrade_%d_%d", os.Getpid(), time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropMigrationSchema(t, admin, quotedSchema) })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return pool
}

func dropMigrationSchema(t *testing.T, pool *pgxpool.Pool, quotedSchema string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
		t.Errorf("清理迁移测试 schema: %v", err)
	}
}

func applyMigrationsThrough(ctx context.Context, pool *pgxpool.Pool, maximum int64) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开始旧版本迁移事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockID); err != nil {
		return fmt.Errorf("获取旧版本迁移锁: %w", err)
	}
	if _, err = tx.Exec(ctx, schemaTableSQL); err != nil {
		return fmt.Errorf("建立旧版本迁移表: %w", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, value := range migrations {
		if value.version > maximum {
			break
		}
		if err = applyMigration(ctx, tx, value); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交旧版本迁移事务: %w", err)
	}
	return nil
}

func assertCoverageColumnAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, column := range []string{"coverage", "superseded_at", "superseded_by_coverage"} {
		var exists bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='hazard_snapshots'
				AND column_name=$1
		)`, column).Scan(&exists)
		if err != nil || exists {
			t.Fatalf("001-009 后 %s 列状态错误: exists=%v err=%v", column, exists, err)
		}
	}
}

func insertLegacyHazardSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	snapshot hazard.Snapshot,
) {
	t.Helper()
	thresholds, source, _, limitations, err := snapshotJSON(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO hazard_snapshots (
		id,hazard_type,model_name,model_version,run_at,valid_from,valid_to,raster_reference,
		probability_semantics,thresholds,status,source,limitations,analysis_complete
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		snapshot.ID, snapshot.HazardType, snapshot.ModelName, snapshot.ModelVersion, snapshot.RunAt,
		snapshot.ValidFrom, snapshot.ValidTo, snapshot.RasterReference, snapshot.ProbabilitySemantics,
		thresholds, snapshot.Status, source, limitations, true)
	if err != nil {
		t.Fatalf("插入 001-009 旧快照: %v", err)
	}
}

func assertCoverageUpgrade(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	snapshotID string,
) {
	t.Helper()
	var migrationName string
	if err := pool.QueryRow(ctx, `SELECT name FROM schema_migrations WHERE version=11`).
		Scan(&migrationName); err != nil || migrationName != "011_hazard_snapshot_coverage.sql" {
		t.Fatalf("011 迁移记录错误: name=%q err=%v", migrationName, err)
	}
	for _, column := range []string{"coverage", "superseded_at", "superseded_by_coverage"} {
		var nullable string
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='hazard_snapshots'
				AND column_name=$1`, column).Scan(&nullable); err != nil || nullable != "YES" {
			t.Fatalf("%s 列可空契约错误: nullable=%q err=%v", column, nullable, err)
		}
	}
	assertCoverageIsSQLNull(t, ctx, pool, snapshotID)
	var supersessionNull bool
	if err := pool.QueryRow(ctx, `SELECT superseded_at IS NULL AND superseded_by_coverage IS NULL
		FROM hazard_snapshots WHERE id=$1`, snapshotID).Scan(&supersessionNull); err != nil || !supersessionNull {
		t.Fatalf("旧快照失效状态非空: is_null=%v err=%v", supersessionNull, err)
	}
}

func assertCoverageIsSQLNull(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	snapshotID string,
) {
	t.Helper()
	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT coverage IS NULL FROM hazard_snapshots WHERE id=$1`,
		snapshotID).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("旧快照 coverage 非 SQL NULL: is_null=%v err=%v", isNull, err)
	}
}

func assertLegacySnapshotReadable(t *testing.T, ctx context.Context,
	repository *HazardRepository, snapshotID string,
) {
	t.Helper()
	stored, err := repository.GetSnapshot(ctx, snapshotID)
	if err != nil || stored.ID != snapshotID || stored.Coverage != nil {
		t.Fatalf("升级后读取旧快照错误: snapshot=%+v err=%v", stored, err)
	}
}
