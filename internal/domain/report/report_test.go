package report

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestEvidenceValidate(t *testing.T) {
	evidence := validEvidence()
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]Evidence{
		"http 地址": func() Evidence { value := evidence; value.URL = "http://example.test/news"; return value }(),
		"缺少摘要":    func() Evidence { value := evidence; value.Summary = ""; return value }(),
		"来源不完整":   func() Evidence { value := evidence; value.Source.Provider = ""; return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestNarrativeInputValidateRejectsDuplicateImmutableFields(t *testing.T) {
	input := NarrativeInput{AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel", "riskLevel"}}
	if err := input.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNarrativeInputValidateChecksEvidence(t *testing.T) {
	input := NarrativeInput{
		AnalysisJSON: json.RawMessage("{\"riskLevel\":\"high\"}"), Evidence: []Evidence{validEvidence()},
		ImmutableFields: []string{"riskLevel"},
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNarrativeValidate(t *testing.T) {
	narrative := Narrative{
		Summary:     "风险区已由确定性模型计算，以下说明仅供辅助研判。",
		KeyFindings: []string{"降雨证据来自公开来源。"}, Actions: []string{"由值班人员核验现场情况。"},
		Caveats: []string{"不替代官方预警。"}, GeneratedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
		Model: "gpt-5.6-terra", Available: true, Source: validSource("llm"),
	}
	if err := narrative.Validate(); err != nil {
		t.Fatal(err)
	}
	value := narrative
	value.Source.Provider = ""
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func validEvidence() Evidence {
	return Evidence{Title: "公开灾害信息", URL: "https://example.test/news/1", Summary: "公开摘要", SiteName: "示例来源",
		CrawledAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC), Source: validSource("bocha")}
}

func validSource(provider string) provenance.Provenance {
	return provenance.Provenance{Provider: provider, Dataset: "test", SourceURI: "https://example.test/api",
		DataKind: provenance.DataKindObservation, FetchedAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)}
}
