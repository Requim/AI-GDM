package httpserver

import (
	"bytes"
	"context"
	"io"
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
	server := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func requestIDTestServer(t *testing.T) (*Server, *bytes.Buffer, *rejectedApplication) {
	t.Helper()
	logOutput := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))
	server := New(":0", time.Second, logger)
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
