package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache 使用 Redis 保存可重建的 JSON 缓存。
type Cache struct {
	client *redis.Client
	prefix string
}

// Open 创建并验证 Redis 客户端。
func Open(ctx context.Context, addr, password string, database int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: database})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接 Redis: %w", err)
	}
	return client, nil
}

// New 创建带项目命名空间的缓存适配器。
func New(client *redis.Client, prefix string) *Cache {
	return &Cache{client: client, prefix: prefix}
}

// Get 将缓存 JSON 解码到 destination。
func (c *Cache) Get(ctx context.Context, key string, destination any) (bool, error) {
	data, err := c.client.Get(ctx, c.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取 Redis 缓存: %w", err)
	}
	if err = json.Unmarshal(data, destination); err != nil {
		return false, fmt.Errorf("解码 Redis 缓存: %w", err)
	}
	return true, nil
}

// Set 写入带过期时间的 JSON 缓存。
func (c *Cache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码 Redis 缓存: %w", err)
	}
	if err = c.client.Set(ctx, c.key(key), data, ttl).Err(); err != nil {
		return fmt.Errorf("写入 Redis 缓存: %w", err)
	}
	return nil
}

// Delete 删除缓存值。
func (c *Cache) Delete(ctx context.Context, key string) error {
	if err := c.client.Del(ctx, c.key(key)).Err(); err != nil {
		return fmt.Errorf("删除 Redis 缓存: %w", err)
	}
	return nil
}

func (c *Cache) key(value string) string {
	return c.prefix + ":" + value
}
