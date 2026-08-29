package aiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
	"github.com/go-chi/chi/v5/middleware"
)

func TestAIReportAcceptsAuthorityReference(t *testing.T) {
	reporter := &reporterStub{result: validResult(t)}
	handler := newTestHandler(t, reporter)
	body := `{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`
	response := request(t, handler, http.MethodPost, "/report", body)
	if response.Code != http.StatusOK || reporter.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, reporter.calls, response.Body.String())
	}
	if reporter.input.AnalysisRef.Kind != report.AuthorityLossAssessment || reporter.input.AnalysisRef.ID != "loss-1" {
		t.Fatalf("analysisRef = %+v", reporter.input.AnalysisRef)
	}
	if !strings.Contains(response.Body.String(), `"authoritySha256"`) ||
		!strings.Contains(response.Body.String(), `"authority"`) || strings.Contains(response.Body.String(), "Authorization") {
		t.Fatalf("响应未满足权威引用边界: %s", response.Body.String())
	}
}

func TestAIReportRejectsLegacyAnalysisTampering(t *testing.T) {
	reporter := &reporterStub{result: validResult(t)}
	handler := newTestHandler(t, reporter)
	for _, body := range []string{
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"},"analysis":{"conditionalLowCents":1}}`,
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"},"immutableFields":["conditionalLowCents"]}`,
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"},"query":"张三 13800138000"}`,
	} {
		response := request(t, handler, http.MethodPost, "/report", body)
		assertCode(t, response, http.StatusBadRequest, "invalid_request")
	}
	if reporter.calls != 0 {
		t.Fatalf("旧字段篡改仍调用 reporter: %d", reporter.calls)
	}
}

func TestAIReportRejectsUnknownTrailingAndOversizedJSON(t *testing.T) {
	handler := newTestHandler(t, &reporterStub{result: validResult(t)})
	unknown := request(t, handler, http.MethodPost, "/report", `{"analysisRef":{"kind":"loss_assessment","id":"loss-1"},"unknown":true}`)
	assertCode(t, unknown, http.StatusBadRequest, "invalid_request")
	trailing := request(t, handler, http.MethodPost, "/report", `{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}{}`)
	assertCode(t, trailing, http.StatusBadRequest, "invalid_request")
	oversized := request(t, handler, http.MethodPost, "/report", strings.Repeat("x", maxAIReportRequestBytes+1))
	assertCode(t, oversized, http.StatusBadRequest, "invalid_request")
}

func TestAIReportMapsContextProviderAndAuthorityErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
		api  string
	}{
		{name: "timeout", err: context.DeadlineExceeded, code: http.StatusGatewayTimeout, api: "request_timeout"},
		{name: "provider", err: domain.ErrProviderUnavailable, code: http.StatusServiceUnavailable, api: "provider_unavailable"},
		{name: "not found", err: domain.ErrNotFound, code: http.StatusNotFound, api: "authority_not_found"},
		{name: "invalid authority", err: report.ErrInvalidAuthority, code: http.StatusInternalServerError, api: "invalid_authority"},
		{name: "unsafe authority", err: report.ErrUnsafeStoredAnalysis, code: http.StatusServiceUnavailable, api: "unsafe_authority"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t, &reporterStub{err: test.err})
			body := `{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`
			response := request(t, handler, http.MethodPost, "/report", body)
			assertCode(t, response, test.code, test.api)
		})
	}
}

