package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	platformobservability "github.com/Requim/AI-GDM/internal/platform/observability"
)

func TestNewObservationRegistryUsesFixedComponents(t *testing.T) {
	registry, err := newObservationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	statuses := registry.Snapshot()
	ids := make([]string, 0, len(statuses))
	for _, status := range statuses {
		ids = append(ids, status.ComponentID)
	}
	expected := []string{"amap", "bocha", "lhasa", "llm", "weather"}
	if !reflect.DeepEqual(ids, expected) {
		t.Fatalf("固定组件=%v，期望 %v", ids, expected)
	}
}

func TestMountMetricsRejectsMissingDependencies(t *testing.T) {
	registry, err := platformobservability.New([]string{"provider"})
	if err != nil {
		t.Fatal(err)
	}
	if err = mountMetrics(nil, registry); err == nil {
		t.Fatal("缺少 HTTP 服务未被拒绝")
	}
	server, err := httpserver.New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpserver.SecurityOptions{AdminToken: "0123456789abcdef0123456789abcdef",
			RateLimitPerMinute: 60_000, RateLimitBurst: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if err = mountMetrics(server, nil); err == nil {
		t.Fatal("缺少观测注册表未被拒绝")
	}
}

func TestMountMetricsUsesProtectedRegistry(t *testing.T) {
	registry, err := newObservationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := httpserver.New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpserver.SecurityOptions{AdminToken: "0123456789abcdef0123456789abcdef",
			RateLimitPerMinute: 60_000, RateLimitBurst: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if err = mountMetrics(server, registry); err != nil {
		t.Fatal(err)
	}
	assertProtectedMetricsMethods(t, server.Handler())
	assertMetricsRouteDoesNotExposeSubtree(t, server.Handler())
}

func assertProtectedMetricsMethods(t *testing.T, handler http.Handler) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/metrics", nil)
		if response := serveHTTP(handler, request); response.Code != http.StatusUnauthorized {
			t.Fatalf("未授权 %s metrics status=%d", method, response.Code)
		}
		request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
		response := serveHTTP(handler, request)
		if response.Code != http.StatusOK {
			t.Fatalf("授权 %s metrics status=%d body=%s", method, response.Code, response.Body.String())
		}
		if method == http.MethodGet && !strings.Contains(response.Body.String(), `component="lhasa"`) {
			t.Fatalf("授权 metrics 缺少固定组件: %s", response.Body.String())
		}
	}
}

func assertMetricsRouteDoesNotExposeSubtree(t *testing.T, handler http.Handler) {
	t.Helper()
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/metrics/", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/metrics/x", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/metrics", want: http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
		response := serveHTTP(handler, request)
		if response.Code != test.want || strings.Contains(response.Body.String(), "ai_gdm_component_") {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func serveHTTP(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
