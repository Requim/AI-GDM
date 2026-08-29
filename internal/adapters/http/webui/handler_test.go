package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
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
	if !strings.Contains(body, "监控中心控制台") || !strings.Contains(body, `id="risk-map"`) ||
		!strings.Contains(body, `id="evacuation"`) || !strings.Contains(body, `id="assessment"`) ||
		!strings.Contains(body, `/assets/risk-map.js`) || !strings.Contains(body, `/assets/evacuation.js`) ||
		!strings.Contains(body, `/assets/assessment.js`) ||
		!strings.Contains(body, `data-survival-cases-endpoint="/api/v1/survival/cases"`) ||
		!strings.Contains(body, `data-request-timeout-ms="30000"`) ||
		!strings.Contains(body, `data-ai-request-timeout-ms="45000"`) ||
		strings.Contains(body, "<script>alert") ||
		!strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("页面内容不符合预期: %s", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("控制台页面缺少 no-store")
	}
}

func TestHandlerServesEmbeddedAssets(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/assets/app.css", contentType: "text/css", contains: ".source-table"},
		{path: "/assets/evacuation.css", contentType: "text/css", contains: ".evacuation-workspace"},
		{path: "/assets/assessment.css", contentType: "text/css", contains: ".assessment-workspace"},
		{path: "/assets/api.js", contentType: "text/javascript", contains: "requestJSON"},
		{path: "/assets/risk-map.js", contentType: "text/javascript", contains: "MAX_VISIBLE_ZONES"},
		{path: "/assets/evacuation.js", contentType: "text/javascript", contains: "planRoutes"},
		{path: "/assets/assessment.js", contentType: "text/javascript", contains: "loadModelCard"},
		{path: "/assets/vendor/leaflet/leaflet.js", contentType: "text/javascript", contains: "Leaflet"},
		{path: "/assets/vendor/leaflet/images/layers.png", contentType: "image/png"},
	}
	for _, test := range tests {
		response := serve(t, handler, test.path)
		if response.Code != http.StatusOK ||
			!strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) ||
			(test.contains != "" && !strings.Contains(response.Body.String(), test.contains)) {
			t.Fatalf("静态资源 %s 响应无效: status=%d type=%s", test.path,
				response.Code, response.Header().Get("Content-Type"))
		}
	}
}

func TestHandlerAIRequestBudgetExceedsServerBudget(t *testing.T) {
	response := serve(t, newTestHandler(t, &serviceStub{}), "/")
	match := regexp.MustCompile(`data-ai-request-timeout-ms="([0-9]+)"`).FindStringSubmatch(response.Body.String())
	if len(match) != 2 {
		t.Fatal("页面缺少 AI 独立请求预算")
	}
	milliseconds, err := strconv.Atoi(match[1])
	if err != nil || time.Duration(milliseconds)*time.Millisecond <= aiapi.ReportTimeout {
		t.Fatalf("browser=%dms server=%s error=%v", milliseconds, aiapi.ReportTimeout, err)
	}
}

func TestHandlerRejectsUnknownAsset(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	response := serve(t, handler, "/assets/unknown.js")
	if response.Code != http.StatusNotFound {
		t.Fatalf("未知静态资源 status=%d", response.Code)
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