func TestAIReportRejectsOversizedResultWithoutDroppingNarrativeEvidence(t *testing.T) {
	result := validResult(t)
	result.Evidence = make([]report.Evidence, 20)
	for index := range result.Evidence {
		result.Evidence[index] = largeEvidence(index, result.GeneratedAt)
	}
	result.EvidenceAvailable = true
	handler := newTestHandler(t, &reporterStub{result: result})
	response := request(t, handler, http.MethodPost, "/report", `{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`)
	if response.Code != http.StatusInternalServerError || response.Body.Len() > maxAIReportResponseBytes {
		t.Fatalf("status=%d bytes=%d body=%s", response.Code, response.Body.Len(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"data"`) || !strings.Contains(response.Body.String(), `"internal_error"`) {
		t.Fatalf("超量响应被静默裁剪: %s", response.Body.String())
	}
}

func TestAIReportDowngradesOversizedProductionNarrativeAndKeepsAuthority(t *testing.T) {
	evidence := make([]report.Evidence, 20)
	for index := range evidence {
		evidence[index] = largeEvidence(index, fixedClock{}.Now())
	}
	search := &evidenceSearcherStub{values: evidence}
	generator := &narrativeGeneratorStub{value: maximumUTF8Narrative()}
	service, err := applicationagent.New(maximumSurvivalAuthorityResolverStub{}, search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, newTestHandler(t, service), http.MethodPost, "/report",
		fmt.Sprintf(`{"analysisRef":{"kind":"survival_assessment","id":%q},"evidenceLimit":20}`,
			maximumSurvivalAssessmentID()))
	if response.Code != http.StatusOK || response.Body.Len() > maxAIReportResponseBytes {
		t.Fatalf("status=%d bytes=%d body=%s", response.Code, response.Body.Len(), response.Body.String())
	}
	var envelope struct {
		Data applicationagent.Result `json:"data"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope.Data
	if result.Authority.ID != maximumSurvivalAssessmentID() || result.NarrativeAvailable || result.Narrative.Available {
		t.Fatalf("超量说明未按 Authority 保留语义降级: %+v", result)
	}
	if result.Evidence == nil || result.Limitations == nil || result.Narrative.KeyFindings == nil {
		t.Fatalf("降级响应出现 null 数组: %+v", result)
	}
	if !containsLimitation(result.Limitations, "解释性说明已降级") {
		t.Fatalf("降级响应缺少预算限制说明: %v", result.Limitations)
	}
	if !sameEvidenceURLs(result.Evidence, generator.input.Evidence) {
		t.Fatal("大模型输入证据与最终保留证据集合不一致")
	}
}

func TestAIReportKeepsAuthorityWhenFutureEvidenceIsDropped(t *testing.T) {
	now := fixedClock{}.Now()
	evidence := report.Evidence{Title: "未来证据", URL: "https://example.test/future", Summary: "异常供应商时间",
		CrawledAt: now.Add(time.Hour), Source: provenance.Provenance{Provider: "bocha", Dataset: "search",
			SourceURI: "https://example.test/search", DataKind: provenance.DataKindObservation,
			FetchedAt: now.Add(time.Hour), QualityFlags: []string{report.TrustedDomainQualityFlagPrefix + "example.test"}}}
	search, generator := &evidenceSearcherStub{values: []report.Evidence{evidence}}, &narrativeGeneratorStub{value: validHandlerNarrative()}
	service, err := applicationagent.New(authorityResolverStub{}, search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t, service)
	response := request(t, handler, http.MethodPost, "/report",
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data applicationagent.Result `json:"data"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Authority.ID != "loss-1" || payload.Data.Evidence == nil || payload.Data.Narrative.KeyFindings == nil ||
		len(payload.Data.Evidence) != 0 || len(generator.input.Evidence) != 0 {
		t.Fatalf("未来证据降级破坏响应: data=%+v input=%+v", payload.Data, generator.input)
	}
}

func TestAIReportAcceptsHistoricalCrawledAtBeforeAuthority(t *testing.T) {
	now := fixedClock{}.Now()
	evidence := report.Evidence{Title: "历史抓取证据", URL: "https://example.test/history", Summary: "历史网页抓取时间",
		CrawledAt: now.Add(-24 * time.Hour), Source: provenance.Provenance{Provider: "bocha", Dataset: "search",
			SourceRevision: "sha256:" + strings.Repeat("a", 64), SourceURI: "https://example.test/search",
			DataKind: provenance.DataKindObservation, FetchedAt: now, SHA256: strings.Repeat("b", 64),
			ProviderRequestID: "search-request-1",
			QualityFlags:      []string{report.TrustedDomainQualityFlagPrefix + "example.test"}}}
	search, generator := &evidenceSearcherStub{values: []report.Evidence{evidence}}, &narrativeGeneratorStub{value: validHandlerNarrative()}
	service, err := applicationagent.New(authorityResolverStub{}, search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, newTestHandler(t, service), http.MethodPost, "/report",
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data applicationagent.Result `json:"data"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Evidence) != 1 || len(generator.input.Evidence) != 1 || payload.Data.Authority.ID != "loss-1" {
		t.Fatalf("历史证据未通过服务链: data=%+v input=%+v", payload.Data, generator.input)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(nil, logger); err == nil {
		t.Fatal("空编排服务未被拒绝")
	}
	if _, err := New(&reporterStub{}, nil); err == nil {
		t.Fatal("空日志器未被拒绝")
	}
	if _, err := newHandler(&reporterStub{}, slog.Default(), 0); err == nil {
		t.Fatal("非正请求预算未被拒绝")
	}
}

func TestAIReportAppliesServerRequestDeadline(t *testing.T) {
	reporter := &deadlineReporter{}
	handler, err := newHandler(reporter, slog.New(slog.NewTextHandler(io.Discard, nil)), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/report",
		`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`)
	assertCode(t, response, http.StatusGatewayTimeout, "request_timeout")
	if !reporter.hasDeadline || reporter.calls != 1 {
		t.Fatalf("hasDeadline=%t calls=%d", reporter.hasDeadline, reporter.calls)
	}
}

func TestAIReportKeepsAuthorityWhenSlowProvidersFailWithinDeadline(t *testing.T) {
	for _, test := range []struct {
		name      string
		search    ports.EvidenceSearcher
		generator ports.NarrativeGenerator
	}{
		{name: "slow search", search: &delayedEvidenceSearcher{delay: 15 * time.Millisecond}},
		{name: "slow llm", generator: &delayedNarrativeGenerator{delay: 15 * time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := applicationagent.New(authorityResolverStub{}, test.search, test.generator, fixedClock{})
			if err != nil {
				t.Fatal(err)
			}
			handler, err := newHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)), 200*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			response := request(t, handler, http.MethodPost, "/report",
				`{"analysisRef":{"kind":"loss_assessment","id":"loss-1"}}`)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"loss-1"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func newTestHandler(t *testing.T, reporter Reporter) http.Handler {
	t.Helper()
	handler, err := New(reporter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type reporterStub struct {
	result applicationagent.Result
	err    error
	input  applicationagent.Input
	calls  int
}

type deadlineReporter struct {
	hasDeadline bool
	calls       int
}

type delayedEvidenceSearcher struct{ delay time.Duration }

func (s *delayedEvidenceSearcher) Search(ctx context.Context, _ string, _ int) ([]report.Evidence, error) {
	if err := waitForProvider(ctx, s.delay); err != nil {
		return nil, err
	}
	return nil, domain.ErrProviderUnavailable
}

type delayedNarrativeGenerator struct{ delay time.Duration }

func (g *delayedNarrativeGenerator) Generate(ctx context.Context,
	_ report.NarrativeInput,
) (report.Narrative, error) {
	if err := waitForProvider(ctx, g.delay); err != nil {
		return report.Narrative{}, err
	}
	return report.Narrative{}, domain.ErrProviderUnavailable
}

func waitForProvider(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *deadlineReporter) Generate(ctx context.Context,
	_ applicationagent.Input,
) (applicationagent.Result, error) {
	r.calls++
	_, r.hasDeadline = ctx.Deadline()
	<-ctx.Done()
	return applicationagent.Result{}, ctx.Err()
}

type evidenceSearcherStub struct{ values []report.Evidence }

func (s *evidenceSearcherStub) Search(context.Context, string, int) ([]report.Evidence, error) {
	return s.values, nil
}

type narrativeGeneratorStub struct {
	value report.Narrative
	input report.NarrativeInput
}

func (s *narrativeGeneratorStub) Generate(_ context.Context, input report.NarrativeInput) (report.Narrative, error) {
	s.input = input
	return s.value, nil
}

func validHandlerNarrative() report.Narrative {
	return report.Narrative{Summary: "说明", KeyFindings: []string{}, Actions: []string{}, Caveats: []string{},
		GeneratedAt: fixedClock{}.Now(), Model: "test-model", Available: true,
		Source: provenance.Provenance{Provider: "llm", Dataset: "report", SourceURI: "https://example.test/llm",
			DataKind: provenance.DataKindObservation, FetchedAt: fixedClock{}.Now()}}
}

func maximumUTF8Narrative() report.Narrative {
	value := validHandlerNarrative()
	text := strings.Repeat("\U0001F9ED", 4096)
	value.Summary = text
	value.KeyFindings = repeatedNarrativeItems(text)
	value.Actions = repeatedNarrativeItems(text)
	value.Caveats = repeatedNarrativeItems(text)
	return value
}

func repeatedNarrativeItems(value string) []string {
	items := make([]string, 16)
	for index := range items {
		items[index] = value
	}
	return items
}

func containsLimitation(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

func (s *reporterStub) Generate(_ context.Context, input applicationagent.Input) (applicationagent.Result, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func validResult(t *testing.T) applicationagent.Result {
	t.Helper()
	service, err := applicationagent.New(authorityResolverStub{}, nil, nil, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), applicationagent.Input{
		AnalysisRef: report.AnalysisReference{Kind: report.AuthorityLossAssessment, ID: "loss-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func largeEvidence(index int, now time.Time) report.Evidence {
	quality := make([]string, 20)
	limitations := make([]string, 20)
	host := fmt.Sprintf("source-%d.example.test", index)
	quality[0] = report.TrustedDomainQualityFlagPrefix + host
	for item := 1; item < len(quality); item++ {
		quality[item] = strings.Repeat("质", 400)
	}
	for item := range limitations {
		limitations[item] = strings.Repeat("限", 500)
	}
	return report.Evidence{
		Title: strings.Repeat("标", 512), URL: "https://" + host + "/news",
		Summary: strings.Repeat("摘", 4096), SiteName: strings.Repeat("站", 256),
		Source: provenance.Provenance{
			Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/search",
			DataKind: provenance.DataKindObservation, FetchedAt: now,
			QualityFlags: quality, Limitations: limitations,
		},
	}
}

type authorityResolverStub struct{}

func (authorityResolverStub) Resolve(context.Context, report.AnalysisReference) (report.Authority, error) {
	analysis, _ := json.Marshal(report.LossAuthorityAnalysis{
		AffectedPopulation: 10, AssessmentID: "loss-1", ConditionalCentralCents: "2000",
		ConditionalHighCents: "3000", ConditionalLowCents: "1000", Confidence: 0.8,
		ConfidenceBand: "high", FormulaVersion: "loss-v1", ImpactAreaSquareMeters: 100,
		SnapshotID: "snapshot-1", Status: "available",
	})
	return report.Authority{
		Kind: report.AuthorityLossAssessment, ID: "loss-1", Version: "loss-v1", SchemaVersion: report.AuthoritySchemaLossV1,
		AnalysisJSON: analysis,
		ResolvedAt:   fixedClock{}.Now(),
	}, nil
}

type maximumSurvivalAuthorityResolverStub struct{}

func (maximumSurvivalAuthorityResolverStub) Resolve(context.Context,
	report.AnalysisReference,
) (report.Authority, error) {
	analysis := report.SurvivalAuthorityAnalysis{
		AssessmentID: maximumSurvivalAssessmentID(), CaseID: "case-maximum-budget",
		Factors: maximumAuthorityItems("factor"), HumanReviewStatus: "required",
		Limitations: maximumAuthorityItems("limitation"), ModelVersion: survivaldomain.ModelVersion,
		Priority: string(survivaldomain.PriorityUrgent), ProbabilityBand: string(survivaldomain.ProbabilityModerate),
		ProbabilityHigh: 0.59, ProbabilityLow: 0.35, ScenarioDigest: "sha256:" + strings.Repeat("b", 64),
		ScenarioID: "scenario-maximum-budget", Score: 60, ScoreBand: "moderate",
		Usage: survivaldomain.HistoricalReplayUsage(),
	}
	payload, err := json.Marshal(analysis)
	if err != nil {
		return report.Authority{}, err
	}
	return report.Authority{
		Kind: report.AuthoritySurvivalAssessment, ID: analysis.AssessmentID,
		Version: survivaldomain.ModelVersion, SchemaVersion: report.AuthoritySchemaSurvivalV1,
		AnalysisJSON: payload, ResolvedAt: fixedClock{}.Now(),
	}, nil
}

func maximumSurvivalAssessmentID() string { return "sha256:" + strings.Repeat("a", 64) }

func maximumAuthorityItems(prefix string) []string {
	items := make([]string, 32)
	for index := range items {
		items[index] = fmt.Sprintf("%s-%02d-%s", prefix, index, strings.Repeat("\U0001F9ED", 1000))
	}
	return items
}

func sameEvidenceURLs(left, right []report.Evidence) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].URL != right[index].URL {
			return false
		}
	}
	return true
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC) }

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	middleware.RequestID(handler).ServeHTTP(recorder, req)
	return recorder
}

func assertCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code {
		t.Fatalf("code=%q want=%q", payload.Error.Code, code)
	}
}
