package report

import (
	"encoding/json"
	"errors"
	"strings"
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
		"站点名称过长":  func() Evidence { value := evidence; value.SiteName = strings.Repeat("站", 257); return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestEvidenceValidateRejectsNonPublicHTTPSHosts(t *testing.T) {
	for _, rawURL := range []string{
		"https://127.0.0.1/private", "https://127.1/private", "https://0177.0.0.1/private",
		"https://0x7f.0.0.1/private", "https://2130706433/private", "https://[::1]/private",
		"https://metadata.internal/private", "https://localhost/private", "https://host.local/private",
		"https://10.0.0.1/private", "https://169.254.169.254/latest", "https://example.com:8443/private",
	} {
		t.Run(rawURL, func(t *testing.T) {
			value := validEvidence()
			value.URL = rawURL
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

func TestEvidenceValidateAcceptsPublicHTTPSHost(t *testing.T) {
	for _, rawURL := range []string{"https://www.mnr.gov.cn/news", "https://news.example.test:443/item"} {
		value := validEvidence()
		value.URL = rawURL
		if err := value.Validate(); err != nil {
			t.Fatalf("Validate(%q) error=%v", rawURL, err)
		}
	}
}

func TestNarrativeInputValidateRejectsDuplicateImmutableFields(t *testing.T) {
	input := NarrativeInput{AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel", "riskLevel"}}
	if err := input.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNarrativeInputValidateRejectsMissingImmutableField(t *testing.T) {
	input := NarrativeInput{AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"amountCents"}}
	if err := input.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAuthorityCanonicalizesAnalysisAndImmutableFields(t *testing.T) {
	analysis := validLossAuthorityAnalysis()
	analysis.ConditionalHighCents = "9007199254740993"
	payload, _ := json.Marshal(analysis)
	value := Authority{
		Kind: AuthorityLossAssessment, ID: "loss-1", Version: "loss-v1", SchemaVersion: AuthoritySchemaLossV1,
		AnalysisJSON: payload, ResolvedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
	}
	canonical, err := value.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical.AnalysisJSON) != string(payload) || !strings.Contains(string(payload), `"9007199254740993"`) {
		t.Fatalf("canonical analysis = %s", canonical.AnalysisJSON)
	}
	if strings.Join(canonical.ImmutableFields, ",") != strings.Join(lossFields, ",") {
		t.Fatalf("canonical immutableFields = %v", canonical.ImmutableFields)
	}
}

func TestAuthorityRejectsPIIInternalFieldsAndIDAliases(t *testing.T) {
	for _, field := range []string{"name", "phone", "detailedAddress", "internalCoefficient", "id"} {
		t.Run(field, func(t *testing.T) {
			value := validLossAuthorityValue()
			var object map[string]any
			if err := json.Unmarshal(value.AnalysisJSON, &object); err != nil {
				t.Fatal(err)
			}
			object[field] = "unsafe"
			value.AnalysisJSON, _ = json.Marshal(object)
			if _, err := value.Canonical(); !errors.Is(err, ErrUnsafeStoredAnalysis) {
				t.Fatalf("Canonical() error = %v", err)
			}
		})
	}
}

func TestLossAuthorityRejectsNonCanonicalOrReversedDecimalAmounts(t *testing.T) {
	for _, values := range [][3]string{{"01", "2", "3"}, {"3", "2", "4"}, {"1", "2.5", "3"}} {
		value := validLossAuthorityValue()
		analysis := validLossAuthorityAnalysis()
		analysis.ConditionalLowCents, analysis.ConditionalCentralCents, analysis.ConditionalHighCents = values[0], values[1], values[2]
		value.AnalysisJSON, _ = json.Marshal(analysis)
		if _, err := value.Canonical(); !errors.Is(err, ErrInvalidAuthority) {
			t.Fatalf("amounts=%v error=%v", values, err)
		}
	}
}

func TestHazardAuthorityRejectsInventedConfidenceLevel(t *testing.T) {
	analysis := HazardAuthorityAnalysis{
		AffectedAreaSquareMeters: 100, ConfidenceLevel: "0.82", DataStatus: "fresh",
		HazardType: "landslide", RiskLevel: "high", RiskZoneCount: 2,
		RuleVersion: "risk-v1", SnapshotID: "snapshot-1",
	}
	payload, _ := json.Marshal(analysis)
	value := Authority{
		Kind: AuthorityHazardSnapshot, ID: "snapshot-1", Version: "risk-v1",
		SchemaVersion: AuthoritySchemaHazardV1, AnalysisJSON: payload,
		ResolvedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
	}
	if _, err := value.Canonical(); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("Canonical() error = %v", err)
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
	value = narrative
	value.Model = strings.Repeat("模", maxNarrativeModel+1)
	if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("超长模型名未被拒绝: %v", err)
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

func validLossAuthorityValue() Authority {
	payload, _ := json.Marshal(validLossAuthorityAnalysis())
	return Authority{
		Kind: AuthorityLossAssessment, ID: "loss-1", Version: "loss-v1", SchemaVersion: AuthoritySchemaLossV1,
		AnalysisJSON: payload, ResolvedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
	}
}

func validLossAuthorityAnalysis() LossAuthorityAnalysis {
	return LossAuthorityAnalysis{
		AffectedPopulation: 10, AssessmentID: "loss-1", ConditionalCentralCents: "2000",
		ConditionalHighCents: "3000", ConditionalLowCents: "1000", Confidence: 0.8,
		ConfidenceBand: "high", FormulaVersion: "loss-v1", ImpactAreaSquareMeters: 100,
		SnapshotID: "snapshot-1", Status: "available",
	}
}
