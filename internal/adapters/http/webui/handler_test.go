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
		!strings.Contains(body, `id="admin-auth-form"`) || !strings.Contains(body, `type="password"`) ||
		!strings.Contains(body, `/assets/risk-map.js`) || !strings.Contains(body, `/assets/evacuation.js`) ||
		!strings.Contains(body, `/assets/assessment.js`) ||
		!strings.Contains(body, `data-survival-cases-endpoint="/api/v1/survival/cases"`) ||
		!strings.Contains(body, `data-request-timeout-ms="30000"`) ||
		!strings.Contains(body, `data-ai-request-timeout-ms="45000"`) ||
		strings.Contains(body, "<script>alert") ||
		!strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("页面内容不符合预期: %s", body)
	}
	if strings.Contains(body, `<form id="admin-auth-form"`) ||
		strings.Contains(body, `<form id="evacuation-form"`) ||
		strings.Contains(body, `<form id="loss-assessment-form"`) ||
		strings.Contains(body, `name="admin-token"`) ||
		strings.Contains(body, `name="originLongitude"`) || strings.Contains(body, `name="originLatitude"`) ||
		strings.Contains(body, `name="destinationLongitude"`) || strings.Contains(body, `name="destinationLatitude"`) ||
		strings.Contains(body, `name="snapshotId"`) ||
		!strings.Contains(body, `placeholder="输入管理员令牌" disabled`) ||
		!strings.Contains(body, `id="admin-auth-submit" type="button" disabled`) ||
		!strings.Contains(body, `id="admin-auth-clear" type="button" disabled`) ||
		!strings.Contains(body, `id="route-plan" class="primary-command" type="button"`) ||
		!strings.Contains(body, `id="loss-assessment-run" class="primary-command" type="button"`) {
		t.Fatalf("管理员令牌控件不得具有默认 GET 提交语义: %s", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("控制台页面缺少 no-store")
	}
}

func TestHandlerRendersObservationTimesAndUnknownLiveEvents(t *testing.T) {
	service := &serviceStub{overview: dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC), Environment: "test", Version: "v0",
		Sources: []dashboard.SourceStatus{{ID: "weather", Name: "降雨与土壤湿度", Provider: "Open-Meteo",
			Category: "气象", State: dashboard.StateDegraded, UpdatedAt: time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC),
			LastAttemptAt: time.Date(2026, 8, 30, 3, 30, 0, 0, time.UTC),
			LastSuccessAt: time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC),
			ValidTo:       time.Date(2026, 8, 30, 5, 0, 0, 0, time.UTC), Detail: "最近业务调用已降级"},
			{ID: "live-events", Name: "实时事件目录", Provider: "未接入", Category: "事件",
				State: dashboard.StateUnknown, Detail: "未接入经核验的实时事件源，无法判断当前是否存在实时事件"}},
		Summary: dashboard.Summary{Attention: 2},
	}}
	body := serve(t, newTestHandler(t, service), "/").Body.String()
	for _, required := range []string{"业务可用", "status-degraded", "已降级", "status-unknown", "未知",
		"数据时间", "最近尝试", "最近成功", "2026-08-30 11:30:00 UTC&#43;8",
		"未接入经核验的实时事件源，无法判断当前是否存在实时事件"} {
		if !strings.Contains(body, required) {
			t.Fatalf("控制台缺少 %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"没有事件", "没有灾害"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("控制台不得推断 %q", forbidden)
		}
	}
}

func TestHandlerServesEmbeddedAssets(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/assets/app.css", contentType: "text/css", contains: ".status-degraded"},
		{path: "/assets/evacuation.css", contentType: "text/css", contains: ".evacuation-workspace"},
		{path: "/assets/assessment.css", contentType: "text/css", contains: ".assessment-workspace"},
		{path: "/assets/api.js", contentType: "text/javascript", contains: "X-CSRF-Token"},
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

func TestEmbeddedAdminAuthorizationUsesMemoryOnly(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	script := serve(t, handler, "/assets/api.js").Body.String()
	for _, required := range []string{"let adminToken", `credentials: "omit"`,
		"setAdminAuthorization", "clearAdminAuthorization", "sameOriginEndpoint"} {
		if !strings.Contains(script, required) {
			t.Fatalf("api.js 缺少 %q", required)
		}
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "document.cookie"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("api.js 不得使用 %s 保存管理员令牌", forbidden)
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
