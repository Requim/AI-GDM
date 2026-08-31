package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Requim/AI-GDM/internal/platform/httpserver"
)

func TestApplicationAPIDispatchesRiskAndMapPrefixes(t *testing.T) {
	server := newTestHTTPServer(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		survival: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/cases" {
				t.Fatalf("生还回放适配器收到错误路径: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		}),
		ai: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/report" {
				t.Fatalf("智能研判适配器收到错误路径: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusResetContent)
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
	survivalResponse := serveApplication(t, server.Handler(), "/api/v1/survival/cases")
	if survivalResponse.Code != http.StatusNoContent {
		t.Fatalf("生还回放路由状态 = %d", survivalResponse.Code)
	}
	aiResponse := serveApplication(t, server.Handler(), "/api/v1/ai/report")
	if aiResponse.Code != http.StatusResetContent {
		t.Fatalf("智能研判路由状态 = %d", aiResponse.Code)
	}
}

func TestApplicationAPIDispatchesNestedChiRouters(t *testing.T) {
	server := newTestHTTPServer(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := server.Mount("/api/v1", applicationAPI{
		hazard:   routeTestRouter(t, http.MethodGet, "/hazards/{hazardType}", "hazardType", "landslide", http.StatusOK),
		mapAPI:   routeTestRouter(t, http.MethodPost, "/places/nearby", "", "", http.StatusCreated),
		survival: routeTestRouter(t, http.MethodGet, "/cases/{caseID}", "caseID", "case-1", http.StatusNoContent),
		loss:     routeTestRouter(t, http.MethodGet, "/assessments/{assessmentID}", "assessmentID", "loss-1", http.StatusAccepted),
		ai:       routeTestRouter(t, http.MethodPost, "/report", "", "", http.StatusResetContent),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/hazards/landslide", http.StatusOK},
		{http.MethodPost, "/api/v1/map/places/nearby", http.StatusCreated},
		{http.MethodGet, "/api/v1/survival/cases/case-1", http.StatusNoContent},
		{http.MethodGet, "/api/v1/loss/assessments/loss-1", http.StatusAccepted},
		{http.MethodPost, "/api/v1/ai/report", http.StatusResetContent},
		{http.MethodGet, "/api/v1/map/places/nearby", http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		response := serveApplicationRequest(t, server.Handler(), test.method, test.path)
		if response.Code != test.want {
			t.Fatalf("%s %s 状态=%d want=%d body=%s", test.method, test.path,
				response.Code, test.want, response.Body.String())
		}
	}
}

func routeTestRouter(t *testing.T, method, pattern, parameter, expected string, status int) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	router.Method(method, pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if parameter != "" && chi.URLParam(r, parameter) != expected {
			t.Errorf("路由参数 %s=%q want=%q", parameter, chi.URLParam(r, parameter), expected)
		}
		w.WriteHeader(status)
	}))
	return router
}

const testAdminToken = "0123456789abcdef0123456789abcdef"

func newTestHTTPServer(t *testing.T, logger *slog.Logger) *httpserver.Server {
	t.Helper()
	server, err := httpserver.New(":0", time.Second, logger, httpserver.SecurityOptions{
		AdminToken: testAdminToken, RateLimitPerMinute: 60_000, RateLimitBurst: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func serveApplication(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return serveApplicationRequest(t, handler, http.MethodGet, path)
}

func serveApplicationRequest(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+testAdminToken)
		request.Header.Set(httpserver.CSRFHeaderName, httpserver.CSRFHeaderValue)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}
