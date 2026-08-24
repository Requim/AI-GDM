package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("APP_HTTP_ADDR", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("APP_LOG_LEVEL", "")
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "")
	t.Setenv("REDIS_DB", "")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPAddr != ":8080" || got.ShutdownTimeout != 10*time.Second {
		t.Fatalf("Load() = %+v", got)
	}
}

func TestLoadRejectsInvalidTimeout(t *testing.T) {
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "invalid")

	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝无效时长")
	}
}

func TestLoadRejectsInvalidRedisDB(t *testing.T) {
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "")
	t.Setenv("REDIS_DB", "-1")

	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝负数 Redis DB")
	}
}
