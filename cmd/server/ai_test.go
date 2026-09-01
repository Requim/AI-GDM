package main

import (
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
)

func TestAIProviderBudgetsFitReportDeadline(t *testing.T) {
	for attempts := 1; attempts <= aiMaxOutputAttempts; attempts++ {
		retryQueue := time.Duration(attempts-1) * aiRequestRate
		narrativeBudget := time.Duration(attempts)*narrativeHTTPTimeout(attempts) + retryQueue
		if narrativeBudget >= applicationagent.NarrativeStageTimeout {
			t.Fatalf("%d 次尝试预算 %s 未小于解释阶段预算 %s",
				attempts, narrativeBudget, applicationagent.NarrativeStageTimeout)
		}
		if aiSearchHTTPTimeout+narrativeBudget >= aiapi.ReportTimeout {
			t.Fatalf("%d 次尝试供应商预算未小于接口预算", attempts)
		}
	}
	if aiSearchHTTPTimeout+aiRequestRate >= applicationagent.SearchStageTimeout ||
		applicationagent.SearchStageTimeout+applicationagent.NarrativeStageTimeout >= aiapi.ReportTimeout {
		t.Fatalf("阶段预算未形成严格包含关系")
	}
}

func TestNarrativeHTTPTimeoutSharesStageBudget(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 1, want: 31 * time.Second},
		{attempts: 2, want: 15*time.Second + 250*time.Millisecond},
		{attempts: 3, want: 10 * time.Second},
		{attempts: 0, want: aiNarrativeFallbackHTTPTimeout},
		{attempts: 4, want: aiNarrativeFallbackHTTPTimeout},
	}
	for _, test := range tests {
		if got := narrativeHTTPTimeout(test.attempts); got != test.want {
			t.Errorf("%d 次尝试的单次预算为 %s，期望 %s", test.attempts, got, test.want)
		}
	}
}
