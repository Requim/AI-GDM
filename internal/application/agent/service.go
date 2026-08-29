// Package agent 编排确定性分析、公开搜索证据和解释性报告。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	defaultEvidenceLimit      = 5
	maxEvidenceLimit          = 20
	maxNarrativeEvidenceBytes = 384 << 10
	maxSerializedResultBytes  = (1 << 20) - 512
	// SearchStageTimeout 包含搜索限流排队和单次 HTTP 请求预算。
	SearchStageTimeout = 7 * time.Second
	// NarrativeStageTimeout 包含 LLM 限流排队和最多三次确定性结构修复预算。
	NarrativeStageTimeout = 32 * time.Second
)

// Input 描述一次基于服务端权威引用的智能研判请求。
type Input struct {
	AnalysisRef   report.AnalysisReference
	EvidenceLimit int
}

// Result 隔离确定性 Authority 和不具备决策权的解释性 Narrative。
type Result struct {
	Authority                report.Authority  `json:"authority"`
	AuthoritySHA256          string            `json:"authoritySha256"`
	AuthorityEnvelopeVersion string            `json:"authorityEnvelopeVersion"`
	Evidence                 []report.Evidence `json:"evidence"`
	Narrative                report.Narrative  `json:"narrative"`
	EvidenceAvailable        bool              `json:"evidenceAvailable"`
	NarrativeAvailable       bool              `json:"narrativeAvailable"`
	Limitations              []string          `json:"limitations"`
	GeneratedAt              time.Time         `json:"generatedAt"`
}

// Service 先解析服务端权威对象，再调用可选的搜索和说明供应商。
type Service struct {
	resolver  AuthoritativeAnalysisResolver
	search    ports.EvidenceSearcher
	generator ports.NarrativeGenerator
	clock     ports.Clock
	searchTTL time.Duration
	llmTTL    time.Duration
}

// New 创建智能研判编排服务；resolver 和 clock 必填，可选供应商允许为空。
func New(resolver AuthoritativeAnalysisResolver, search ports.EvidenceSearcher,
	generator ports.NarrativeGenerator, clock ports.Clock,
) (*Service, error) {
	if resolver == nil || clock == nil {
		return nil, fmt.Errorf("%w: 权威分析解析器或智能研判时钟为空", domain.ErrInvalidInput)
	}
	return &Service{
		resolver: resolver, search: search, generator: generator, clock: clock,
		searchTTL: SearchStageTimeout, llmTTL: NarrativeStageTimeout,
	}, nil
}

// Generate 固定权威分析后补充证据和说明，模型输出不会写回 Authority。
func (s *Service) Generate(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.resolver == nil || s.clock == nil {
		return Result{}, fmt.Errorf("%w: 智能研判服务未正确初始化", domain.ErrInvalidInput)
	}
	if err := requestContextError(ctx, "智能研判请求已终止"); err != nil {
		return Result{}, err
	}
	input, err := normalizeInput(input)
	if err != nil {
		return Result{}, err
	}
	if err = requestContextError(ctx, "调用权威分析解析器"); err != nil {
		return Result{}, err
	}
	authority, resolveErr := s.resolveAuthority(ctx, input.AnalysisRef)
	if err = requestContextError(ctx, "解析权威分析"); err != nil {
		return Result{}, err
	}
	if resolveErr != nil {
		return Result{}, resolveErr
	}
	authority = cloneAuthority(authority)
	narrativeInput := narrativeInputFromAuthority(authority)
	if err = narrativeInput.Validate(); err != nil {
		return Result{}, fmt.Errorf("构造大模型权威输入: %w", err)
	}
	digest, err := authoritySHA256(authority)
	if err != nil {
		return Result{}, fmt.Errorf("计算权威分析摘要: %w", err)
	}
	result := newResult(authority, digest)
	if err = s.collectEvidence(ctx, input, &narrativeInput, &result); err != nil {
		return Result{}, err
	}
	if err = s.generateNarrative(ctx, narrativeInput, &result); err != nil {
		return Result{}, err
	}
	if err = requestContextError(ctx, "完成智能研判报告"); err != nil {
		return Result{}, err
	}
	result.GeneratedAt = s.clock.Now().UTC()
	if err = enforceResultBudget(&result); err != nil {
		return Result{}, err
	}
	if err = requestContextError(ctx, "发布智能研判报告"); err != nil {
		return Result{}, err
	}
	return result, nil
}

func newResult(authority report.Authority, digest string) Result {
	return Result{
		Authority: cloneAuthority(authority), AuthoritySHA256: digest,
		AuthorityEnvelopeVersion: AuthorityEnvelopeVersion,
		Evidence:                 []report.Evidence{}, Limitations: []string{},
	}
}

