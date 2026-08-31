package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestScenarioConfigureAcceptsEveryRegisteredScenario(t *testing.T) {
	store, err := newScenarioStore()
	if err != nil {
		t.Fatal(err)
	}
	for name := range supportedScenarios {
		request := httptest.NewRequest(http.MethodPost, "/__fixture/scenario",
			strings.NewReader(`{"name":"`+name+`"}`))
		response := httptest.NewRecorder()
		store.configure(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("场景 %q configure 状态=%d body=%s", name, response.Code, response.Body.String())
		}
	}
}

func TestAssessmentSpecScenarioLiteralsAreRegistered(t *testing.T) {
	payload, err := os.ReadFile("../specs/assessment.spec.js")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`["'](success|(?:loss|survival|ai)_[a-z0-9_]+)["']`)
	missing := unregisteredScenarioLiterals(pattern.FindAllStringSubmatch(string(payload), -1))
	if len(missing) > 0 {
		t.Fatalf("Playwright 引用未登记场景: %v", missing)
	}
}

func unregisteredScenarioLiterals(matches [][]string) []string {
	nonScenarios := map[string]struct{}{
		"ai_report": {}, "ai_structured_attempt": {}, "loss_get": {}, "loss_post": {}, "loss_sources": {},
		"survival_assessment": {},
	}
	missing := map[string]struct{}{}
	for _, match := range matches {
		name := match[1]
		if _, registered := supportedScenarios[name]; registered {
			continue
		}
		if _, excluded := nonScenarios[name]; !excluded {
			missing[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(missing))
	for name := range missing {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
