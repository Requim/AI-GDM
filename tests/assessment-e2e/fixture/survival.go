package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/adapters/http/survivalapi"
	"github.com/Requim/AI-GDM/internal/adapters/storage/memory"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

const (
	survivalCaseID       = "case-oso-2014"
	survivalScenarioID   = "replay-oso-2014"
	survivalAssessmentID = "sha256:830da326807c37d810886e4eeeed303aca4c8216ce839dd25f904b872b05550f"
	survivalScenarioHash = "sha256:af6d0d291cb5396bb78f9daf043d7da379824e1604d551c670e7495c5a664046"
	survivalModelVersion = "ai-gdm-survival-rules-v1"
)

var survivalFixtureNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

type survivalFixture struct {
	cases     []applicationsurvival.HistoricalCase
	details   map[string]applicationsurvival.HistoricalCaseDetail
	replays   map[string]applicationsurvival.ReplayAssessment
	modelCard survivaldomain.ModelCard
	handler   http.Handler
}

func newSurvivalFixture() (survivalFixture, error) {
	catalog, err := memory.NewDefaultSurvivalCatalog()
	if err != nil {
		return survivalFixture{}, fmt.Errorf("创建真实生还目录 fixture: %w", err)
	}
	cases, err := applicationsurvival.NewCatalogService(catalog)
	if err != nil {
		return survivalFixture{}, err
	}
	assessments, err := applicationsurvival.NewAssessmentService(catalog, survivalFixtureClock{})
	if err != nil {
		return survivalFixture{}, err
	}
	handler, err := survivalapi.New(cases, assessments,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		return survivalFixture{}, fmt.Errorf("创建真实生还 HTTP fixture: %w", err)
	}
	values, err := cases.ListCases(context.Background())
	if err != nil {
		return survivalFixture{}, fmt.Errorf("读取真实生还目录 fixture: %w", err)
	}
	result := survivalFixture{cases: values, details: make(map[string]applicationsurvival.HistoricalCaseDetail),
		replays: make(map[string]applicationsurvival.ReplayAssessment), modelCard: survivaldomain.DefaultModelCard(),
		handler: middleware.RequestID(handler)}
	for _, value := range values {
		if err = result.addCase(cases, assessments, value.Event.ID); err != nil {
			return survivalFixture{}, err
		}
	}
	if err = result.validateFixedIdentity(); err != nil {
		return survivalFixture{}, err
	}
	return result, result.modelCard.Validate()
}

func (f *survivalFixture) addCase(catalog applicationsurvival.CatalogService,
	assessments applicationsurvival.AssessmentService, caseID string,
) error {
	detail, err := catalog.GetCase(context.Background(), caseID)
	if err != nil {
		return fmt.Errorf("读取案例 %s fixture: %w", caseID, err)
	}
	replay, err := assessments.AssessCase(context.Background(), caseID)
	if err != nil {
		return fmt.Errorf("评估案例 %s fixture: %w", caseID, err)
	}
	f.details[caseID], f.replays[caseID] = detail, replay
	return nil
}

func (f survivalFixture) validateFixedIdentity() error {
	detail, detailOK := f.details[survivalCaseID]
	replay, replayOK := f.replays[survivalCaseID]
	if !detailOK || !replayOK || detail.Scenario.ID != survivalScenarioID ||
		detail.ScenarioDigest != survivalScenarioHash || replay.AssessmentID != survivalAssessmentID ||
		replay.Assessment.ModelVersion != survivalModelVersion {
		return fmt.Errorf("真实生还 fixture 身份与浏览器常量不一致")
	}
	return nil
}

type survivalFixtureClock struct{}

func (survivalFixtureClock) Now() time.Time { return survivalFixtureNow }

func (s *scenarioStore) serveSurvival(w http.ResponseWriter, r *http.Request) {
	operation := survivalOperation(r)
	name, call := s.next(operation)
	if handleSurvivalOverride(w, r, name, operation, call) {
		return
	}
	if survivalMutation(name, operation) {
		s.serveMutatedSurvival(w, r, name, operation)
		return
	}
	s.survival.handler.ServeHTTP(w, r)
}

func survivalOperation(r *http.Request) string {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/cases":
		return "survival_cases"
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/cases/"):
		return "survival_detail"
	case r.Method == http.MethodGet && r.URL.Path == "/model-card":
		return "survival_model_card"
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/replays/cases/"):
		return "survival_replay"
	default:
		return "survival_other"
	}
}

func handleSurvivalOverride(w http.ResponseWriter, r *http.Request, name, operation string, call int) bool {
	if writeSurvivalOversized(w, name, operation) {
		return true
	}
	if name == "survival_detail_success_then_503" && operation == "survival_detail" && call > 1 {
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "历史案例目录暂时不可用")
		return true
	}
	if name == "survival_replay_success_then_503" && operation == "survival_replay" && call > 1 {
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "生还评估依赖暂时不可用")
		return true
	}
	if name == "survival_model_card_503" && operation == "survival_model_card" {
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "生还评估模型卡暂时不可用")
		return true
	}
	if name == "survival_replay_timeout" && operation == "survival_replay" {
		<-r.Context().Done()
		return true
	}
	return name == "survival_replay_delayed" && operation == "survival_replay" &&
		!waitForRequest(r, 800*time.Millisecond)
}

