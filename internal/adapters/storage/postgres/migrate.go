package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate 按版本顺序执行尚未应用的嵌入式 SQL 迁移。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaTableSQL); err != nil {
		return fmt.Errorf("建立迁移版本表: %w", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if err := applyMigration(ctx, pool, migration); err != nil {
			return err
		}
	}
	return nil
}

type migration struct {
	version int64
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取迁移文件: %w", err)
	}
	values := make([]migration, 0, len(entries))
	for _, entry := range entries {
		value, err := readMigration(entry.Name())
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].version < values[j].version })
	return values, nil
}

func readMigration(name string) (migration, error) {
	versionText, _, ok := strings.Cut(filepath.Base(name), "_")
	if !ok {
		return migration{}, fmt.Errorf("迁移文件名缺少版本: %s", name)
	}
	version, err := strconv.ParseInt(versionText, 10, 64)
	if err != nil {
		return migration{}, fmt.Errorf("解析迁移版本 %s: %w", name, err)
	}
	content, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		return migration{}, fmt.Errorf("读取迁移 %s: %w", name, err)
	}
	return migration{version: version, name: name, sql: string(content)}, nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, value migration) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开始迁移事务 %s: %w", value.name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var applied bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, value.version).Scan(&applied)
	if err != nil || applied {
		if err != nil {
			return fmt.Errorf("查询迁移版本 %s: %w", value.name, err)
		}
		return nil
	}
	if _, err = tx.Exec(ctx, value.sql); err != nil {
		return fmt.Errorf("执行迁移 %s: %w", value.name, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version, name) VALUES($1,$2)`, value.version, value.name); err != nil {
		return fmt.Errorf("记录迁移 %s: %w", value.name, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交迁移 %s: %w", value.name, err)
	}
	return nil
}

const schemaTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`
