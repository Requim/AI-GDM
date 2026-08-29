package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestAIFixtureSuccessUsesAgentServiceAndAIAPI(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	setAITestScenario(scenarios, "success")
	result, requestID := requestAIResult(t, handler, report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: survivalAssessmentID,
	})
	if requestID == "" || result.Authority.Kind != report.AuthoritySurvivalAssessment ||
		result.Authority.ID != survivalAssessmentID || !result.EvidenceAvailable || !result.NarrativeAvailable {
		t.Fatalf("requestID=%q result=%+v", requestID, result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("真实 Agent 结果校验失败: %v", err)
	}
	assertAIFixtureCall(t, scenarios, "ai_report", 1)
	assertAIFixtureCall(t, scenarios, "ai_search", 1)
	assertAIFixtureCall(t, scenarios, "ai_narrative", 1)
	assertNarrativeInput(t, scenarios)
}

func TestAIFixtureNoSuppliersUsesAgentDegradation(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	setAITestScenario(scenarios, "ai_no_suppliers")
	result, _ := requestAIResult(t, handler, report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: survivalAssessmentID,
	})
	if err := result.Validate(); err != nil {
		t.Fatalf("无供应商降级结果校验失败: %v", err)
	}
	if result.Evidence == nil || result.Narrative.KeyFindings == nil ||
		result.EvidenceAvailable || result.NarrativeAvailable {
		t.Fatalf("无供应商结果没有保持非 null 空数组或错误标记: %+v", result)
	}
	assertAIFixtureCall(t, scenarios, "ai_search", 0)
	assertAIFixtureCall(t, scenarios, "ai_narrative", 0)
}

func TestAIFixtureStructuredRetryRunsInsideTypedGenerator(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	setAITestScenario(scenarios, "ai_structured_retry_success")
	result, _ := requestAIResult(t, handler, report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: survivalAssessmentID,
	})
	if err := result.Validate(); err != nil || !result.NarrativeAvailable {
		t.Fatalf("结构重试结果 err=%v result=%+v", err, result)
	}
	assertAIFixtureCall(t, scenarios, "ai_structured_attempt", 2)
}

func TestAIFixtureNegativeWireMutatesEncodedServiceResult(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	setAITestScenario(scenarios, "ai_bad_sha")
	result, _ := requestAIResult(t, handler, report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: survivalAssessmentID,
	})
	canonical, err := result.Authority.Canonical()
	if err != nil || canonical.ID != survivalAssessmentID {
		t.Fatalf("负向 wire 的 Authority 基线不是固定 schema: err=%v authority=%+v", err, result.Authority)
	}
	if result.AuthoritySHA256 != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("摘要定向篡改未生效: %s", result.AuthoritySHA256)
	}
	assertAIFixtureCall(t, scenarios, "ai_search", 1)
	assertAIFixtureCall(t, scenarios, "ai_narrative", 1)
}

func TestAIFixtureTimeAndURLMutationsPreserveAuthority(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	base := requestAIResultForScenario(t, scenarios, handler, "success")
	tests := []string{
		"ai_bad_time_order", "ai_narrative_before_authority", "ai_evidence_before_authority",
		"ai_evidence_after_narrative", "ai_future_crawled_at", "ai_crawled_after_source",
		"ai_unsafe_url", "ai_private_source", "ai_localhost_source", "ai_ipv6_source",
		"ai_ipv4_mapped_source", "ai_local_source", "ai_internal_source",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := requestAIResultForScenario(t, scenarios, handler, name)
			if mutated.AuthoritySHA256 != base.AuthoritySHA256 ||
				!reflect.DeepEqual(mutated.Authority, base.Authority) {
				t.Fatalf("负向响应改写了 Authority: base=%s mutated=%s", base.AuthoritySHA256,
					mutated.AuthoritySHA256)
			}
		})
	}
}

func TestAIFixtureInjectionMinimizesEvidenceBeforeResponse(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	result := requestAIResultForScenario(t, scenarios, handler, "ai_injection")
	if len(result.Evidence) != 1 || result.Evidence[0].URL != "https://mnr.gov.cn/" ||
		strings.Contains(result.Evidence[0].Title, "<svg") ||
		!strings.Contains(result.Narrative.Summary, "<img") {
		t.Fatalf("注入夹具未保持解释文本或未最小化证据: %+v", result)
	}
}

