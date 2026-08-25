package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthAndReadiness(t *testing.T) {
	server := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	health := serve(t, server.Handler(), "/healthz")
	if health.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", health.Code)
	}

	ready := serve(t, server.Handler(), "/readyz")
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d", ready.Code)
	}
}

func TestRequestIDHeader(t *testing.T) {
	server := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := serve(t, server.Handler(), "/healthz")

	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("响应缺少 X-Request-ID")
	}
}

func TestMountAddsApplicationRoutes(t *testing.T) {
	server := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	if err := server.Mount("/api/v1", api); err != nil {
		t.Fatal(err)
	}

	response := serve(t, server.Handler(), "/api/v1/hazards/landslide")
	if response.Code != http.StatusAccepted {
		t.Fatalf("mounted route status = %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("挂载路由响应缺少 X-Request-ID")
	}
}

func TestMountRejectsInvalidInputs(t *testing.T) {
	server := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		pattern string
		handler http.Handler
	}{
		{pattern: "api/v1", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
		{pattern: "/api/v1?debug=true", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
		{pattern: "/api/v1"},
	}
	for _, test := range tests {
		if err := server.Mount(test.pattern, test.handler); err == nil {
			t.Fatalf("Mount(%q) 未拒绝无效输入", test.pattern)
		}
	}
}

func serve(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}
