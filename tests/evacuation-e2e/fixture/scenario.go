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
	"success": {}, "html_injection": {}, "facility_delayed": {}, "route_delayed": {},
	"facility_success_then_503": {}, "route_success_then_timeout": {},
	"missing_snapshot_valid_to": {}, "missing_source_valid_to": {},
	"array_snapshot_valid_to": {}, "array_source_valid_to": {},
	"non_strict_valid_to": {}, "invalid_calendar_valid_to": {}, "mismatched_valid_to": {},
	"missing_source_stale": {}, "invalid_source_stale": {},
	"facility_missing_omitted_count": {}, "route_missing_omitted_count": {},
	"short_validity": {}, "zero_facilities": {}, "facility_zero_distance": {},
	"route_empty_optional_lists": {}, "transit_citycodes": {}, "risk_zone_all_excluded": {},
	"risk_zone_all_excluded_fallback_then_expiry": {},
	"risk_score_unavailable":                      {},
	"risk_score_zero":                             {}, "risk_score_mixed": {}, "route_rank_contract": {},
	"route_intersects_flag": {}, "facility_page_limit": {}, "facility_excess_count": {},
	"route_excess_count":    {},
	"route_excess_geometry": {}, "facility_all_omitted": {}, "route_all_omitted": {},
	"all_omitted_short_validity": {}, "fallback_unexpired": {}, "fallback_then_expiry": {},
	"oversized_response": {},
}

type scenarioStore struct {
	mu         sync.Mutex
	name       string
	calls      map[string]int
	mapHandler http.Handler
}

func newScenarioStore() *scenarioStore {
	return &scenarioStore{name: defaultScenario, calls: map[string]int{}}
}

func (s *scenarioStore) serveFacilities(w http.ResponseWriter, r *http.Request) {
	name, call := s.next("facilities")
	if name == "facility_success_then_503" && call > 1 {
		writeProviderUnavailable(w)
		return
	}
	if name == "facility_delayed" && !waitForDelay(r, 800*time.Millisecond) {
		return
	}
	if name == "oversized_response" {
		writeOversizedResponse(w)
		return
	}
	if rawSnapshotScenario(name) || name == "missing_source_stale" || name == "invalid_source_stale" {
		writeJSON(w, http.StatusOK, facilityEnvelopeFor(name))
		return
	}
	if name == "facility_missing_omitted_count" {
		writeJSON(w, http.StatusOK, facilityEnvelopeMissingOmittedCount())
		return
	}
	s.serveRealMap(w, r)
}

func (s *scenarioStore) serveRoutes(w http.ResponseWriter, r *http.Request) {
	name, call := s.next("routes")
	if name == "route_success_then_timeout" && call > 1 {
		<-r.Context().Done()
		return
	}
	if name == "route_delayed" && !waitForDelay(r, 800*time.Millisecond) {
		return
	}
	if rawSnapshotScenario(name) || name == "missing_source_stale" || name == "invalid_source_stale" {
		writeJSON(w, http.StatusOK, routeEnvelopeFor(name))
		return
	}
	if name == "route_missing_omitted_count" {
		writeJSON(w, http.StatusOK, routeEnvelopeMissingOmittedCount())
		return
	}
	s.serveRealMap(w, r)
}

func (s *scenarioStore) useMapHandler(handler http.Handler) {
	s.mu.Lock()
	s.mapHandler = handler
	s.mu.Unlock()
}

func (s *scenarioStore) serveRealMap(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	handler := s.mapHandler
	s.mu.Unlock()
	if handler == nil {
		http.Error(w, "map handler unavailable", http.StatusInternalServerError)
		return
	}
	handler.ServeHTTP(w, r)
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
	s.name, s.calls = input.Name, map[string]int{}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"name": input.Name})
}

func (s *scenarioStore) next(operation string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[operation]++
	return s.name, s.calls[operation]
}

func (s *scenarioStore) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func rawSnapshotScenario(name string) bool {
	switch name {
	case "missing_snapshot_valid_to", "missing_source_valid_to",
		"array_snapshot_valid_to", "array_source_valid_to", "non_strict_valid_to",
		"invalid_calendar_valid_to", "mismatched_valid_to":
		return true
	default:
		return false
	}
}

func waitForDelay(r *http.Request, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func writeProviderUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{
		"code": "provider_unavailable", "message": "地图供应商暂时不可用", "requestId": "fixture-503",
	}})
}

func writeOversizedResponse(w http.ResponseWriter) {
	body := `{"data":{"padding":"` + strings.Repeat("x", 2*1024*1024+1024) + `"}}`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
