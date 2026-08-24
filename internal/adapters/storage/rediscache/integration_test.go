package rediscache

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCacheIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("未配置 TEST_REDIS_ADDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Open(ctx, addr, os.Getenv("TEST_REDIS_PASSWORD"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	cache := New(client, "ai-gdm-integration")
	key := time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() { _ = cache.Delete(context.Background(), key) })
	want := map[string]string{"status": "ready"}
	if err = cache.Set(ctx, key, want, time.Minute); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	found, err := cache.Get(ctx, key, &got)
	if err != nil || !found || got["status"] != want["status"] {
		t.Fatalf("Get() found=%v value=%v err=%v", found, got, err)
	}
}
