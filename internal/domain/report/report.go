package report

import (
	"encoding/json"
	"time"

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
