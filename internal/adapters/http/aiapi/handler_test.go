package aiapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/go-chi/chi/v5/middleware"
)

func TestAIReportReturnsAuditableResult(t *testing.T) {
	reporter := &reporterStub{result: validResult()}
	handler, err := New(reporter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	body := "{\"query\":\"四川 滑坡\",\"analysis\":{\"riskLevel\":\"high\"},\"immutableFields\":[\"riskLevel\"]}"
	response := request(t, handler, http.MethodPost, "/report", body)
	if response.Code != http.StatusOK || reporter.input.Query != "四川 滑坡" {
		t.Fatalf("status=%d input=%+v body=%s", response.Code, reporter.input, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "analysisSha256") ||
		strings.Contains(response.Body.String(), "Authorization") {
		t.Fatalf("响应未满足审计边界: %s", response.Body.String())
	}
}

func TestAIReportRejectsUnknownAndTrailingJSON(t *testing.T) {
	handler, err := New(&reporterStub{result: validResult()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	unknown := request(t, handler, http.MethodPost, "/report", "{\"query\":\"滑坡\",\"analysis\":{},\"immutableFields\":[\"riskLevel\"],\"unknown\":true}")
	assertCode(t, unknown, http.StatusBadRequest, "invalid_request")
	trailing := request(t, handler, http.MethodPost, "/report", "{\"query\":\"滑坡\",\"analysis\":{},\"immutableFields\":[\"riskLevel\"]}{}")
	assertCode(t, trailing, http.StatusBadRequest, "invalid_request")
}

func TestAIReportMapsContextAndProviderErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
	}{
		{name: "timeout", err: context.DeadlineExceeded, code: http.StatusGatewayTimeout},
		{name: "provider", err: domain.ErrProviderUnavailable, code: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := New(&reporterStub{err: test.err}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			response := request(t, handler, http.MethodPost, "/report", "{\"query\":\"滑坡\",\"analysis\":{},\"immutableFields\":[\"riskLevel\"]}")
			if response.Code != test.code {
				t.Fatalf("status=%d want=%d", response.Code, test.code)
			}
		})
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(nil, logger); err == nil {
		t.Fatal("空编排服务未被拒绝")
	}
	if _, err := New(&reporterStub{}, nil); err == nil {
		t.Fatal("空日志器未被拒绝")
	}
}

type reporterStub struct {
	result applicationagent.Result
	err    error
	input  applicationagent.Input
}

func (s *reporterStub) Generate(_ context.Context, input applicationagent.Input) (applicationagent.Result, error) {
	s.input = input
	return s.result, s.err
}

func validResult() applicationagent.Result {
	analysis := json.RawMessage("{\"riskLevel\":\"high\"}")
	digest := sha256.Sum256(analysis)
	return applicationagent.Result{
		Query: "四川 滑坡", AnalysisJSON: analysis,
		AnalysisSHA256: hex.EncodeToString(digest[:]), ImmutableFields: []string{"riskLevel"},
		Evidence: []report.Evidence{}, Narrative: report.Narrative{Summary: "说明"},
		Limitations: []string{"仅供人工复核"},
		GeneratedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	middleware.RequestID(handler).ServeHTTP(recorder, req)
	return recorder
}

func assertCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code {
		t.Fatalf("code=%q want=%q", payload.Error.Code, code)
	}
}
