package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNewMapAPIHandlerRejectsUnfilteredFacilitiesWithoutDatabase(t *testing.T) {
	cfg := mapEnabledConfig()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newMapAPIHandler(cfg, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/places/nearby", strings.NewReader(
		`{"hazardType":"landslide","center":{"longitude":116.4,"latitude":39.9},"kind":"shelter","radiusMeters":1000}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"insufficient_data"`) {
		t.Fatalf("缺少 PostGIS 时设施接口未明确降级: status=%d body=%s",
			response.Code, response.Body.String())
	}
}

func TestNewMapAPIHandlerKeepsRouteContractWithoutDatabase(t *testing.T) {
	handler, err := newMapAPIHandler(mapEnabledConfig(), nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/routes", strings.NewReader(
		`{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"transit"}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("路线接口契约未保留: status=%d body=%s", response.Code, response.Body.String())
	}
}

func mapEnabledConfig() config.Config {
	return config.Config{Map: config.MapConfig{
		Enabled: true, BaseURL: "https://example.test", APIKey: "server-key",
		SecurityCode: "server-code", Timeout: time.Second,
	}}
}
