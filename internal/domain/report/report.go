package report

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// Evidence 表示可追溯的实时搜索证据。
type Evidence struct {
	Title     string                `json:"title"`
	URL       string                `json:"url"`
	Summary   string                `json:"summary"`
	SiteName  string                `json:"siteName,omitempty"`
	CrawledAt time.Time             `json:"crawledAt,omitempty"`
	Source    provenance.Provenance `json:"source"`
}

// NarrativeInput 是提供给大模型的去标识化、不可变分析输入。
type NarrativeInput struct {
	AnalysisJSON    json.RawMessage `json:"analysis"`
	Evidence        []Evidence      `json:"evidence"`
	ImmutableFields []string        `json:"immutableFields"`
}

// Narrative 保存经过结构校验的大模型说明。
type Narrative struct {
	Summary     string                `json:"summary"`
	KeyFindings []string              `json:"keyFindings"`
	Actions     []string              `json:"actions"`
	Caveats     []string              `json:"caveats"`
	GeneratedAt time.Time             `json:"generatedAt"`
	Model       string                `json:"model"`
	Available   bool                  `json:"available"`
	Source      provenance.Provenance `json:"source"`
}

const (
	maxEvidenceTitle   = 512
	maxEvidenceSummary = 4096
	maxNarrativeText   = 4096
	maxNarrativeItems  = 16
)

// Validate 检查搜索证据的可追溯字段和公开 HTTPS 地址。
func (e Evidence) Validate() error {
	if strings.TrimSpace(e.Title) == "" || len([]rune(e.Title)) > maxEvidenceTitle {
		return fmt.Errorf("%w: 搜索证据标题无效", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(e.Summary) == "" || len([]rune(e.Summary)) > maxEvidenceSummary {
		return fmt.Errorf("%w: 搜索证据摘要无效", domain.ErrInvalidInput)
	}
	parsed, err := url.Parse(strings.TrimSpace(e.URL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%w: 搜索证据地址必须是无用户信息的 HTTPS 地址", domain.ErrInvalidInput)
	}
	if err = e.Source.Validate(); err != nil {
		return fmt.Errorf("搜索证据来源: %w", err)
	}
	if !e.CrawledAt.IsZero() {
		if _, offset := e.CrawledAt.Zone(); offset != 0 {
			return fmt.Errorf("%w: 搜索证据抓取时间必须使用 UTC", domain.ErrInvalidInput)
		}
	}
	return nil
}

// Validate 检查交给大模型的输入是合法 JSON，且不可变字段没有重复。
func (i NarrativeInput) Validate() error {
	if len(i.AnalysisJSON) == 0 || !json.Valid(i.AnalysisJSON) {
		return fmt.Errorf("%w: 大模型分析输入不是合法 JSON", domain.ErrInvalidInput)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(i.AnalysisJSON, &object); err != nil || object == nil {
		return fmt.Errorf("%w: 大模型分析输入必须是 JSON 对象", domain.ErrInvalidInput)
	}
	if len(i.ImmutableFields) == 0 {
		return fmt.Errorf("%w: 大模型输入缺少不可变字段清单", domain.ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(i.ImmutableFields))
	for _, field := range i.ImmutableFields {
		field = strings.TrimSpace(field)
		if field == "" {
			return fmt.Errorf("%w: 大模型不可变字段不能为空", domain.ErrInvalidInput)
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("%w: 大模型不可变字段重复", domain.ErrInvalidInput)
		}
		seen[field] = struct{}{}
	}
	for index, evidence := range i.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("第 %d 条搜索证据: %w", index+1, err)
		}
	}
	return nil
}

// Validate 检查大模型说明的有限文本结构；核心数值不属于说明输出。
func (n Narrative) Validate() error {
	if n.Available {
		if strings.TrimSpace(n.Summary) == "" || strings.TrimSpace(n.Model) == "" {
			return fmt.Errorf("%w: 可用大模型说明缺少摘要或模型名", domain.ErrInvalidInput)
		}
		if n.GeneratedAt.IsZero() {
			return fmt.Errorf("%w: 大模型说明生成时间必须使用 UTC", domain.ErrInvalidInput)
		}
		if _, offset := n.GeneratedAt.Zone(); offset != 0 {
			return fmt.Errorf("%w: 大模型说明生成时间必须使用 UTC", domain.ErrInvalidInput)
		}
	}
	if len([]rune(n.Summary)) > maxNarrativeText {
		return fmt.Errorf("%w: 大模型说明摘要过长", domain.ErrInvalidInput)
	}
	if len(n.KeyFindings) > maxNarrativeItems || len(n.Actions) > maxNarrativeItems ||
		len(n.Caveats) > maxNarrativeItems {
		return fmt.Errorf("%w: 大模型说明条目过多", domain.ErrInvalidInput)
	}
	for _, values := range [][]string{n.KeyFindings, n.Actions, n.Caveats} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len([]rune(value)) > maxNarrativeText {
				return fmt.Errorf("%w: 大模型说明条目无效", domain.ErrInvalidInput)
			}
		}
	}
	if n.Available {
		if err := n.Source.Validate(); err != nil {
			return fmt.Errorf("大模型说明来源: %w", err)
		}
	}
	return nil
}
