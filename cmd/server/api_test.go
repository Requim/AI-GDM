package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/platform/httpserver"
)

func TestApplicationAPIDispatchesRiskAndMapPrefixes(t *testing.T) {
	server := httpserver.New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := applicationAPI{
		hazard: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/hazards/landslide" {
				t.Fatalf("风险适配器收到错误路径: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		}),
		mapAPI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/places/nearby" {
				t.Fatalf("地图适配器收到错误路径: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusCreated)
		}),
	}
	if err := server.Mount("/api/v1", app); err != nil {
		t.Fatal(err)
	}

	risk := serveApplication(t, server.Handler(), "/api/v1/hazards/landslide")
	if risk.Code != http.StatusAccepted {
		t.Fatalf("风险路由状态 = %d", risk.Code)
	}
	mapResponse := serveApplication(t, server.Handler(), "/api/v1/map/places/nearby")
	if mapResponse.Code != http.StatusCreated {
		t.Fatalf("地图路由状态 = %d", mapResponse.Code)
	}
}

func serveApplication(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}
