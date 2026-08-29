package survivalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/adapters/storage/memory"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestReplayAPIListsCaseSummariesAndReturnsRequestID(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	catalog := &catalogStub{cases: []applicationsurvival.HistoricalCase{
		{Event: detail.Event, ScenarioID: detail.Scenario.ID},
	}, detail: detail}
	response := performRequest(t, catalog, &assessmentStub{}, http.MethodGet, "/cases", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload := decodeSuccess[[]applicationsurvival.HistoricalCase](t, response)
	if payload.RequestID == "" || len(payload.Data) != 1 || payload.Data[0].ScenarioID != detail.Scenario.ID {
		t.Fatalf("payload=%+v", payload)
	}
	if response.Header().Get("X-Request-ID") != payload.RequestID {
		t.Fatalf("request id header=%q payload=%q", response.Header().Get("X-Request-ID"), payload.RequestID)
	}
}

func TestReplayAPIRoutesDetailAssessmentAndModelCard(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	replay := validReplayAssessment(t, detail)
	catalog := &catalogStub{detail: detail}
	assessment := &assessmentStub{value: replay}
	assertDetailRoute(t, catalog, assessment, detail)
	assertAssessmentRoute(t, catalog, assessment, replay)
	model := performRequest(t, catalog, assessment, http.MethodGet, "/model-card", nil)
	if model.Code != http.StatusOK {
		t.Fatalf("model card status=%d body=%s", model.Code, model.Body.String())
	}
	modelPayload := decodeSuccess[survivaldomain.ModelCard](t, model)
	if modelPayload.Data.ModelVersion != replay.Assessment.ModelVersion {
		t.Fatalf("model card=%s assessment=%s", modelPayload.Data.ModelVersion, replay.Assessment.ModelVersion)
	}
	legacy := performRequest(t, catalog, assessment, http.MethodPost, "/scenarios/replay-case-1/assess", nil)
	assertError(t, legacy, http.StatusNotFound, "route_not_found")
}

func TestReplayAPIRejectsNonEmptyBodyBeforeAssessment(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	assessment := &assessmentStub{value: validReplayAssessment(t, detail)}
	response := performRequest(t, &catalogStub{detail: detail}, assessment, http.MethodPost,
		"/replays/cases/case-1/assessment", bytes.NewBufferString("{}"))
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	if assessment.calls != 0 {
		t.Fatalf("non-empty body triggered %d assessment calls", assessment.calls)
	}
}

