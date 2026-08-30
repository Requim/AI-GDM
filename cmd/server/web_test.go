package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/resources"
)

func TestWebConsoleDoesNotShadowHealthOrAPI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := newTestHTTPServer(t, logger)
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	if err := server.Mount("/api/v1", api); err != nil {
		t.Fatal(err)
	}
	if err := mountWebConsole(server, nil, config.Config{Environment: "test"}, &resources.Resources{}, logger); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, server.Handler(), "/", http.StatusOK)
	assertStatus(t, server.Handler(), "/healthz", http.StatusOK)
	assertStatus(t, server.Handler(), "/api/v1/hazards/landslide", http.StatusAccepted)
}

func TestWebConsoleShowsUnavailableWithoutDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newWebConsole(nil, config.Config{Environment: "test"}, &resources.Resources{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "PostGIS 未配置") {
		t.Fatalf("控制台降级响应无效: status=%d", response.Code)
	}
}

func assertStatus(t *testing.T, handler http.Handler, path string, expected int) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != expected {
		t.Fatalf("%s status=%d want=%d", path, response.Code, expected)
	}
}