func (s *Service) collectEvidence(ctx context.Context, input Input,
	narrativeInput *report.NarrativeInput, result *Result,
) error {
	if err := requestContextError(ctx, "搜索灾害证据"); err != nil {
		return err
	}
	if s.search == nil {
		addLimitation(result, "实时搜索供应商未配置，未返回外部证据")
		return nil
	}
	searchCtx, cancel := context.WithTimeout(ctx, s.searchTTL)
	defer cancel()
	if parentErr := contextCompletionError(ctx); parentErr != nil {
		return fmt.Errorf("搜索灾害证据: %w", parentErr)
	}
	if stageErr := contextCompletionError(searchCtx); stageErr != nil {
		addLimitation(result, "实时搜索暂时不可用，以下说明不含外部搜索证据")
		return nil
	}
	values, err := s.search.Search(searchCtx, searchQueryForAuthority(result.Authority.Kind), input.EvidenceLimit)
	parentErr := contextCompletionError(ctx)
	stageErr := contextCompletionError(searchCtx)
	if parentErr != nil {
		return fmt.Errorf("搜索灾害证据: %w", parentErr)
	}
	if err == nil && stageErr != nil {
		err = stageErr
	}
	if err != nil {
		addLimitation(result, "实时搜索暂时不可用，以下说明不含外部搜索证据")
		return nil
	}
	limit := min(input.EvidenceLimit, maxEvidenceLimit)
	if len(values) > limit {
		values = values[:limit:limit]
		addLimitation(result, fmt.Sprintf("实时搜索结果超过请求上限，已截取前 %d 条", limit))
	}
	result.Evidence = filterEvidence(values, result.Authority.ResolvedAt, s.clock.Now().UTC(), result)
	if len(values) == 0 {
		addLimitation(result, "实时搜索未返回可用证据")
	} else if len(result.Evidence) == 0 {
		addLimitation(result, "实时搜索结果均未通过校验")
	}
	result.EvidenceAvailable = len(result.Evidence) > 0
	narrativeInput.Evidence = cloneEvidenceSlice(result.Evidence)
	return requestContextError(ctx, "完成搜索灾害证据")
}

func filterEvidence(values []report.Evidence, lower, upper time.Time, result *Result) []report.Evidence {
	filtered := make([]report.Evidence, 0, len(values))
	seenReferences := make(map[string]struct{}, len(values))
	usedBytes := 2
	minimizedCount, duplicateCount := 0, 0
	for index, evidence := range values {
		if err := evidence.Validate(); err != nil {
			addLimitation(result, fmt.Sprintf("第 %d 条搜索证据校验失败，已丢弃", index+1))
			continue
		}
		if evidenceContainsSensitiveData(evidence) {
			addLimitation(result, fmt.Sprintf("第 %d 条搜索证据疑似包含个人信息，已丢弃", index+1))
			continue
		}
		if !evidenceWithinWindow(evidence, lower, upper) {
			addLimitation(result, fmt.Sprintf("第 %d 条搜索证据时间契约无效，未发送给大模型", index+1))
			continue
		}
		minimized, err := minimizedEvidence(evidence)
		if err != nil {
			addLimitation(result, fmt.Sprintf("第 %d 条搜索证据无法安全最小化，已丢弃", index+1))
			continue
		}
		reference := minimized.Source.SourceRevision
		if _, exists := seenReferences[reference]; exists {
			duplicateCount++
			continue
		}
		payload, err := json.Marshal(minimized)
		if err != nil || usedBytes+len(payload)+1 > maxNarrativeEvidenceBytes {
			addLimitation(result, "搜索证据超过可返回预算，已在发送给大模型前省略")
			continue
		}
		seenReferences[reference] = struct{}{}
		filtered, usedBytes = append(filtered, minimized), usedBytes+len(payload)+1
		minimizedCount++
	}
	if minimizedCount > 0 {
		addLimitation(result, "响应证据仅保留可信基域、时间和不可逆条目引用；大模型提示不发送 URL、主机、原始标题或摘要")
	}
	if duplicateCount > 0 {
		addLimitation(result, fmt.Sprintf("去标识化后有 %d 条证据条目重复，已按不可逆审计引用去重", duplicateCount))
	}
	return filtered
}

