package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/application/dashboard"
)

func TestUnversionedScriptsRequireRevalidation(t *testing.T) {
	handler := cacheTestHandler(t)
	for _, target := range []string{"/assets/api.js", "/assets/risk-map.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("GET %s status=%d cache=%q", target, response.Code,
				response.Header().Get("Cache-Control"))
		}
	}
}

func TestConsoleReferencesRevalidatedRiskMapScripts(t *testing.T) {
	response := httptest.NewRecorder()
	cacheTestHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	body := response.Body.String()
	for _, source := range []string{`src="/assets/api.js"`, `src="/assets/risk-map.js"`} {
		if !strings.Contains(body, source) {
			t.Fatalf("控制台缺少脚本引用 %s", source)
		}
	}
}

func cacheTestHandler(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := New(cacheOverviewService{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type cacheOverviewService struct{}

func (cacheOverviewService) Overview(context.Context) dashboard.Overview {
	return dashboard.Overview{}
}
