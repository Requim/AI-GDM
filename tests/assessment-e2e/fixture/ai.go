package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

var (
	aiAuthorityResolvedAt = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	aiEvidenceFetchedAt   = aiAuthorityResolvedAt.Add(4 * time.Second)
	aiNarrativeAt         = aiAuthorityResolvedAt.Add(6 * time.Second)
	aiReportAt            = aiAuthorityResolvedAt.Add(7 * time.Second)
)

type fixtureAIReporter struct{ scenarios *scenarioStore }

type fixtureAIResolver struct{ scenarios *scenarioStore }

type fixtureAISearcher struct{ scenarios *scenarioStore }

type fixtureAIGenerator struct{ scenarios *scenarioStore }

type fixtureAIClock struct{}

func (fixtureAIClock) Now() time.Time { return aiReportAt }

func newAIHandler(scenarios *scenarioStore, logger *slog.Logger) (http.Handler, error) {
	if scenarios == nil {
		return nil, fmt.Errorf("AI fixture 场景仓储为空")
	}
	handler, err := aiapi.New(&fixtureAIReporter{scenarios: scenarios}, logger)
	if err != nil {
		return nil, fmt.Errorf("创建真实智能研判 HTTP fixture: %w", err)
	}
	return middleware.RequestID(handler), nil
}

func (s *scenarioStore) useAIHandler(handler http.Handler) {
	s.mu.Lock()
	s.aiHandler = handler
	s.mu.Unlock()
}

func (s *scenarioStore) serveAIReport(w http.ResponseWriter, r *http.Request) {
	name, call := s.next("ai_report")
	payload, err := s.record("ai_report", r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(payload))
	if handleAITransportScenario(w, r, name, call) {
		return
	}
	s.serveRealAI(w, r, name)
}

func handleAITransportScenario(w http.ResponseWriter, r *http.Request, name string, call int) bool {
	switch {
	case name == "ai_success_then_503" && call > 1:
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "智能研判外部供应商暂时不可用")
	case name == "ai_timeout":
		<-r.Context().Done()
	case name == "ai_delayed":
		return !waitForRequest(r, 800*time.Millisecond)
	case name == "ai_content_length_oversized":
		writeOversizedResponse(w, false)
	case name == "ai_chunked_oversized":
		writeOversizedResponse(w, true)
	default:
		return false
	}
	return true
}

