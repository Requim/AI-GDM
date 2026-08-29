package main

import (
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
)

func TestAIProviderBudgetsFitReportDeadline(t *testing.T) {
	structuredRetries := time.Duration(aiMaxOutputAttempts-1) * aiRequestRate
	worstCase := aiSearchHTTPTimeout + time.Duration(aiMaxOutputAttempts)*aiNarrativeHTTPTimeout + structuredRetries
	if worstCase >= aiapi.ReportTimeout {
		t.Fatalf("供应商最坏预算 %s 未小于服务端预算 %s", worstCase, aiapi.ReportTimeout)
	}
	if aiSearchHTTPTimeout+aiRequestRate >= applicationagent.SearchStageTimeout ||
		time.Duration(aiMaxOutputAttempts)*aiNarrativeHTTPTimeout+structuredRetries >=
			applicationagent.NarrativeStageTimeout ||
		applicationagent.SearchStageTimeout+applicationagent.NarrativeStageTimeout >= aiapi.ReportTimeout {
		t.Fatalf("阶段预算未形成严格包含关系")
	}
}