func TestAIFixtureKeepsDistinctSameDomainEvidenceReferences(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	result := requestAIResultForScenario(t, scenarios, handler, "ai_same_domain_distinct")
	if len(result.Evidence) != 2 || result.Evidence[0].URL != "https://mnr.gov.cn/" ||
		result.Evidence[1].URL != "https://mnr.gov.cn/" ||
		result.Evidence[0].Source.SourceRevision == result.Evidence[1].Source.SourceRevision {
		t.Fatalf("同域不同条目未保留独立不可逆引用: %+v", result.Evidence)
	}
	payload, err := json.Marshal(result.Evidence)
	if err != nil || bytes.Contains(payload, []byte("zhangsan")) ||
		bytes.Contains(payload, []byte("/news/")) || bytes.Contains(payload, []byte("revision=2")) {
		t.Fatalf("响应证据泄露原始子域或路径: payload=%s err=%v", payload, err)
	}
	input := recordedNarrativeInput(t, scenarios)
	if len(input.Evidence) != 2 || input.Evidence[0].Source.SourceRevision != result.Evidence[0].Source.SourceRevision ||
		input.Evidence[1].Source.SourceRevision != result.Evidence[1].Source.SourceRevision {
		t.Fatalf("响应证据与 LLM 审计引用未对齐: result=%+v input=%+v", result.Evidence, input.Evidence)
	}
}

func TestAIFixtureDoesNotExposeFreeTextProvenance(t *testing.T) {
	scenarios, handler := newAITestFixture(t)
	result := requestAIResultForScenario(t, scenarios, handler, "ai_provenance_pii")
	input := recordedNarrativeInput(t, scenarios)
	payload, err := json.Marshal(struct {
		Result applicationagent.Result `json:"result"`
		Input  report.NarrativeInput   `json:"input"`
	}{Result: result, Input: input})
	for _, sensitive := range []string{"张三", "E12345678", "人民南路四段27号"} {
		if bytes.Contains(payload, []byte(sensitive)) {
			t.Fatalf("自由文本 provenance 泄露到 API 或 LLM: %q payload=%s", sensitive, payload)
		}
	}
	if err != nil || len(result.Evidence) != 1 ||
		result.Evidence[0].Source.DatasetVersion != "redacted-v2" ||
		!strings.HasPrefix(result.Evidence[0].Source.ProviderRequestID, "sha256:") {
		t.Fatalf("fixture provenance 最小化失败: evidence=%+v err=%v", result.Evidence, err)
	}
}

func requestAIResultForScenario(t *testing.T, scenarios *scenarioStore, handler http.Handler,
	name string,
) applicationagent.Result {
	t.Helper()
	setAITestScenario(scenarios, name)
	result, _ := requestAIResult(t, handler, report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: survivalAssessmentID,
	})
	return result
}

func newAITestFixture(t *testing.T) (*scenarioStore, http.Handler) {
	t.Helper()
	scenarios, err := newScenarioStore()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lossHandler, lossStore, err := newLossHandler(scenarios, logger)
	if err != nil {
		t.Fatal(err)
	}
	scenarios.useLossHandler(lossHandler, lossStore)
	handler, err := newAIHandler(scenarios, logger)
	if err != nil {
		t.Fatal(err)
	}
	scenarios.useAIHandler(handler)
	return scenarios, http.HandlerFunc(scenarios.serveAIReport)
}

func setAITestScenario(scenarios *scenarioStore, name string) {
	scenarios.mu.Lock()
	scenarios.name, scenarios.calls = name, map[string]int{}
	scenarios.requests = map[string]json.RawMessage{}
	scenarios.mu.Unlock()
}

func requestAIResult(t *testing.T, handler http.Handler,
	reference report.AnalysisReference,
) (applicationagent.Result, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"analysisRef": reference, "evidenceLimit": 5,
	})
	request := httptest.NewRequest(http.MethodPost, "/report", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("AI fixture status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data      applicationagent.Result `json:"data"`
		RequestID string                  `json:"requestId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("解码 AI fixture: %v", err)
	}
	return envelope.Data, envelope.RequestID
}

func assertAIFixtureCall(t *testing.T, scenarios *scenarioStore, operation string, expected int) {
	t.Helper()
	scenarios.mu.Lock()
	actual := scenarios.calls[operation]
	scenarios.mu.Unlock()
	if actual != expected {
		t.Fatalf("%s calls=%d want=%d", operation, actual, expected)
	}
}

func assertNarrativeInput(t *testing.T, scenarios *scenarioStore) {
	t.Helper()
	input := recordedNarrativeInput(t, scenarios)
	if err := input.Validate(); err != nil || len(input.Evidence) != 1 {
		t.Fatalf("NarrativeInput err=%v input=%+v", err, input)
	}
}

func recordedNarrativeInput(t *testing.T, scenarios *scenarioStore) report.NarrativeInput {
	t.Helper()
	scenarios.mu.Lock()
	payload := append(json.RawMessage(nil), scenarios.requests["ai_narrative_input"]...)
	scenarios.mu.Unlock()
	var input report.NarrativeInput
	if err := json.Unmarshal(payload, &input); err != nil {
		t.Fatalf("解码真实 NarrativeInput: %v", err)
	}
	return input
}
