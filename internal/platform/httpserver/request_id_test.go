package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/survivalapi"
	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

func TestInvalidRequestIDStopsApplicationAPIsBeforeSideEffects(t *testing.T) {
	values := []string{strings.Repeat("r", 4097), "bad/request", "invalid-编号"}
	for _, value := range values {
		t.Run(requestIDCaseName(value), func(t *testing.T) {
			server, logOutput, application := requestIDTestServer(t)
			assertRejectedApplicationRequest(t, server.Handler(), logOutput, application,
				http.MethodPost, "/api/v1/ai/report", `{}`, value)
			assertRejectedApplicationRequest(t, server.Handler(), logOutput, application,
				http.MethodGet, "/api/v1/survival/cases", "", value)
			assertRejectedApplicationRequest(t, server.Handler(), logOutput, application,
				http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`, value)
		})
	}
}

func TestRequestIDMiddlewareKeepsOnlyBoundedASCIIIdentifiers(t *testing.T) {
	server := newTestServer(t)
	valid := "client.Request-42:alpha"
	response := requestWithID(server.Handler(), http.MethodGet, "/healthz", "", valid)
	if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") != valid {
		t.Fatalf("合法 requestID 未原样保留: status=%d id=%q", response.Code, response.Header().Get("X-Request-ID"))
	}
	generated := requestWithID(server.Handler(), http.MethodGet, "/healthz", "", "")
	if id := generated.Header().Get("X-Request-ID"); !validRequestID(id) {
		t.Fatalf("服务端生成了非规范 requestID: %q", id)
	}
}

func TestMaximumRequestIDKeepsApplicationErrorsWithinWireBudget(t *testing.T) {
	server, _, application := requestIDTestServer(t)
	requestID := strings.Repeat("r", maxRequestIDBytes)
	assertBoundedApplicationError(t, server.Handler(), application,
		http.MethodPost, "/api/v1/ai/report", `{`, requestID)
	assertBoundedApplicationError(t, server.Handler(), application,
		http.MethodGet, "/api/v1/survival/not-found", "", requestID)
}

func TestInvalidRequestIDFloodIsRateLimitedBeforeRejectionAudit(t *testing.T) {
	logOutput := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))
	calls := 0
	server, err := New(":0", time.Second, logger, SecurityOptions{
		AdminToken: securityTestAdminToken, RateLimitPerMinute: 20, RateLimitBurst: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 25; index++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
		request.Header.Add("X-Request-ID", "first")
		request.Header.Add("X-Request-ID", "second")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		want := http.StatusBadRequest
		if index >= 20 {
			want = http.StatusTooManyRequests
		}
		assertRequestIDFloodResponse(t, response, want)
	}
	if calls != 0 {
		t.Fatalf("非法 requestID 洪泛进入业务 %d 次", calls)
	}
	if records := strings.Count(logOutput.String(), "\n"); records != 20 {
		t.Fatalf("拒绝审计未受限流预算约束: records=%d", records)
	}
	if strings.Contains(logOutput.String(), "first") || strings.Contains(logOutput.String(), "second") {
		t.Fatal("拒绝审计泄露非法 requestID")
	}
}

func TestInvalidRequestIDFloodOnPublicPathsConsumesSecurityBudget(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "health", path: "/healthz"},
		{name: "readiness", path: "/readyz"},
		{name: "web", path: "/"},
		{name: "not_found", path: "/missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidPublicRequestIDFlood(t, test.path)
		})
	}
}

func TestPublicRequestsConsumeSecurityBudget(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
		webCalls   int
	}{
		{name: "health", path: "/healthz", wantStatus: http.StatusOK},
		{name: "readiness", path: "/readyz", wantStatus: http.StatusServiceUnavailable},
		{name: "web", path: "/", wantStatus: http.StatusNoContent, webCalls: 20},
		{name: "not_found", path: "/missing", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, logOutput, calls := publicRequestIDTestServer(t, test.path == "/")
			for index := 0; index < 25; index++ {
				request := httptest.NewRequest(http.MethodGet, test.path, nil)
				if index%2 == 0 {
					request.Header.Set("X-Request-ID", fmt.Sprintf("public-%d", index))
				}
				response := serveRequest(server.Handler(), request)
				want := test.wantStatus
				if index >= 20 {
					want = http.StatusTooManyRequests
				}
				if response.Code != want {
					t.Fatalf("index=%d status=%d want=%d", index, response.Code, want)
				}
				if want == http.StatusTooManyRequests {
					assertRequestIDFloodResponse(t, response, want)
				}
			}
			if *calls != test.webCalls || strings.Count(logOutput.String(), "\n") != 20 {
				t.Fatalf("calls=%d logs=%d", *calls, strings.Count(logOutput.String(), "\n"))
			}
		})
	}
}

func TestLongNotFoundPathsUseBoundedAuditDigest(t *testing.T) {
	server, logOutput, _ := publicRequestIDTestServer(t, false)
	path := "/missing/" + strings.Repeat("sensitive-path-segment-", 800)
	for index := 0; index < 25; index++ {
		response := serveRequest(server.Handler(), httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusNotFound
		if index >= 20 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("index=%d status=%d want=%d", index, response.Code, want)
		}
	}
	digest := sha256.Sum256([]byte(path))
	auditPath := fmt.Sprintf("sha256:%x", digest[:])
	logs := logOutput.String()
	if strings.Count(logs, "\n") != 20 || strings.Contains(logs, path) || !strings.Contains(logs, auditPath) {
		t.Fatalf("长路径审计无效: logs=%d bytes=%d digest=%t raw=%t",
			strings.Count(logs, "\n"), len(logs), strings.Contains(logs, auditPath), strings.Contains(logs, path))
	}
	if len(logs) > 20*1024 {
		t.Fatalf("长路径审计超过总预算: bytes=%d", len(logs))
	}
}

func assertInvalidPublicRequestIDFlood(t *testing.T, path string) {
	t.Helper()
	server, logOutput, calls := publicRequestIDTestServer(t, path == "/")
	for index := 0; index < 25; index++ {
		response := duplicateRequestIDRequest(server.Handler(), path)
		want := http.StatusBadRequest
		if index >= 20 {
			want = http.StatusTooManyRequests
		}
		assertRequestIDFloodResponse(t, response, want)
	}
	if *calls != 0 || strings.Count(logOutput.String(), "\n") != 20 {
		t.Fatalf("path=%s calls=%d logs=%d", path, *calls, strings.Count(logOutput.String(), "\n"))
	}
	if strings.Contains(logOutput.String(), "first") || strings.Contains(logOutput.String(), "second") {
		t.Fatalf("path=%s 拒绝审计泄露非法 requestID", path)
	}
}

func publicRequestIDTestServer(t *testing.T, mountWeb bool) (*Server, *bytes.Buffer, *int) {
	t.Helper()
	logOutput := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))
	server, err := New(":0", time.Second, logger, SecurityOptions{
		AdminToken: securityTestAdminToken, RateLimitPerMinute: 20, RateLimitBurst: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	if mountWeb {
		err = server.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*calls++
			w.WriteHeader(http.StatusNoContent)
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
	return server, logOutput, calls
}

func duplicateRequestIDRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Add("X-Request-ID", "first")
	request.Header.Add("X-Request-ID", "second")
	return serveRequest(handler, request)
}

func assertRequestIDFloodResponse(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	requestID := response.Header().Get("X-Request-ID")
	if response.Code != want || !validRequestID(requestID) || requestID == "first" || requestID == "second" ||
		!strings.Contains(response.Body.String(), requestID) {
		t.Fatalf("洪泛响应无效: status=%d want=%d id=%q body=%s",
			response.Code, want, requestID, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("洪泛响应缺少安全响应头")
	}
	if want == http.StatusTooManyRequests && response.Header().Get("Retry-After") != "60" {
		t.Fatalf("429 缺少 Retry-After: %v", response.Header())
	}
}

func requestIDTestServer(t *testing.T) (*Server, *bytes.Buffer, *rejectedApplication) {
	t.Helper()
	logOutput := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))
	server, err := New(":0", time.Second, logger, SecurityOptions{
		AdminToken: securityTestAdminToken, RateLimitPerMinute: 60_000, RateLimitBurst: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	application := &rejectedApplication{}
	aiHandler, err := aiapi.New(application, logger)
	if err != nil {
		t.Fatal(err)
	}
	survivalHandler, err := survivalapi.New(application, application, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Mount("/api/v1/ai", aiHandler); err != nil {
		t.Fatal(err)
	}
	if err = server.Mount("/api/v1/survival", survivalHandler); err != nil {
		t.Fatal(err)
	}
	lossHandler, err := lossapi.New(application, application, application, "/api/v1/loss", logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Mount("/api/v1/loss", lossHandler); err != nil {
		t.Fatal(err)
	}
	return server, logOutput, application
}

func assertRejectedApplicationRequest(t *testing.T, handler http.Handler, logOutput *bytes.Buffer,
	application *rejectedApplication, method, path, body, rawID string,
) {
	t.Helper()
	before, logStart := application.calls, logOutput.Len()
	response := requestWithID(handler, method, path, body, rawID)
	if response.Code != http.StatusBadRequest || application.calls != before {
		t.Fatalf("非法 requestID 进入业务: status=%d calls=%d body=%s",
			response.Code, application.calls-before, response.Body.String())
	}
	responseID := response.Header().Get("X-Request-ID")
	if !validRequestID(responseID) || responseID == rawID || response.Header().Get("Location") != "" ||
		response.Body.Len() > 1<<20 || !strings.Contains(response.Body.String(), responseID) {
		t.Fatalf("非法 requestID 响应越界: headers=%v bytes=%d", response.Header(), response.Body.Len())
	}
	logRecord := logOutput.String()[logStart:]
	if strings.Count(logRecord, "\n") != 1 || !strings.Contains(logRecord, responseID) ||
		!strings.Contains(logRecord, `"reason"`) || !strings.Contains(logRecord, `"length_category"`) {
		t.Fatalf("非法 requestID 缺少安全拒绝审计: %s", logRecord)
	}
	if strings.Contains(response.Body.String(), rawID) || strings.Contains(logRecord, rawID) {
		t.Fatal("非法 requestID 泄露到响应或日志")
	}
}

func requestWithID(handler http.Handler, method, path, body, requestID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		request.Header.Set("Authorization", "Bearer "+securityTestAdminToken)
		request.Header.Set(CSRFHeaderName, CSRFHeaderValue)
		request.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertBoundedApplicationError(t *testing.T, handler http.Handler, application *rejectedApplication,
	method, path, body, requestID string,
) {
	t.Helper()
	before := application.calls
	response := requestWithID(handler, method, path, body, requestID)
	if response.Code < http.StatusBadRequest || application.calls != before {
		t.Fatalf("错误请求进入业务: status=%d calls=%d", response.Code, application.calls-before)
	}
	if response.Header().Get("X-Request-ID") != requestID || response.Body.Len() > 1<<20 ||
		!strings.Contains(response.Body.String(), requestID) {
		t.Fatalf("最大 requestID 错误封包越界: headers=%v bytes=%d", response.Header(), response.Body.Len())
	}
}

func requestIDCaseName(value string) string {
	if len(value) > maxRequestIDBytes {
		return "oversized"
	}
	if strings.Contains(value, "/") {
		return "forbidden_ascii"
	}
	return "non_ascii"
}

type rejectedApplication struct{ calls int }

func (a *rejectedApplication) Generate(context.Context,
	applicationagent.Input,
) (applicationagent.Result, error) {
	a.calls++
	return applicationagent.Result{}, nil
}

func (a *rejectedApplication) ListCases(context.Context) ([]applicationsurvival.HistoricalCase, error) {
	a.calls++
	return nil, nil
}

func (a *rejectedApplication) GetCase(context.Context,
	string,
) (applicationsurvival.HistoricalCaseDetail, error) {
	a.calls++
	return applicationsurvival.HistoricalCaseDetail{}, nil
}

func (a *rejectedApplication) AssessCase(context.Context,
	string,
) (applicationsurvival.ReplayAssessment, error) {
	a.calls++
	return applicationsurvival.ReplayAssessment{}, nil
}

func (a *rejectedApplication) Estimate(context.Context,
	applicationloss.EstimateInput,
) (lossdomain.Assessment, error) {
	a.calls++
	return lossdomain.Assessment{}, nil
}

func (a *rejectedApplication) SaveAssessment(context.Context, lossdomain.Assessment) error {
	a.calls++
	return nil
}

func (a *rejectedApplication) GetAssessment(context.Context, string) (lossdomain.Assessment, error) {
	a.calls++
	return lossdomain.Assessment{}, nil
}
