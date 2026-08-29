package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

const fixtureProjectionLimitation = "跳过非闭合设施 way 42，设施数量可能低估"

func TestLossFixtureProjectionLimitationUsesTypedServiceAndHTTP(t *testing.T) {
	scenarios, handler := newLossTestFixture(t, "loss_projection_limitation")
	value, err := (&fixtureLossEstimator{scenarios: scenarios}).Estimate(t.Context(),
		applicationloss.EstimateInput{SnapshotID: lossSnapshotID})
	if err != nil {
		t.Fatalf("真实 Loss Service 计算有限投影: %v", err)
	}
	assertLimitedFixtureAssessment(t, value)

	created, location := createFixtureLossAssessment(t, handler)
	if created.Status != lossdomain.AssessmentAvailable || created.ConfidenceBand != "moderate" ||
		!slices.Contains(created.Limitations, fixtureProjectionLimitation) {
		t.Fatalf("POST typed DTO 未保留投影限制: %+v", created)
	}
	assertFixtureLossRead(t, handler, location, fixtureProjectionLimitation)
}

func newLossTestFixture(t *testing.T, scenario string) (*scenarioStore, http.Handler) {
	t.Helper()
	scenarios, err := newScenarioStore()
	if err != nil {
		t.Fatal(err)
	}
	setAITestScenario(scenarios, scenario)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, store, err := newLossHandler(scenarios, logger)
	if err != nil {
		t.Fatal(err)
	}
	scenarios.useLossHandler(handler, store)
	return scenarios, handler
}

func assertLimitedFixtureAssessment(t *testing.T, value lossdomain.Assessment) {
	t.Helper()
	if err := value.Validate(); err != nil {
		t.Fatalf("有限投影评估校验失败: %v", err)
	}
	if value.Status != lossdomain.AssessmentAvailable || value.Confidence != 0.79 ||
		value.ConfidenceBand != "moderate" ||
		!slices.Contains(value.Limitations, fixtureProjectionLimitation) ||
		!slices.Contains(value.Evidence.SpatialAnalysis.ProjectionLimitations, fixtureProjectionLimitation) {
		t.Fatalf("有限投影评估契约不一致: %+v", value)
	}
}

func createFixtureLossAssessment(t *testing.T, handler http.Handler) (lossWireAssessment, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"snapshotId": lossSnapshotID})
	request := httptest.NewRequest(http.MethodPost, "/assessments", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST loss fixture status=%d body=%s", response.Code, response.Body.String())
	}
	return decodeLossWire(t, response), response.Header().Get("Location")
}

func assertFixtureLossRead(t *testing.T, handler http.Handler, location, limitation string) {
	t.Helper()
	const prefix = "/api/v1/loss"
	if !strings.HasPrefix(location, prefix+"/assessments/") {
		t.Fatalf("loss fixture Location=%q", location)
	}
	for _, suffix := range []string{"", "/sources"} {
		request := httptest.NewRequest(http.MethodGet, location[len(prefix):]+suffix, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", suffix, response.Code, response.Body.String())
		}
		value := decodeLossWire(t, response)
		if !slices.Contains(value.ProjectionLimitations, limitation) {
			t.Fatalf("GET %s 未保留投影限制: %+v", suffix, value)
		}
	}
}

type lossWireAssessment struct {
	Status                lossdomain.AssessmentStatus `json:"status"`
	Confidence            float64                     `json:"confidence"`
	ConfidenceBand        string                      `json:"confidenceBand"`
	Limitations           []string                    `json:"limitations"`
	ProjectionLimitations []string                    `json:"projectionLimitations"`
	Evidence              struct {
		SpatialAnalysis struct {
			ProjectionLimitations []string `json:"projectionLimitations"`
		} `json:"spatialAnalysis"`
	} `json:"evidence"`
}

func decodeLossWire(t *testing.T, response *httptest.ResponseRecorder) lossWireAssessment {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解码 loss envelope: %v", err)
	}
	var value lossWireAssessment
	if err := json.Unmarshal(envelope.Data, &value); err != nil {
		t.Fatalf("解码 loss typed DTO: %v", err)
	}
	if len(value.ProjectionLimitations) == 0 {
		value.ProjectionLimitations = value.Evidence.SpatialAnalysis.ProjectionLimitations
	}
	return value
}
