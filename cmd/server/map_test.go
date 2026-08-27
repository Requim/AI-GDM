package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/platform/config"
)

func TestNewMapProviderDisabled(t *testing.T) {
	provider, err := newMapProvider(config.Config{}, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if provider != nil {
		t.Fatal("关闭高德地图时不应创建适配器")
	}
}

func TestNewMapProviderRejectsMissingServerSecrets(t *testing.T) {
	cfg := config.Config{Map: config.MapConfig{
		Enabled: true, BaseURL: "https://example.test", Timeout: time.Second,
	}}
	if _, err := newMapProvider(cfg, nil, slog.Default()); err == nil {
		t.Fatal("缺少服务端密钥时未拒绝创建高德适配器")
	}
}
