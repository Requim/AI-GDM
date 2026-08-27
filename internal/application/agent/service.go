// Package agent 编排确定性分析、公开搜索证据和解释性报告。
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	defaultEvidenceLimit = 5
	maxEvidenceLimit     = 20
	maxQueryRunes        = 512
)

// Input 描述一次可审计的智能研判请求；AnalysisJSON 必须来自确定性用例。
type Input struct {
	Query           string
	AnalysisJSON    json.RawMessage
	ImmutableFields []string
	EvidenceLimit   int
}

// Result 同时返回权威数值输入和非权威解释性说明。
type Result struct {
	Query              string            `json:"query"`
	AnalysisJSON       json.RawMessage   `json:"analysis"`
	AnalysisSHA256     string            `json:"analysisSha256"`
	ImmutableFields    []string          `json:"immutableFields"`
	Evidence           []report.Evidence `json:"evidence"`
	Narrative          report.Narrative  `json:"narrative"`
	EvidenceAvailable  bool              `json:"evidenceAvailable"`
	NarrativeAvailable bool              `json:"narrativeAvailable"`
	Limitations        []string          `json:"limitations"`
	GeneratedAt        time.Time         `json:"generatedAt"`
}

// Service 只依赖搜索和说明生成端口；任一可选供应商失败都可降级。
type Service struct {
	search    ports.EvidenceSearcher
	generator ports.NarrativeGenerator
	clock     ports.Clock
}

// New 创建智能研判编排服务；search 和 generator 可以为空以支持确定性降级。
func New(search ports.EvidenceSearcher, generator ports.NarrativeGenerator, clock ports.Clock) (*Service, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: 智能研判时钟为空", domain.ErrInvalidInput)
	}
	return &Service{search: search, generator: generator, clock: clock}, nil
}