func survivalMutation(name, operation string) bool {
	switch operation {
	case "survival_detail":
		return name == "survival_scenario_tampered" || name == "survival_source_invalid" ||
			name == "survival_source_invalid_window" || name == "survival_scenario_completeness_mismatch"
	case "survival_replay":
		return strings.HasPrefix(name, "survival_bad_") || strings.HasPrefix(name, "survival_missing_") ||
			strings.HasPrefix(name, "survival_invalid_") || name == "survival_calculated_before_scenario"
	case "survival_model_card":
		return name == "survival_model_card_missing_field" || name == "survival_model_card_wrong_version"
	default:
		return false
	}
}

func (s *scenarioStore) serveMutatedSurvival(w http.ResponseWriter, r *http.Request, name, operation string) {
	recorded := httptest.NewRecorder()
	s.survival.handler.ServeHTTP(recorded, r)
	if recorded.Code != http.StatusOK {
		copyRecordedResponse(w, recorded)
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorded.Body.Bytes(), &envelope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "fixture_invalid", err.Error())
		return
	}
	if err := mutateSurvivalData(envelope, name, operation); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "fixture_invalid", err.Error())
		return
	}
	if requestID := recorded.Header().Get("X-Request-ID"); requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	writeJSON(w, http.StatusOK, envelope)
}

func writeSurvivalOversized(w http.ResponseWriter, name, operation string) bool {
	if name == operation+"_content_length_oversized" {
		writeOversizedResponse(w, false)
		return true
	}
	if name == operation+"_chunked_oversized" {
		writeOversizedResponse(w, true)
		return true
	}
	return false
}

func mutateSurvivalData(envelope map[string]any, name, operation string) error {
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return fmt.Errorf("真实生还 API fixture 缺少 data 对象")
	}
	switch operation {
	case "survival_detail":
		return mutateSurvivalDetail(data, name)
	case "survival_replay":
		return mutateSurvivalReplay(data, name)
	case "survival_model_card":
		return mutateSurvivalModelCard(data, name)
	default:
		return fmt.Errorf("不支持篡改生还操作 %s", operation)
	}
}

func mutateSurvivalDetail(data map[string]any, name string) error {
	scenario, scenarioOK := data["scenario"].(map[string]any)
	event, eventOK := data["event"].(map[string]any)
	if !scenarioOK || !eventOK {
		return fmt.Errorf("案例详情 fixture 结构无效")
	}
	switch name {
	case "survival_scenario_tampered":
		environment, ok := scenario["environment"].(map[string]any)
		if !ok {
			return fmt.Errorf("场景环境 fixture 结构无效")
		}
		environment["airPocket"] = "no"
	case "survival_scenario_completeness_mismatch":
		scenario["inputCompleteness"] = 1.0
	case "survival_source_invalid", "survival_source_invalid_window":
		return mutateHistoricalSource(event, name)
	}
	return nil
}

func mutateHistoricalSource(event map[string]any, name string) error {
	source, ok := event["source"].(map[string]any)
	if !ok {
		return fmt.Errorf("历史来源 fixture 结构无效")
	}
	if name == "survival_source_invalid" {
		source["dataKind"] = "forecast"
		return nil
	}
	source["validFrom"] = "2026-08-27T12:00:00Z"
	source["validTo"] = "2026-08-27T11:00:00Z"
	return nil
}

func mutateSurvivalReplay(data map[string]any, name string) error {
	assessment, ok := data["assessment"].(map[string]any)
	if !ok {
		return fmt.Errorf("评估 fixture 结构无效")
	}
	switch name {
	case "survival_bad_assessment_id":
		data["assessmentId"] = "sha256:" + strings.Repeat("0", 64)
	case "survival_missing_calculated_at":
		delete(assessment, "calculatedAt")
	case "survival_invalid_calculated_at":
		assessment["calculatedAt"] = "2026-02-30T12:00:00Z"
	case "survival_calculated_before_scenario":
		assessment["calculatedAt"] = "2014-03-21T23:59:59Z"
	}
	return nil
}

func mutateSurvivalModelCard(data map[string]any, name string) error {
	switch name {
	case "survival_model_card_missing_field":
		delete(data, "purpose")
	case "survival_model_card_wrong_version":
		data["modelVersion"] = "ai-gdm-survival-rules-v999"
	}
	return nil
}

func publicSource(provider, dataset, dataKind string) map[string]any {
	return map[string]any{
		"provider": provider, "dataset": dataset, "datasetVersion": "2026.08", "sourceRevision": "fixture-v1",
		"sourceUri": "https://data.mnr.gov.cn/assessment-e2e", "citation": dataset + " 公开来源",
		"license": "公开数据许可", "dataKind": dataKind, "observedAt": "2026-08-27T11:00:00Z",
		"publishedAt": "2026-08-27T11:05:00Z", "revisionFirstSeenAt": "2026-08-27T11:10:00Z",
		"fetchedAt": "2026-08-27T11:15:00Z", "validFrom": "2026-08-27T11:00:00Z",
		"validTo": "2026-08-27T13:00:00Z", "spatialResolution": "1 km", "temporalResolution": "hour",
		"crs": "EPSG:4326", "bbox": []float64{103.0, 29.0, 105.0, 31.0}, "sha256": strings.Repeat("c", 64),
		"transformVersion": "fixture-v1", "providerRequestId": "fixture-request", "model": "fixture",
		"stale": false, "qualityFlags": []string{}, "limitations": []string{}, "sourceParts": []any{
			map[string]any{"reference": "https://data.mnr.gov.cn/assessment-e2e/part-1", "revision": "v1",
				"sizeBytes": 1024, "bbox": []float64{103.0, 29.0, 105.0, 31.0}, "sha256": strings.Repeat("d", 64)},
		},
	}
}
