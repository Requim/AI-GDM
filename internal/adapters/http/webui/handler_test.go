package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/dashboard"
)

func TestHandlerRendersEscapedChineseConsole(t *testing.T) {
	service := &serviceStub{overview: dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC), Environment: "test", Version: "v0",
		Sources: []dashboard.SourceStatus{{ID: "source", Name: "风险数据", Provider: "<script>alert(1)</script>",
			Category: "风险", State: dashboard.StateAvailable, Detail: "最后成功数据"}},
		Summary: dashboard.Summary{Available: 1},
	}}
	handler := newTestHandler(t, service)
	response := serve(t, handler, "/")
	if response.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, service.calls)
	}
	body := response.Body.String()
	if !strings.Contains(body, "监控中心控制台") || strings.Contains(body, "<script>alert") ||
		!strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("页面内容不符合预期: %s", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("控制台页面缺少 no-store")
	}
}

func TestHandlerServesEmbeddedStyles(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	response := serve(t, handler, "/assets/app.css")
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/css") ||
		!strings.Contains(response.Body.String(), ".source-table") {
		t.Fatalf("静态样式响应无效: status=%d", response.Code)
	}
}

func newTestHandler(t *testing.T, service OverviewService) http.Handler {
	t.Helper()
	handler, err := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serve(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

type serviceStub struct {
	overview dashboard.Overview
	calls    int
}

func (s *serviceStub) Overview(context.Context) dashboard.Overview {
	s.calls++
	return s.overview
}