// Generate 先固定权威分析，再按需补充证据和解释，不把模型输出写回分析结果。
func (s *Service) Generate(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.clock == nil {
		return Result{}, fmt.Errorf("%w: 智能研判服务未正确初始化", domain.ErrInvalidInput)
	}
	input, err := normalizeInput(input)
	if err != nil {
		return Result{}, err
	}
	authoritative := report.NarrativeInput{
		AnalysisJSON:    append(json.RawMessage(nil), input.AnalysisJSON...),
		ImmutableFields: append([]string(nil), input.ImmutableFields...),
	}
	if err = authoritative.Validate(); err != nil {
		return Result{}, err
	}
	result := newResult(input, s.clock.Now().UTC())
	digest := sha256.Sum256(result.AnalysisJSON)
	result.AnalysisSHA256 = hex.EncodeToString(digest[:])
	if err = s.collectEvidence(ctx, input, &authoritative, &result); err != nil {
		return Result{}, err
	}
	if err = s.generateNarrative(ctx, authoritative, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func newResult(input Input, now time.Time) Result {
	return Result{
		Query: input.Query, AnalysisJSON: append(json.RawMessage(nil), input.AnalysisJSON...),
		ImmutableFields: append([]string(nil), input.ImmutableFields...),
		Evidence:        []report.Evidence{}, Limitations: []string{}, GeneratedAt: now.UTC(),
	}
}

func (s *Service) collectEvidence(ctx context.Context, input Input, authoritative *report.NarrativeInput, result *Result) error {
	if s.search == nil {
		addLimitation(result, "实时搜索供应商未配置，未返回外部证据")
		return nil
	}
	values, err := s.search.Search(ctx, input.Query, input.EvidenceLimit)
	if err != nil {
		if isContextError(err) {
			return fmt.Errorf("搜索灾害证据: %w", err)
		}
		addLimitation(result, "实时搜索暂时不可用，以下说明不含外部搜索证据")
		return nil
	}
	if len(values) > maxEvidenceLimit {
		values = values[:maxEvidenceLimit]
		addLimitation(result, "实时搜索结果超过编排上限，已截取前 20 条")
	}
	for index, evidence := range values {
		if err := evidence.Validate(); err != nil {
			addLimitation(result, fmt.Sprintf("第 %d 条搜索证据校验失败，已丢弃", index+1))
			continue
		}
		result.Evidence = append(result.Evidence, evidence)
	}
	result.EvidenceAvailable = len(result.Evidence) > 0
	authoritative.Evidence = append([]report.Evidence(nil), result.Evidence...)
	return nil
}

func (s *Service) generateNarrative(ctx context.Context, input report.NarrativeInput, result *Result) error {
	if s.generator == nil {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型未配置，已保留确定性分析结果")
		return nil
	}
	narrative, err := s.generator.Generate(ctx, input)
	if err != nil {
		if isContextError(err) {
			return fmt.Errorf("生成智能研判说明: %w", err)
		}
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型暂时不可用，已保留确定性分析结果")
		return nil
	}
	if err = narrative.Validate(); err != nil || !narrative.Available {
		result.Narrative = unavailableNarrative(s.clock.Now().UTC())
		addLimitation(result, "解释性大模型返回结果未通过校验，已保留确定性分析结果")
		return nil
	}
	result.Narrative, result.NarrativeAvailable = narrative, true
	return nil
}

func normalizeInput(input Input) (Input, error) {
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || len([]rune(input.Query)) > maxQueryRunes {
		return Input{}, fmt.Errorf("%w: 智能研判关键词长度无效", domain.ErrInvalidInput)
	}
	if input.EvidenceLimit < 0 || input.EvidenceLimit > maxEvidenceLimit {
		return Input{}, fmt.Errorf("%w: 搜索证据数量超过限制", domain.ErrInvalidInput)
	}
	if input.EvidenceLimit == 0 {
		input.EvidenceLimit = defaultEvidenceLimit
	}
	return input, nil
}

func unavailableNarrative(now time.Time) report.Narrative {
	return report.Narrative{Summary: "暂无解释性说明。", Caveats: []string{"请直接查看确定性分析结果，并由值班人员复核。"}, GeneratedAt: now.UTC(), Available: false}
}

func addLimitation(result *Result, value string) {
	for _, existing := range result.Limitations {
		if existing == value {
			return
		}
	}
	result.Limitations = append(result.Limitations, value)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Validate 检查编排结果没有改变权威分析字节，并验证所有输出证据。
func (r Result) Validate() error {
	if r.Query == "" || len([]rune(r.Query)) > maxQueryRunes {
		return fmt.Errorf("%w: 智能研判关键词无效", domain.ErrInvalidInput)
	}
	if len(r.AnalysisJSON) == 0 || !json.Valid(r.AnalysisJSON) || r.AnalysisSHA256 == "" {
		return fmt.Errorf("%w: 智能研判权威分析结果无效", domain.ErrInvalidInput)
	}
	if r.GeneratedAt.IsZero() || r.GeneratedAt.Location() == nil {
		return fmt.Errorf("%w: 智能研判生成时间为空", domain.ErrInvalidInput)
	}
	if _, offset := r.GeneratedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 智能研判生成时间必须使用 UTC", domain.ErrInvalidInput)
	}
	digest := sha256.Sum256(r.AnalysisJSON)
	if r.AnalysisSHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: 智能研判权威分析摘要不匹配", domain.ErrInvalidInput)
	}
	input := report.NarrativeInput{
		AnalysisJSON: r.AnalysisJSON, Evidence: r.Evidence, ImmutableFields: r.ImmutableFields,
	}
	if err := input.Validate(); err != nil {
		return fmt.Errorf("智能研判结果输入: %w", err)
	}
	if r.EvidenceAvailable != (len(r.Evidence) > 0) {
		return fmt.Errorf("%w: 搜索证据可用标志与结果不一致", domain.ErrInvalidInput)
	}
	if r.NarrativeAvailable != r.Narrative.Available {
		return fmt.Errorf("%w: 说明可用标志与结果不一致", domain.ErrInvalidInput)
	}
	if err := r.Narrative.Validate(); err != nil {
		return fmt.Errorf("智能研判说明: %w", err)
	}
	return nil
}
