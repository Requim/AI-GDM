package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

const defaultScenario = "success"

var supportedScenarios = map[string]struct{}{
	"success": {}, "missing_valid_to": {}, "invalid_valid_to": {},
	"short_validity": {}, "success_then_503": {}, "success_then_timeout": {},
	"too_many_zones": {}, "complex_geometry": {},
}

type scenarioStore struct {
	mu    sync.Mutex
	name  string
	calls int
}

func newScenarioStore() *scenarioStore {
	return &scenarioStore{name: defaultScenario}
}

func (s *scenarioStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, call := s.next()
	if name == "success_then_timeout" && call > 1 {
		<-r.Context().Done()
		return
	}
	if name == "success_then_503" && call > 1 {
		writeProviderUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, envelopeFor(name))
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
	s.name, s.calls = input.Name, 0
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"name": input.Name, "calls": 0})
}

func (s *scenarioStore) next() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.name, s.calls
}

func writeProviderUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{
		"code": "provider_unavailable", "message": "实时风险数据暂时不可用", "requestId": "fixture-503",
	}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