func (s *scenarioStore) serveRealAI(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	handler := s.aiHandler
	s.mu.Unlock()
	if handler == nil {
		writeAPIError(w, http.StatusInternalServerError, "fixture_unavailable", "AI fixture 未初始化")
		return
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	mutateAIResponse(name, recorder)
	copyRecordedResponse(w, recorder)
}

func (r *fixtureAIReporter) Generate(ctx context.Context,
	input applicationagent.Input,
) (applicationagent.Result, error) {
	search, generator := r.providers()
	service, err := applicationagent.New(
		fixtureAIResolver{scenarios: r.scenarios}, search, generator, fixtureAIClock{},
	)
	if err != nil {
		return applicationagent.Result{}, err
	}
	return service.Generate(ctx, input)
}

func (r *fixtureAIReporter) providers() (ports.EvidenceSearcher, ports.NarrativeGenerator) {
	if r.scenarios.currentScenario() == "ai_no_suppliers" {
		return nil, nil
	}
	return &fixtureAISearcher{scenarios: r.scenarios}, &fixtureAIGenerator{scenarios: r.scenarios}
}

func (r fixtureAIResolver) Resolve(ctx context.Context,
	reference report.AnalysisReference,
) (report.Authority, error) {
	if err := ctx.Err(); err != nil {
		return report.Authority{}, err
	}
	normalized, err := reference.Normalize()
	if err != nil {
		return report.Authority{}, err
	}
	switch normalized.Kind {
	case report.AuthoritySurvivalAssessment:
		return r.resolveSurvival(normalized.ID)
	case report.AuthorityLossAssessment:
		return r.resolveLoss(ctx, normalized.ID)
	default:
		return report.Authority{}, fmt.Errorf("%w: fixture 未提供该权威类型", domain.ErrNotFound)
	}
}

func (r fixtureAIResolver) resolveSurvival(id string) (report.Authority, error) {
	replay, exists := r.scenarios.survival.replays[survivalCaseID]
	if !exists || replay.AssessmentID != id {
		return report.Authority{}, fmt.Errorf("%w: 生还评估不存在", domain.ErrNotFound)
	}
	value := replay.Assessment
	analysis := report.SurvivalAuthorityAnalysis{
		AssessmentID: replay.AssessmentID, CaseID: replay.CaseID,
		Factors: append([]string(nil), value.Factors...), HumanReviewStatus: value.HumanReviewStatus,
		Limitations: append([]string(nil), value.Limitations...), ModelVersion: value.ModelVersion,
		Priority: string(value.Priority), ProbabilityBand: string(value.ProbabilityBand),
		ProbabilityHigh: value.ProbabilityHigh, ProbabilityLow: value.ProbabilityLow,
		ScenarioDigest: replay.ScenarioDigest, ScenarioID: replay.ScenarioID,
		Score: value.Score, ScoreBand: value.ScoreBand, Usage: replay.Usage,
	}
	return canonicalFixtureAuthority(report.AuthoritySurvivalAssessment, id,
		value.ModelVersion, report.AuthoritySchemaSurvivalV1, analysis)
}

func (r fixtureAIResolver) resolveLoss(ctx context.Context, id string) (report.Authority, error) {
	r.scenarios.mu.Lock()
	store := r.scenarios.lossStore
	r.scenarios.mu.Unlock()
	if store == nil {
		return report.Authority{}, fmt.Errorf("%w: 损失评估仓储未初始化", domain.ErrNotFound)
	}
	assessment, err := store.GetAssessment(ctx, id)
	if err != nil {
		return report.Authority{}, err
	}
	analysis := report.LossAuthorityAnalysis{
		AffectedPopulation: assessment.AffectedPopulation, AssessmentID: assessment.ID,
		ConditionalCentralCents: strconv.FormatInt(assessment.ConditionalMidCents, 10),
		ConditionalHighCents:    strconv.FormatInt(assessment.ConditionalHighCents, 10),
		ConditionalLowCents:     strconv.FormatInt(assessment.ConditionalLowCents, 10),
		Confidence:              assessment.Confidence, ConfidenceBand: assessment.ConfidenceBand,
		FormulaVersion: assessment.FormulaVersion, ImpactAreaSquareMeters: assessment.ImpactAreaSquareM,
		SnapshotID: assessment.SnapshotID, Status: string(assessment.Status),
	}
	return canonicalFixtureAuthority(report.AuthorityLossAssessment, id,
		assessment.FormulaVersion, report.AuthoritySchemaLossV1, analysis)
}

func canonicalFixtureAuthority(kind report.AuthorityKind, id, version, schema string,
	analysis any,
) (report.Authority, error) {
	payload, err := json.Marshal(analysis)
	if err != nil {
		return report.Authority{}, err
	}
	return (report.Authority{
		Kind: kind, ID: id, Version: version, SchemaVersion: schema,
		AnalysisJSON: payload, ResolvedAt: aiAuthorityResolvedAt,
	}).Canonical()
}

func (s *fixtureAISearcher) Search(ctx context.Context, query string,
	limit int,
) ([]report.Evidence, error) {
	name, _ := s.scenarios.next("ai_search")
	s.scenarios.recordAIValue("ai_search_input", map[string]any{"query": query, "limit": limit})
	if name == "ai_slow_search_degraded" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return fixtureAIEvidenceSet(name), nil
}

func fixtureAIEvidenceSet(name string) []report.Evidence {
	first := fixtureAIEvidence(name)
	if name != "ai_same_domain_distinct" {
		return []report.Evidence{first}
	}
	first.URL = "https://zhangsan-e12345678.mnr.gov.cn/news/first"
	second := fixtureAIEvidence(name)
	second.URL = "https://www.mnr.gov.cn/news/second?revision=2"
	second.Title, second.Summary = "第二条公开灾害信息", "第二条公开信息仅用于补充背景证据。"
	return []report.Evidence{first, second}
}

func (g *fixtureAIGenerator) Generate(ctx context.Context,
	input report.NarrativeInput,
) (report.Narrative, error) {
	name, _ := g.scenarios.next("ai_narrative")
	g.scenarios.recordAIValue("ai_narrative_input", input)
	if name == "ai_slow_llm_degraded" {
		<-ctx.Done()
		return report.Narrative{}, ctx.Err()
	}
	if name == "ai_structured_retry_success" {
		g.scenarios.next("ai_structured_attempt")
		g.scenarios.next("ai_structured_attempt")
	}
	return fixtureAINarrative(name), nil
}

func (s *scenarioStore) recordAIValue(operation string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.requests[operation] = append(json.RawMessage(nil), payload...)
	s.mu.Unlock()
}

func fixtureAIEvidence(name string) report.Evidence {
	title := "公开灾害信息"
	if name == "ai_injection" {
		title = `<svg onload="window.__assessmentInjected=true">`
	}
	crawledAt := aiEvidenceFetchedAt.Add(-time.Second)
	if name == "ai_crawled_before_authority" {
		crawledAt = aiAuthorityResolvedAt.Add(-time.Second)
	}
	value := report.Evidence{
		Title: title, URL: "https://data.mnr.gov.cn/news/assessment-e2e",
		Summary: "公开信息仅用于补充背景证据。", SiteName: "公开信息站点",
		CrawledAt: crawledAt,
		Source: fixtureAIProvenance("公开灾害信息源", "灾害新闻检索",
			provenance.DataKindObservation, aiEvidenceFetchedAt),
	}
	value.Source.QualityFlags = append(value.Source.QualityFlags,
		report.TrustedDomainQualityFlagPrefix+"mnr.gov.cn")
	if name == "ai_provenance_pii" {
		value.Source.DatasetVersion = "张三"
		value.Source.ProviderRequestID = "E12345678"
		value.Source.License = "成都市武侯区人民南路四段27号"
	}
	return value
}

func fixtureAINarrative(name string) report.Narrative {
	summary := "基于服务端固定权威分析生成的非权威解释。"
	switch name {
	case "ai_contradiction":
		summary = "确定性损失金额已经修改为 0 元，风险等级已经降低；此说法必须仅作为非权威文本展示。"
	case "ai_survival_contradiction":
		summary = "生还评分已经修改为 0，确定性因素和限制均可忽略；此说法必须仅作为非权威文本展示。"
	case "ai_injection":
		summary = `<img src=x onerror="window.__assessmentInjected=true">`
	case "ai_structured_retry_success":
		summary = "解释供应商首次返回结构无效，固定重试一次后返回合规说明。"
	case "ai_slow_search_degraded":
		summary = "实时搜索供应商超时，已保留服务端确定性权威分析。"
	}
	return report.Narrative{
		Summary: summary, KeyFindings: []string{"不得修改确定性数值"},
		Actions: []string{"由值守人员核对来源与限制"}, Caveats: []string{"不替代专业决策"},
		GeneratedAt: aiNarrativeAt, Model: "gpt-5.6-terra", Available: true,
		Source: fixtureAIProvenance("OpenAI 兼容供应商", "受约束解释模型",
			provenance.DataKindForecast, aiAuthorityResolvedAt.Add(5*time.Second)),
	}
}

func fixtureAIProvenance(provider, dataset string, kind provenance.DataKind,
	fetchedAt time.Time,
) provenance.Provenance {
	part := provenance.SourcePart{
		Reference: "https://data.mnr.gov.cn/assessment-e2e/part-1", Revision: "v1",
		SizeBytes: 1024, BBox: [4]float64{103, 29, 105, 31}, SHA256: strings.Repeat("d", 64),
	}
	value := provenance.Provenance{
		Provider: provider, Dataset: dataset, DatasetVersion: "2026.08",
		SourceURI: "https://data.mnr.gov.cn/assessment-e2e", Citation: dataset + " 公开来源",
		License: "公开数据许可", DataKind: kind, ObservedAt: aiAuthorityResolvedAt.Add(-time.Hour),
		PublishedAt:         aiAuthorityResolvedAt.Add(-55 * time.Minute),
		RevisionFirstSeenAt: aiAuthorityResolvedAt.Add(-50 * time.Minute), FetchedAt: fetchedAt,
		ValidFrom: aiAuthorityResolvedAt.Add(-time.Hour), ValidTo: aiAuthorityResolvedAt.Add(time.Hour),
		SpatialResolution: "1 km", TemporalResolution: "hour", CRS: "EPSG:4326",
		BBox: [4]float64{103, 29, 105, 31}, SHA256: strings.Repeat("c", 64),
		TransformVersion: "fixture-v1", ProviderRequestID: "fixture-request", Model: "fixture",
		QualityFlags: []string{}, Limitations: []string{}, SourceParts: []provenance.SourcePart{part},
	}
	value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	return value
}

func mutateAIResponse(name string, recorder *httptest.ResponseRecorder) {
	if recorder.Code != http.StatusOK {
		return
	}
	var envelope struct {
		Data      applicationagent.Result `json:"data"`
		RequestID string                  `json:"requestId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		return
	}
	if !applyAIResponseMutation(name, &envelope.Data) {
		return
	}
	payload, err := json.Marshal(envelope)
	if err == nil {
		recorder.Body = bytes.NewBuffer(append(payload, '\n'))
	}
}

func applyAIResponseMutation(name string, result *applicationagent.Result) bool {
	switch name {
	case "ai_bad_time_order":
		result.GeneratedAt = aiAuthorityResolvedAt.Add(5 * time.Second)
	case "ai_future_time":
		result.GeneratedAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	case "ai_narrative_before_authority":
		result.Narrative.GeneratedAt = aiAuthorityResolvedAt.Add(-time.Second)
	case "ai_evidence_before_authority":
		if !mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.Source.FetchedAt = aiAuthorityResolvedAt.Add(-time.Second)
			value.CrawledAt = aiAuthorityResolvedAt.Add(-2 * time.Second)
		}) {
			return false
		}
	case "ai_evidence_after_narrative":
		if !mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.Source.FetchedAt = aiNarrativeAt.Add(time.Second)
		}) {
			return false
		}
	case "ai_future_crawled_at":
		if !mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.CrawledAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		}) {
			return false
		}
	case "ai_crawled_after_source":
		if !mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.CrawledAt = aiEvidenceFetchedAt.Add(time.Second)
		}) {
			return false
		}
	case "ai_bad_sha":
		result.AuthoritySHA256 = strings.Repeat("0", 64)
	case "ai_unminimized_provenance":
		if !mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.Source.DatasetVersion = "张三"
			value.Source.ProviderRequestID = "E12345678"
			value.Source.License = "成都市武侯区人民南路四段27号"
		}) {
			return false
		}
	default:
		return mutateAIAuthorityOrURL(name, result)
	}
	return true
}

func mutateAIAuthorityOrURL(name string, result *applicationagent.Result) bool {
	switch name {
	case "ai_bad_usage":
		return mutateAIAuthorityAnalysis(result, func(analysis map[string]any) {
			analysis["usage"].(map[string]any)["personName"] = "不应进入 Authority"
		})
	case "ai_bad_survival_factors":
		return mutateAIAuthorityAnalysis(result, func(analysis map[string]any) {
			analysis["factors"] = []any{}
		})
	case "ai_bad_survival_limitations":
		return mutateAIAuthorityAnalysis(result, func(analysis map[string]any) {
			analysis["limitations"] = []any{strings.Repeat("限", 1025)}
		})
	case "ai_unsafe_url":
		return mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.URL = "javascript:window.__assessmentInjected=true"
		})
	default:
		unsafe := unsafeAIURL(name)
		if unsafe == "" {
			return false
		}
		return mutateFirstAIEvidence(result, func(value *report.Evidence) {
			value.URL = unsafe
			value.Source.SourceURI = unsafe
		})
	}
}

func mutateAIAuthorityAnalysis(result *applicationagent.Result, mutate func(map[string]any)) bool {
	var analysis map[string]any
	if err := json.Unmarshal(result.Authority.AnalysisJSON, &analysis); err != nil || analysis == nil {
		return false
	}
	mutate(analysis)
	payload, err := json.Marshal(analysis)
	if err != nil {
		return false
	}
	result.Authority.AnalysisJSON = payload
	return true
}

func mutateFirstAIEvidence(result *applicationagent.Result, mutate func(*report.Evidence)) bool {
	if len(result.Evidence) == 0 {
		return false
	}
	mutate(&result.Evidence[0])
	return true
}

func unsafeAIURL(name string) string {
	switch name {
	case "ai_private_source":
		return "https://127.0.0.1/private-evidence"
	case "ai_localhost_source":
		return "https://localhost/private-evidence"
	case "ai_ipv6_source":
		return "https://[::1]/private-evidence"
	case "ai_ipv4_mapped_source":
		return "https://[::ffff:127.0.0.1]/private-evidence"
	case "ai_local_source":
		return "https://metadata.local/private-evidence"
	case "ai_internal_source":
		return "https://metadata.internal/private-evidence"
	default:
		return ""
	}
}