func TestReplayAPIValidatesAssessmentBeforeWriting(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	value := validReplayAssessment(t, detail)
	value.Assessment.HumanReviewStatus = "approved"
	response := performRequest(t, &catalogStub{detail: detail}, &assessmentStub{value: value}, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestReplayAPIRejectsCrossCaseAndWrongDigestBinding(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	base := validReplayAssessment(t, detail)
	crossCase, err := applicationsurvival.NewReplayAssessment("case-other", detail.ScenarioDigest, base.Assessment)
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(t, &catalogStub{detail: detail}, &assessmentStub{value: crossCase}, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
	wrongDigest, err := applicationsurvival.NewReplayAssessment("case-1",
		"sha256:"+strings.Repeat("0", 64), base.Assessment)
	if err != nil {
		t.Fatal(err)
	}
	response = performRequest(t, &catalogStub{detail: detail}, &assessmentStub{value: wrongDigest}, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestReplayAPIRejectsAssessmentBeforeScenario(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	assessment, err := survivaldomain.Evaluate(detail.Scenario, detail.Scenario.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	assessment.CalculatedAt = detail.Scenario.AsOf.Add(-time.Second)
	value, err := applicationsurvival.NewReplayAssessment(detail.Event.ID, detail.ScenarioDigest, assessment)
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(t, &catalogStub{detail: detail}, &assessmentStub{value: value}, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestReplayAPIRejectsInvalidIDAndMapsNotFound(t *testing.T) {
	catalog := &catalogStub{err: domain.ErrNotFound}
	invalid := performRequest(t, catalog, &assessmentStub{}, http.MethodGet, "/cases/bad%20id", nil)
	assertError(t, invalid, http.StatusBadRequest, "invalid_request")
	missing := performRequest(t, catalog, &assessmentStub{}, http.MethodGet, "/cases/case-1", nil)
	assertError(t, missing, http.StatusNotFound, "replay_not_found")
}

func TestReplayAPIRealListDetailAssessmentChain(t *testing.T) {
	catalog, err := memory.NewSurvivalCatalog(time.Date(2026, 8, 27, 7, 8, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	cases, err := applicationsurvival.NewCatalogService(catalog)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := applicationsurvival.NewAssessmentService(catalog, httpFixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	assertRealReplayChain(t, cases, assessment)
}

func TestReplayAPIRejectsOverBudgetServiceResults(t *testing.T) {
	detail := validCaseDetail(t, "case-1")
	overflow := make([]applicationsurvival.HistoricalCase, applicationsurvival.MaxCatalogCases+1)
	response := performRequest(t, &catalogStub{cases: overflow}, &assessmentStub{}, http.MethodGet, "/cases", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")

	longDetail := detail
	longDetail.Event.Source.Citation = strings.Repeat("x", 2<<20)
	response = performRequest(t, &catalogStub{detail: longDetail}, &assessmentStub{}, http.MethodGet,
		"/cases/case-1", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")

	replay := validReplayAssessment(t, detail)
	replay.Assessment.Factors[0] = strings.Repeat("x", 2<<20)
	response = performRequest(t, &catalogStub{detail: detail}, &assessmentStub{value: replay}, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestReplayAPIRejectsFinalWireBudget(t *testing.T) {
	values := make([]applicationsurvival.HistoricalCase, 13)
	for index := range values {
		caseID := fmt.Sprintf("case-wire-%d", index)
		event := validHTTPEvent(caseID)
		event.Limitations = make([]string, 32)
		for limitationIndex := range event.Limitations {
			event.Limitations[limitationIndex] = strings.Repeat("\x01", 1000)
		}
		values[index] = applicationsurvival.HistoricalCase{Event: event, ScenarioID: "replay-" + caseID}
	}
	response := performRequest(t, &catalogStub{cases: values}, &assessmentStub{}, http.MethodGet, "/cases", nil)
	assertError(t, response, http.StatusInternalServerError, "internal_error")
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

func assertDetailRoute(t *testing.T, catalog applicationsurvival.CatalogService,
	assessment applicationsurvival.AssessmentService, expected applicationsurvival.HistoricalCaseDetail,
) {
	t.Helper()
	response := performRequest(t, catalog, assessment, http.MethodGet, "/cases/case-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", response.Code, response.Body.String())
	}
	payload := decodeSuccess[applicationsurvival.HistoricalCaseDetail](t, response)
	if payload.Data.Scenario.CaseID != "case-1" || payload.Data.ScenarioDigest != expected.ScenarioDigest ||
		payload.Data.Usage.LiveUseAllowed {
		t.Fatalf("detail=%+v", payload.Data)
	}
}

func assertAssessmentRoute(t *testing.T, catalog applicationsurvival.CatalogService,
	assessment applicationsurvival.AssessmentService, expected applicationsurvival.ReplayAssessment,
) {
	t.Helper()
	response := performRequest(t, catalog, assessment, http.MethodPost,
		"/replays/cases/case-1/assessment", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("assessment status=%d body=%s", response.Code, response.Body.String())
	}
	payload := decodeSuccess[applicationsurvival.ReplayAssessment](t, response)
	if payload.Data.AssessmentID == "" || payload.Data.CaseID != "case-1" ||
		payload.Data.ScenarioDigest != expected.ScenarioDigest ||
		payload.Data.Usage.LiveUseAllowed || payload.Data.Assessment.HumanReviewStatus != "required" {
		t.Fatalf("assessment=%+v", payload.Data)
	}
}

func assertRealReplayChain(t *testing.T, catalog applicationsurvival.CatalogService,
	assessment applicationsurvival.AssessmentService,
) {
	t.Helper()
	list := performRequest(t, catalog, assessment, http.MethodGet, "/cases", nil)
	cases := decodeSuccess[[]applicationsurvival.HistoricalCase](t, list).Data
	if len(cases) == 0 {
		t.Fatal("real catalog returned no cases")
	}
	caseID := cases[0].Event.ID
	detailResponse := performRequest(t, catalog, assessment, http.MethodGet, "/cases/"+caseID, nil)
	detail := decodeSuccess[applicationsurvival.HistoricalCaseDetail](t, detailResponse).Data
	replayResponse := performRequest(t, catalog, assessment, http.MethodPost,
		"/replays/cases/"+caseID+"/assessment", nil)
	replay := decodeSuccess[applicationsurvival.ReplayAssessment](t, replayResponse).Data
	if detail.Scenario.ID != replay.ScenarioID || detail.ScenarioDigest != replay.ScenarioDigest ||
		detail.Event.ID != replay.CaseID || detail.Usage != replay.Usage || replay.AssessmentID == "" {
		t.Fatalf("detail=%+v replay=%+v", detail, replay)
	}
	if err := replay.Validate(); err != nil {
		t.Fatalf("replay validation=%v", err)
	}
}

func performRequest(t *testing.T, catalog applicationsurvival.CatalogService,
	assessment applicationsurvival.AssessmentService, method, path string, body io.Reader,
) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := New(catalog, assessment, logger)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	middleware.RequestID(handler).ServeHTTP(response, httptest.NewRequest(method, path, body))
	return response
}

type typedSuccess[T any] struct {
	Data      T      `json:"data"`
	RequestID string `json:"requestId"`
}

func decodeSuccess[T any](t *testing.T, response *httptest.ResponseRecorder) typedSuccess[T] {
	t.Helper()
	var payload typedSuccess[T]
	decode(t, response, &payload)
	return payload
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
	cases  []applicationsurvival.HistoricalCase
	detail applicationsurvival.HistoricalCaseDetail
	err    error
	id     string
}

func (s *catalogStub) ListCases(context.Context) ([]applicationsurvival.HistoricalCase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]applicationsurvival.HistoricalCase(nil), s.cases...), nil
}

func (s *catalogStub) GetCase(_ context.Context, id string) (applicationsurvival.HistoricalCaseDetail, error) {
	s.id = id
	if s.err != nil {
		return applicationsurvival.HistoricalCaseDetail{}, s.err
	}
	return s.detail, nil
}

type assessmentStub struct {
	value applicationsurvival.ReplayAssessment
	err   error
	id    string
	calls int
}

func (s *assessmentStub) AssessCase(_ context.Context, id string) (applicationsurvival.ReplayAssessment, error) {
	s.id, s.calls = id, s.calls+1
	if s.err != nil {
		return applicationsurvival.ReplayAssessment{}, s.err
	}
	return s.value, nil
}

func validCaseDetail(t *testing.T, caseID string) applicationsurvival.HistoricalCaseDetail {
	t.Helper()
	scenario := validHTTPScenario(caseID)
	digest, err := scenario.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return applicationsurvival.HistoricalCaseDetail{Event: validHTTPEvent(caseID), Scenario: scenario,
		ScenarioDigest: digest, Usage: survivaldomain.HistoricalReplayUsage()}
}

func validReplayAssessment(t *testing.T,
	detail applicationsurvival.HistoricalCaseDetail,
) applicationsurvival.ReplayAssessment {
	t.Helper()
	assessment, err := survivaldomain.Evaluate(detail.Scenario, httpFixedClock{}.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := applicationsurvival.NewReplayAssessment(detail.Event.ID, detail.ScenarioDigest, assessment)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func validHTTPScenario(caseID string) survivaldomain.Scenario {
	return survivaldomain.Scenario{ID: "replay-" + caseID, CaseID: caseID,
		AsOf: time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC), ElapsedMinutes: 60,
		InputCompleteness: 0.6, Synthetic: true,
		Environment: survivaldomain.EnvironmentSignals{AirPocket: survivaldomain.SignalYes,
			WaterAvailable: survivaldomain.SignalNo, HazardStable: survivaldomain.SignalUnknown},
		Entrapment: survivaldomain.EntrapmentSignals{Communication: survivaldomain.SignalYes,
			Injury: survivaldomain.InjuryUnknown}}
}

func validHTTPEvent(caseID string) survivaldomain.HistoricalEvent {
	return survivaldomain.HistoricalEvent{ID: caseID, DatasetEventID: "catalog:" + caseID,
		EventDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Category: "landslide",
		Country: "United States", LocationAccuracy: "approximate", Location: spatial.Point{Longitude: -120, Latitude: 35},
		Source: provenance.Provenance{Provider: "USGS", Dataset: "history", SourceRevision: "revision-1",
			SourceURI: "https://example.test/" + caseID, DataKind: provenance.DataKindHistorical,
			FetchedAt: time.Date(2026, 8, 27, 7, 8, 5, 0, time.UTC)}}
}

type httpFixedClock struct{}

func (httpFixedClock) Now() time.Time { return time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC) }
