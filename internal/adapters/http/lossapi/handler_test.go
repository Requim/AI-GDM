package lossapi

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
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/go-chi/chi/v5/middleware"
)

func TestLossAPIProvidesCreateQueryAndSourceAudit(t *testing.T) {
	value := validAssessment()
	value.InputReferences = []string{"https://example.test/source?" + "api_key" + "=removed&revision=7"}
	service := &serviceStub{value: value}
	store := &assessmentStoreStub{value: value}
	handler, err := New(service, store, store, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	body := "{\"snapshotId\":\"snapshot-1\",\"regionCode\":\"CN\",\"hazardType\":\"landslide\",\"intensityBand\":\"high\",\"exposures\":[]}"
	create := request(t, handler, http.MethodPost, "/assessments", body)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	if store.saved.ID != value.ID {
		t.Fatalf("saved=%+v", store.saved)
	}
	get := request(t, handler, http.MethodGet, "/assessments/"+value.ID, "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	audit := request(t, handler, http.MethodGet, "/assessments/"+value.ID+"/sources", "")
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "formulaVersion") {
		t.Fatalf("audit status=%d body=%s", audit.Code, audit.Body.String())
	}
	if strings.Contains(audit.Body.String(), "removed") || !strings.Contains(audit.Body.String(), "revision=7") {
		t.Fatalf("来源审计未正确脱敏: %s", audit.Body.String())
	}
}

func TestLossAPIMapsValidationAndNotFound(t *testing.T) {
	store := &assessmentStoreStub{err: domain.ErrNotFound}
	handler, err := New(&serviceStub{value: validAssessment()}, store, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	invalid := request(t, handler, http.MethodGet, "/assessments/bad%20id", "")
	assertStatusCode(t, invalid, http.StatusBadRequest, "invalid_request")
	missing := request(t, handler, http.MethodGet, "/assessments/loss-missing", "")
	assertStatusCode(t, missing, http.StatusNotFound, "assessment_not_found")
	badJSON := request(t, handler, http.MethodPost, "/assessments", "{}")
	assertStatusCode(t, badJSON, http.StatusBadRequest, "invalid_request")
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &assessmentStoreStub{}
	service := &serviceStub{value: validAssessment()}
	if _, err := New(nil, store, store, logger); err == nil {
		t.Fatal("空服务未被拒绝")
	}
	if _, err := New(service, nil, store, logger); err == nil {
		t.Fatal("空读端口未被拒绝")
	}
	if _, err := New(service, store, nil, logger); err == nil {
		t.Fatal("空写端口未被拒绝")
	}
}

func TestLossAPIRejectsUnknownFieldsAndSaveFailures(t *testing.T) {
	value := validAssessment()
	store := &assessmentStoreStub{value: value, err: errors.New("写入失败")}
	handler, err := New(&serviceStub{value: value}, store, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	unknown := request(t, handler, http.MethodPost, "/assessments", "{\"hazardType\":\"landslide\",\"unknown\":true}")
	assertStatusCode(t, unknown, http.StatusBadRequest, "invalid_request")
	saveFailure := request(t, handler, http.MethodPost, "/assessments", "{\"snapshotId\":\"snapshot-1\",\"regionCode\":\"CN\",\"hazardType\":\"landslide\",\"intensityBand\":\"high\",\"exposures\":[]}")
	assertStatusCode(t, saveFailure, http.StatusInternalServerError, "internal_error")
}

type serviceStub struct {
	value loss.Assessment
	err   error
}

func (s *serviceStub) Estimate(context.Context, applicationloss.EstimateInput) (loss.Assessment, error) {
	return s.value, s.err
}

type assessmentStoreStub struct {
	value loss.Assessment
	saved loss.Assessment
	err   error
}

func (s *assessmentStoreStub) SaveAssessment(_ context.Context, value loss.Assessment) error {
	s.saved = value
	return s.err
}
func (s *assessmentStoreStub) GetAssessment(_ context.Context, _ string) (loss.Assessment, error) {
	if s.err != nil {
		return loss.Assessment{}, s.err
	}
	return s.value, nil
}

func validAssessment() loss.Assessment {
	return loss.Assessment{ID: "loss-123", SnapshotID: "snapshot-1", FormulaVersion: loss.FormulaVersion, ScenarioMethod: "确定性公式", HazardType: "landslide", RegionCode: "CN", ConditionalMidCents: 10, ConditionalHighCents: 20, InputReferences: []string{"source://one"}, Status: loss.AssessmentAvailable, Confidence: 0.8, ConfidenceBand: "high", CalculatedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)}
}

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	middleware.RequestID(handler).ServeHTTP(recorder, req)
	return recorder
}

func assertStatusCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
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
