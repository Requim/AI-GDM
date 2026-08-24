package resources

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/adapters/storage/rediscache"
	"github.com/Requim/AI-GDM/internal/platform/config"
)

// Resources 保存进程拥有的外部连接。
type Resources struct {
	Database *pgxpool.Pool
	Redis    *redis.Client
}

// Open 按配置连接、验证并迁移外部资源。
func Open(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Resources, error) {
	resources := &Resources{}
	if cfg.DatabaseURL != "" {
		pool, err := postgres.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		resources.Database = pool
		if err = postgres.Migrate(ctx, pool); err != nil {
			resources.Close()
			return nil, fmt.Errorf("迁移数据库: %w", err)
		}
		logger.Info("PostGIS 已连接并完成迁移")
	}
	if cfg.RedisAddr != "" {
		client, err := rediscache.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			resources.Close()
			return nil, err
		}
		resources.Redis = client
		logger.Info("Redis 已连接", "database", cfg.RedisDB)
	}
	return resources, nil
}

// Close 关闭已成功创建的全部连接。
func (r *Resources) Close() {
	if r.Redis != nil {
		_ = r.Redis.Close()
	}
	if r.Database != nil {
		r.Database.Close()
	}
}
