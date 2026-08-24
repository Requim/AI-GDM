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

func serve(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}
