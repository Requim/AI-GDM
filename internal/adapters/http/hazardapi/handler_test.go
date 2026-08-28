package hazardapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	applicationhazard "github.com/Requim/AI-GDM/internal/application/hazard"
	"github.com/Requim/AI-GDM/internal/domain"
	domainhazard "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
)

func TestLatestRiskReturnsAssessmentAndRequestID(t *testing.T) {
	service := &riskServiceStub{result: riskResult("snapshot-latest")}
	response := request(t, service, http.MethodGet, "/hazards/landslide/risks/latest")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload successResponse
	decodeResponse(t, response, &payload)
	if payload.Data.Snapshot.ID != "snapshot-latest" || payload.Data.Assessment.ID != "risk-1" {
		t.Fatalf("response data = %+v", payload.Data)
	}
	if payload.RequestID == "" || response.Header().Get("X-Request-ID") != payload.RequestID {
		t.Fatalf("request id header=%q body=%q", response.Header().Get("X-Request-ID"), payload.RequestID)
	}
	if service.operation != "latest" || service.hazardType != domainhazard.TypeLandslide {
		t.Fatalf("service call = %+v", service)
	}
	if payload.Data.Zones == nil {
		t.Fatalf("空风险区未编码为数组: %+v", payload.Data)
	}
}

func TestRiskDetailPassesHazardTypeAndSnapshotID(t *testing.T) {
	service := &riskServiceStub{result: riskResult("snapshot-20260825")}
	response := request(t, service, http.MethodGet,
		"/hazards/debris_flow/risks/snapshot-20260825")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if service.operation != "detail" || service.hazardType != domainhazard.TypeDebrisFlow ||
		service.snapshotID != "snapshot-20260825" {
		t.Fatalf("service call = %+v", service)
	}
}

func TestRefreshReturnsCurrentRiskResult(t *testing.T) {
	service := &riskServiceStub{result: riskResult("snapshot-refreshed")}
	response := request(t, service, http.MethodPost, "/hazards/landslide/refresh")

	if response.Code != http.StatusOK || service.operation != "refresh" {
		t.Fatalf("status=%d operation=%q body=%s",
			response.Code, service.operation, response.Body.String())
	}
}

func TestRiskAPIMapsDomainErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: domain.ErrInvalidInput, status: 400, code: "invalid_request"},
		{name: "unsupported hazard", err: errors.Join(applicationhazard.ErrHazardNotSupported, domain.ErrNotFound),
			status: 404, code: "hazard_not_supported"},
		{name: "missing", err: domain.ErrNotFound, status: 404, code: "risk_not_found"},
		{name: "timeout", err: context.DeadlineExceeded, status: 504, code: "request_timeout"},
		{name: "canceled", err: context.Canceled, status: 408, code: "request_canceled"},
		{name: "provider", err: domain.ErrProviderUnavailable, status: 503, code: "provider_unavailable"},
		{name: "insufficient", err: domain.ErrInsufficientData, status: 503, code: "insufficient_data"},
		{name: "internal", err: errors.New("数据库口令 secret-value"), status: 500, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &riskServiceStub{err: test.err}
			response := request(t, service, http.MethodGet, "/hazards/landslide/risks/latest")
			assertAPIError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "secret-value") {
				t.Fatalf("响应泄漏内部错误: %s", response.Body.String())
			}
		})
	}
}

func TestRiskAPIRejectsInvalidPathParametersBeforeApplicationCall(t *testing.T) {
	tests := []string{
		"/hazards/Landslide/risks/latest",
		"/hazards/landslide/risks/bad%20snapshot",
	}
	for _, path := range tests {
		service := &riskServiceStub{}
		response := request(t, service, http.MethodGet, path)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
		if service.calls != 0 {
			t.Fatalf("路径 %q 仍调用了应用服务", path)
		}
	}
}

func TestRiskAPIReturnsJSONForUnknownRouteAndMethod(t *testing.T) {
	service := &riskServiceStub{}
	unknown := request(t, service, http.MethodGet, "/unknown")
	assertAPIError(t, unknown, http.StatusNotFound, "route_not_found")

	method := request(t, service, http.MethodDelete, "/hazards/landslide/risks/latest")
	assertAPIError(t, method, http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(nil, logger); err == nil {
		t.Fatal("New() 未拒绝空应用服务")
	}
	if _, err := New(&riskServiceStub{}, nil); err == nil {
		t.Fatal("New() 未拒绝空日志器")
	}
}

func request(t *testing.T, service RiskService, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := New(service, logger)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	middleware.RequestID(handler).ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v, body=%s", err, response.Body.String())
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	decodeResponse(t, response, &payload)
	if payload.Error.Code != code || payload.Error.Message == "" || payload.Error.RequestID == "" {
		t.Fatalf("error response = %+v", payload.Error)
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response headers = %v", response.Header())
	}
}

func riskResult(snapshotID string) applicationhazard.RiskResult {
	return applicationhazard.RiskResult{
		Snapshot:   domainhazard.Snapshot{ID: snapshotID, HazardType: domainhazard.TypeLandslide},
		Assessment: risk.Assessment{ID: "risk-1", SnapshotID: snapshotID},
	}
}

type riskServiceStub struct {
	result      applicationhazard.RiskResult
	mapResult   *applicationhazard.MapRiskResult
	err         error
	operation   string
	hazardType  domainhazard.Type
	snapshotID  string
	mapMaxZones int
	calls       int
}

func (s *riskServiceStub) Latest(_ context.Context,
	hazardType domainhazard.Type,
) (applicationhazard.RiskResult, error) {
	s.record("latest", hazardType, "")
	return s.result, s.err
}

func (s *riskServiceStub) LatestMap(_ context.Context, hazardType domainhazard.Type,
	maxZones int,
) (applicationhazard.MapRiskResult, error) {
	s.record("latest-map", hazardType, "")
	s.mapMaxZones = maxZones
	if s.mapResult != nil {
		return *s.mapResult, s.err
	}
	total := len(s.result.Zones)
	if total > maxZones {
		return applicationhazard.MapRiskResult{}, domain.ErrInsufficientData
	}
	return applicationhazard.MapRiskResult{RiskResult: s.result, TotalZoneCount: total}, s.err
}

func (s *riskServiceStub) Get(_ context.Context, hazardType domainhazard.Type,
	snapshotID string,
) (applicationhazard.RiskResult, error) {
	s.record("detail", hazardType, snapshotID)
	return s.result, s.err
}

func (s *riskServiceStub) Refresh(_ context.Context,
	hazardType domainhazard.Type,
) (applicationhazard.RiskResult, error) {
	s.record("refresh", hazardType, "")
	return s.result, s.err
}

func (s *riskServiceStub) record(operation string, hazardType domainhazard.Type, snapshotID string) {
	s.calls++
	s.operation = operation
	s.hazardType = hazardType
	s.snapshotID = snapshotID
}
