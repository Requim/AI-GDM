package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestNewSurvivalFixtureUsesTypedApplicationOutputs(t *testing.T) {
	fixture, err := newSurvivalFixture()
	if err != nil {
		t.Fatal(err)
	}
	detail := fixture.details[survivalCaseID]
	replay := fixture.replays[survivalCaseID]
	if len(fixture.cases) != 3 || detail.ScenarioDigest != survivalScenarioHash ||
		replay.AssessmentID != survivalAssessmentID || !replay.Assessment.CalculatedAt.Equal(survivalFixtureNow) {
		t.Fatalf("fixture detail=%+v replay=%+v cases=%d", detail, replay, len(fixture.cases))
	}
	if err = detail.Validate(); err != nil {
		t.Fatalf("detail validation=%v", err)
	}
	if err = replay.Validate(); err != nil {
		t.Fatalf("replay validation=%v", err)
	}
	payload, err := json.Marshal(replay)
	if err != nil || !json.Valid(payload) {
		t.Fatalf("typed replay JSON err=%v payload=%s", err, payload)
	}
}

func TestSurvivalFixtureSuccessUsesRealHTTPHandler(t *testing.T) {
	fixture, err := newSurvivalFixture()
	if err != nil {
		t.Fatal(err)
	}
	cases := fixtureRequest[[]applicationsurvival.HistoricalCase](t, fixture.handler, http.MethodGet, "/cases")
	if len(cases) != 3 || cases[0].Event.ID == "" {
		t.Fatalf("cases=%+v", cases)
	}
	for _, value := range cases {
		if value.Event.Source.Provider != "USGS" || value.Event.Source.CRS != "EPSG:4326" ||
			value.Event.Source.DataKind != "historical" {
			t.Fatalf("browser source contract=%+v", value.Event.Source)
		}
	}
	detail := fixtureRequest[applicationsurvival.HistoricalCaseDetail](t, fixture.handler,
		http.MethodGet, "/cases/"+survivalCaseID)
	replay := fixtureRequest[applicationsurvival.ReplayAssessment](t, fixture.handler,
		http.MethodPost, "/replays/cases/"+survivalCaseID+"/assessment")
	model := fixtureRequest[survivaldomain.ModelCard](t, fixture.handler, http.MethodGet, "/model-card")
	if detail.ScenarioDigest != replay.ScenarioDigest || replay.AssessmentID != survivalAssessmentID ||
		model.ModelVersion != replay.Assessment.ModelVersion {
		t.Fatalf("detail=%+v replay=%+v model=%+v", detail, replay, model)
	}
}

func fixtureRequest[T any](t *testing.T, handler http.Handler, method, path string) T {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("%s %s status=%d headers=%v body=%s", method, path,
			response.Code, response.Header(), response.Body.String())
	}
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return envelope.Data
}