func (s *Service) generateNarrative(ctx context.Context, input report.NarrativeInput,
	result *Result,
) error {
	if err := requestContextError(ctx, "生成智能研判说明"); err != nil {
		return err
	}
	if s.generator == nil {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型未配置，已保留确定性分析结果")
		return nil
	}
	llmCtx, cancel := context.WithTimeout(ctx, s.llmTTL)
	defer cancel()
	if parentErr := contextCompletionError(ctx); parentErr != nil {
		return fmt.Errorf("生成智能研判说明: %w", parentErr)
	}
	if stageErr := contextCompletionError(llmCtx); stageErr != nil {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型暂时不可用，已保留确定性分析结果")
		return nil
	}
	narrative, err := s.generator.Generate(llmCtx, cloneNarrativeInput(input))
	parentErr := contextCompletionError(ctx)
	stageErr := contextCompletionError(llmCtx)
	if parentErr != nil {
		return fmt.Errorf("生成智能研判说明: %w", parentErr)
	}
	if err == nil && stageErr != nil {
		err = stageErr
	}
	if err != nil {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型暂时不可用，已保留确定性分析结果")
		return nil
	}
	narrative = normalizeNarrativeSlices(cloneNarrative(narrative))
	if err = narrative.Validate(); err == nil && narrative.Available {
		err = validateNarrativeEvidenceOrder(narrative, input.Evidence)
	}
	if err != nil || !narrative.Available {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型返回结果未通过校验，已保留确定性分析结果")
		return nil
	}
	result.Narrative, result.NarrativeAvailable = narrative, true
	return nil
}

func contextCompletionError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func requestContextError(ctx context.Context, operation string) error {
	if err := contextCompletionError(ctx); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func evidenceWithinWindow(value report.Evidence, lower, upper time.Time) bool {
	if upper.Before(lower) || value.Source.FetchedAt.Before(lower) || value.Source.FetchedAt.After(upper) {
		return false
	}
	if value.CrawledAt.IsZero() {
		return true
	}
	return !value.CrawledAt.After(upper) && !value.CrawledAt.After(value.Source.FetchedAt)
}

func validateNarrativeEvidenceOrder(value report.Narrative, evidence []report.Evidence) error {
	for index, item := range evidence {
		if err := validateEvidenceNarrativeOrder(fmt.Sprintf("第 %d 条证据", index+1),
			item, value.GeneratedAt); err != nil {
			return err
		}
	}
	return nil
}

func normalizeInput(input Input) (Input, error) {
	reference, err := input.AnalysisRef.Normalize()
	if err != nil {
		return Input{}, err
	}
	input.AnalysisRef = reference
	if input.EvidenceLimit < 0 || input.EvidenceLimit > maxEvidenceLimit {
		return Input{}, fmt.Errorf("%w: 搜索证据数量超过限制", domain.ErrInvalidInput)
	}
	if input.EvidenceLimit == 0 {
		input.EvidenceLimit = defaultEvidenceLimit
	}
	return input, nil
}

func unavailableNarrative(now time.Time) report.Narrative {
	return report.Narrative{
		Summary: "暂无解释性说明。", KeyFindings: []string{}, Actions: []string{},
		Caveats: []string{}, GeneratedAt: now.UTC(), Available: false,
	}
}

func normalizeNarrativeSlices(value report.Narrative) report.Narrative {
	if value.KeyFindings == nil {
		value.KeyFindings = []string{}
	}
	if value.Actions == nil {
		value.Actions = []string{}
	}
	if value.Caveats == nil {
		value.Caveats = []string{}
	}
	return value
}

func enforceResultBudget(result *Result) error {
	if resultFitsBudget(*result) {
		return nil
	}
	result.Narrative = unavailableNarrative(result.GeneratedAt)
	result.NarrativeAvailable = false
	addLimitation(result, "智能研判组合结果超过安全预算，解释性说明已降级")
	if resultFitsBudget(*result) {
		return nil
	}
	addLimitation(result, "外部证据超过最终响应预算，已从尾部省略")
	for len(result.Evidence) > 0 && !resultFitsBudget(*result) {
		result.Evidence = result.Evidence[:len(result.Evidence)-1]
	}
	result.EvidenceAvailable = len(result.Evidence) > 0
	if !resultFitsBudget(*result) {
		return fmt.Errorf("%w: 权威分析超过智能研判响应预算", report.ErrInvalidAuthority)
	}
	return nil
}

func resultFitsBudget(result Result) bool {
	payload, err := json.Marshal(result)
	return err == nil && len(payload) <= maxSerializedResultBytes
}

func addLimitation(result *Result, value string) {
	for _, existing := range result.Limitations {
		if existing == value {
			return
		}
	}
	result.Limitations = append(result.Limitations, value)
}

// Validate 检查权威分析摘要、输出来源和确定性/解释性隔离边界。
func (r Result) Validate() error {
	if r.AuthorityEnvelopeVersion != AuthorityEnvelopeVersion || r.AuthoritySHA256 == "" {
		return fmt.Errorf("%w: 权威分析摘要版本无效", domain.ErrInvalidInput)
	}
	if err := r.Authority.Validate(); err != nil {
		return fmt.Errorf("智能研判 Authority: %w", err)
	}
	digest, err := authoritySHA256(r.Authority)
	if err != nil || digest != r.AuthoritySHA256 {
		return fmt.Errorf("%w: 权威分析摘要不匹配", domain.ErrInvalidInput)
	}
	if r.GeneratedAt.IsZero() {
		return fmt.Errorf("%w: 智能研判生成时间为空", domain.ErrInvalidInput)
	}
	if _, offset := r.GeneratedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 智能研判生成时间必须使用 UTC", domain.ErrInvalidInput)
	}
	if r.Evidence == nil || r.Limitations == nil {
		return fmt.Errorf("%w: 智能研判证据和限制数组不能为 null", domain.ErrInvalidInput)
	}
	input := report.NarrativeInput{
		AnalysisJSON: r.Authority.AnalysisJSON, Evidence: r.Evidence,
		ImmutableFields: r.Authority.ImmutableFields,
	}
	if err = input.Validate(); err != nil {
		return fmt.Errorf("智能研判结果输入: %w", err)
	}
	if r.EvidenceAvailable != (len(r.Evidence) > 0) {
		return fmt.Errorf("%w: 搜索证据可用标志与结果不一致", domain.ErrInvalidInput)
	}
	if r.NarrativeAvailable != r.Narrative.Available {
		return fmt.Errorf("%w: 说明可用标志与结果不一致", domain.ErrInvalidInput)
	}
	if r.Narrative.KeyFindings == nil || r.Narrative.Actions == nil || r.Narrative.Caveats == nil {
		return fmt.Errorf("%w: 智能研判说明数组不能为 null", domain.ErrInvalidInput)
	}
	if err = r.Narrative.Validate(); err != nil {
		return fmt.Errorf("智能研判说明: %w", err)
	}
	if err = r.validateTemporalOrder(); err != nil {
		return err
	}
	return nil
}

