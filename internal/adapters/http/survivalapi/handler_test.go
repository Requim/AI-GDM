package survivalapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestReplayAPIListsCasesAndReturnsRequestID(t *testing.T) {
	service := newReplayStub()
	response := performRequest(t, service, service, http.MethodGet, "/cases")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload successResponse
	decode(t, response, &payload)
	if payload.RequestID == "" || response.Header().Get("X-Request-ID") != payload.RequestID {
		t.Fatalf("request id header=%q payload=%q", response.Header().Get("X-Request-ID"), payload.RequestID)
	}
}

func TestReplayAPIRoutesCaseAssessmentAndModelCard(t *testing.T) {
	catalog := &catalogStub{caseValue: applicationsurvival.HistoricalCase{
		Event: survivaldomain.HistoricalEvent{ID: "case-1"}, ScenarioID: "replay-1",
	}}
	assessment := &assessmentStub{value: survivaldomain.Assessment{
		ScenarioID: "scenario-1", ModelVersion: survivaldomain.ModelVersion,
	}}
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/cases/case-1"},
		{http.MethodPost, "/scenarios/scenario-1/assess"},
		{http.MethodGet, "/model-card"},
	} {
		response := performRequest(t, catalog, assessment, test.method, test.path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	if catalog.id != "case-1" || assessment.id != "scenario-1" {
		t.Fatalf("service calls case=%q scenario=%q", catalog.id, assessment.id)
	}
}

func TestReplayAPIRejectsInvalidIDAndMapsNotFound(t *testing.T) {
	catalog := &catalogStub{err: domain.ErrNotFound}
	assessment := &assessmentStub{}
	invalid := performRequest(t, catalog, assessment, http.MethodGet, "/cases/bad%20id")
	assertError(t, invalid, http.StatusBadRequest, "invalid_request")
	missing := performRequest(t, catalog, assessment, http.MethodGet, "/cases/case-1")
	assertError(t, missing, http.StatusNotFound, "replay_not_found")
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(nil, &assessmentStub{}, logger); err == nil {
		t.Fatal("New() 未拒绝空目录服务")
	}
	if _, err := New(&catalogStub{}, nil, logger); err == nil {
		t.Fatal("New() 未拒绝空评估服务")
	}
	if _, err := New(&catalogStub{}, &assessmentStub{}, nil); err == nil {
		t.Fatal("New() 未拒绝空日志器")
	}
}

func performRequest(t *testing.T, catalog applicationsurvival.CatalogService,
	assessment applicationsurvival.AssessmentService, method, path string,
) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := New(catalog, assessment, logger)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	middleware.RequestID(handler).ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func decode(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	decode(t, response, &payload)
	if payload.Error.Code != code || payload.Error.RequestID == "" {
		t.Fatalf("error=%+v", payload.Error)
	}
}

type catalogStub struct {
	caseValue applicationsurvival.HistoricalCase
	err       error
	id        string
}

func (s *catalogStub) ListCases(context.Context) ([]applicationsurvival.HistoricalCase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []applicationsurvival.HistoricalCase{s.caseValue}, nil
}

func (s *catalogStub) GetCase(_ context.Context, id string) (applicationsurvival.HistoricalCase, error) {
	s.id = id
	if s.err != nil {
		return applicationsurvival.HistoricalCase{}, s.err
	}
	return s.caseValue, nil
}

type assessmentStub struct {
	value survivaldomain.Assessment
	err   error
	id    string
}

func (s *assessmentStub) Assess(_ context.Context, id string) (survivaldomain.Assessment, error) {
	s.id = id
	if s.err != nil {
		return survivaldomain.Assessment{}, s.err
	}
	return s.value, nil
}

type replayStub struct {
	catalogStub
	assessmentStub
}

func newReplayStub() *replayStub {
	return &replayStub{catalogStub: catalogStub{caseValue: applicationsurvival.HistoricalCase{
		Event: survivaldomain.HistoricalEvent{ID: "case-1"}, ScenarioID: "replay-1",
	}}, assessmentStub: assessmentStub{value: survivaldomain.Assessment{
		ScenarioID: "scenario-1", ModelVersion: survivaldomain.ModelVersion,
	}}}
}
