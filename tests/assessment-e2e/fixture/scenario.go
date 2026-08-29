package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultScenario = "success"

var supportedScenarios = map[string]struct{}{
	"success": {}, "loss_big_integer": {}, "loss_national_baseline": {},
	"loss_many_limitations": {}, "loss_projection_limitation": {},
	"loss_projection_limitations_missing": {}, "loss_audit_projection_limitations_mismatch": {},
	"loss_success_then_503": {}, "loss_get_503": {}, "loss_sources_503": {},
	"loss_timeout": {}, "loss_bad_wire": {}, "loss_delayed": {},
	"loss_content_length_oversized": {}, "loss_chunked_oversized": {},
	"loss_get_content_length_oversized": {}, "loss_get_chunked_oversized": {},
	"loss_sources_content_length_oversized": {}, "loss_sources_chunked_oversized": {},
	"loss_included_assets_mismatch": {}, "loss_cost_unit_mismatch": {}, "loss_input_reference_mismatch": {},
	"loss_cost_missing_semantic": {}, "loss_cost_duplicate_semantic": {}, "loss_cost_extra_semantic": {},
	"loss_vulnerability_missing_semantic": {}, "loss_vulnerability_duplicate_semantic": {},
	"loss_vulnerability_extra_semantic": {}, "loss_bad_time_order": {},
	"loss_snapshot_expired_at_assessment": {}, "loss_spatial_after_assessment": {},
	"loss_projection_collected_after_assessment": {}, "loss_projection_expired_at_assessment": {},
	"loss_projection_invalid_window": {}, "loss_admin_boundary_bad_digest": {},
	"loss_audit_admin_boundary_mismatch":   {},
	"loss_source_fetched_after_assessment": {}, "loss_source_valid_from_after_assessment": {},
	"loss_source_observed_after_assessment": {}, "loss_source_published_after_assessment": {},
	"loss_source_revision_seen_after_assessment": {}, "loss_cost_price_after_assessment": {},
	"loss_baseline_fetched_after_assessment":      {},
	"loss_vulnerability_fetched_after_assessment": {}, "loss_private_source": {}, "loss_localhost_source": {},
	"loss_ipv6_source": {}, "loss_ipv4_mapped_source": {}, "loss_local_source": {}, "loss_internal_source": {},
	"loss_location_missing": {}, "loss_location_wrong_id": {}, "loss_location_query": {},
	"loss_location_hash": {}, "loss_location_extra_path": {}, "loss_location_encoded": {},
	"loss_location_cross_origin":       {},
	"survival_detail_success_then_503": {}, "survival_replay_success_then_503": {},
	"survival_replay_timeout": {}, "survival_replay_delayed": {},
	"survival_model_card_503": {}, "survival_model_card_missing_field": {},
	"survival_model_card_wrong_version": {}, "survival_source_invalid": {},
	"survival_source_invalid_window": {}, "survival_scenario_completeness_mismatch": {},
	"survival_cases_content_length_oversized": {}, "survival_cases_chunked_oversized": {},
	"survival_detail_content_length_oversized": {}, "survival_detail_chunked_oversized": {},
	"survival_replay_content_length_oversized": {}, "survival_replay_chunked_oversized": {},
	"survival_scenario_tampered": {}, "survival_bad_assessment_id": {},
	"survival_missing_calculated_at": {}, "survival_invalid_calculated_at": {},
	"survival_calculated_before_scenario": {}, "ai_no_suppliers": {}, "ai_success_then_503": {},
	"ai_timeout": {}, "ai_delayed": {}, "ai_content_length_oversized": {}, "ai_chunked_oversized": {},
	"ai_slow_search_degraded": {}, "ai_slow_llm_degraded": {}, "ai_structured_retry_success": {},
	"ai_bad_time_order": {}, "ai_future_time": {}, "ai_narrative_before_authority": {},
	"ai_evidence_before_authority": {}, "ai_evidence_after_narrative": {},
	"ai_crawled_before_authority": {}, "ai_future_crawled_at": {}, "ai_crawled_after_source": {},
	"ai_bad_sha": {}, "ai_bad_usage": {}, "ai_bad_survival_factors": {},
	"ai_bad_survival_limitations": {}, "ai_injection": {},
	"ai_unsafe_url": {}, "ai_private_source": {}, "ai_localhost_source": {}, "ai_ipv6_source": {},
	"ai_ipv4_mapped_source": {}, "ai_local_source": {}, "ai_internal_source": {}, "ai_contradiction": {},
	"ai_survival_contradiction": {}, "ai_provenance_pii": {}, "ai_unminimized_provenance": {},
	"ai_same_domain_distinct": {},
}

type scenarioState struct {
	Name     string                     `json:"name"`
	Calls    map[string]int             `json:"calls"`
	Requests map[string]json.RawMessage `json:"requests"`
}

type scenarioStore struct {
	mu          sync.Mutex
	name        string
	calls       map[string]int
	requests    map[string]json.RawMessage
	lossHandler http.Handler
	lossStore   *fixtureLossStore
	aiHandler   http.Handler
	survival    survivalFixture
}

func newScenarioStore() (*scenarioStore, error) {
	survival, err := newSurvivalFixture()
	if err != nil {
		return nil, err
	}
	return &scenarioStore{name: defaultScenario, calls: map[string]int{},
		requests: map[string]json.RawMessage{}, survival: survival}, nil
}

func (s *scenarioStore) configure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid scenario", http.StatusBadRequest)
		return
	}
	if _, exists := supportedScenarios[input.Name]; !exists {
		http.Error(w, "unsupported scenario", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.name, s.calls, s.requests = input.Name, map[string]int{}, map[string]json.RawMessage{}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"name": input.Name})
}

func (s *scenarioStore) serveState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	state := scenarioState{Name: s.name, Calls: cloneCalls(s.calls), Requests: cloneRequests(s.requests)}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"data": state})
}

func cloneCalls(values map[string]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneRequests(values map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func (s *scenarioStore) next(operation string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[operation]++
	return s.name, s.calls[operation]
}

func (s *scenarioStore) record(operation string, r *http.Request) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.requests[operation] = append(json.RawMessage(nil), payload...)
	s.mu.Unlock()
	return payload, nil
}

func waitForRequest(r *http.Request, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{
		"code": code, "message": message, "requestId": "fixture-error",
	}})
}

func writeOversizedResponse(w http.ResponseWriter, chunked bool) {
	body := `{"data":{"padding":"` + strings.Repeat("x", 1024*1024+1024) + `"}}`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !chunked {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		return
	}
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	for start := 0; start < len(body); start += 16384 {
		end := start + 16384
		if end > len(body) {
			end = len(body)
		}
		_, _ = io.WriteString(w, body[start:end])
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