func (r Result) validateTemporalOrder() error {
	if r.Authority.ResolvedAt.After(r.GeneratedAt) {
		return fmt.Errorf("%w: 权威分析解析时间晚于报告生成时间", domain.ErrInvalidInput)
	}
	if err := validateComponentTime("解释性说明生成时间", r.Narrative.GeneratedAt,
		r.Authority.ResolvedAt, r.GeneratedAt, true); err != nil {
		return err
	}
	if _, offset := r.Narrative.GeneratedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 解释性说明生成时间必须使用 UTC", domain.ErrInvalidInput)
	}
	for index, evidence := range r.Evidence {
		prefix := fmt.Sprintf("第 %d 条证据", index+1)
		if err := validateHistoricalCrawlTime(prefix, evidence, r.GeneratedAt); err != nil {
			return err
		}
		if err := validateComponentTime(prefix+"来源获取时间", evidence.Source.FetchedAt,
			r.Authority.ResolvedAt, r.GeneratedAt, true); err != nil {
			return err
		}
		if r.Narrative.Available {
			if err := validateEvidenceNarrativeOrder(prefix, evidence, r.Narrative.GeneratedAt); err != nil {
				return err
			}
		}
	}
	if r.Narrative.Available {
		return validateComponentTime("说明来源获取时间", r.Narrative.Source.FetchedAt,
			r.Authority.ResolvedAt, r.GeneratedAt, true)
	}
	return nil
}

func validateHistoricalCrawlTime(prefix string, evidence report.Evidence, upper time.Time) error {
	if evidence.CrawledAt.IsZero() {
		return nil
	}
	if evidence.CrawledAt.After(upper) {
		return fmt.Errorf("%w: %s抓取时间晚于报告生成时间", domain.ErrInvalidInput, prefix)
	}
	if evidence.CrawledAt.After(evidence.Source.FetchedAt) {
		return fmt.Errorf("%w: %s抓取时间晚于本次来源获取时间", domain.ErrInvalidInput, prefix)
	}
	return nil
}

func validateEvidenceNarrativeOrder(prefix string, evidence report.Evidence, narrativeAt time.Time) error {
	if !evidence.CrawledAt.IsZero() && evidence.CrawledAt.After(narrativeAt) {
		return fmt.Errorf("%w: %s抓取时间晚于解释性说明生成时间", domain.ErrInvalidInput, prefix)
	}
	if evidence.Source.FetchedAt.After(narrativeAt) {
		return fmt.Errorf("%w: %s来源获取时间晚于解释性说明生成时间", domain.ErrInvalidInput, prefix)
	}
	return nil
}

func validateComponentTime(name string, value, lower, upper time.Time, required bool) error {
	if value.IsZero() {
		if required {
			return fmt.Errorf("%w: %s为空", domain.ErrInvalidInput, name)
		}
		return nil
	}
	if value.Before(lower) {
		return fmt.Errorf("%w: %s早于权威分析解析时间", domain.ErrInvalidInput, name)
	}
	if value.After(upper) {
		return fmt.Errorf("%w: %s晚于报告生成时间", domain.ErrInvalidInput, name)
	}
	return nil
}
