package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestGeneratePreservesAnalysisWhenProvidersSucceed(t *testing.T) {
	search := &searchStub{values: []report.Evidence{validEvidence()}}
	generator := &generatorStub{value: report.Narrative{
		Summary: "说明", GeneratedAt: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC),
		Model: "gpt-5.6-terra", Available: true, Source: validSource("llm"),
	}}
	service, err := New(search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Query: "四川 滑坡", AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel"}}
	result, err := service.Generate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.EvidenceAvailable || !result.NarrativeAvailable || result.AnalysisSHA256 == "" {
		t.Fatalf("结果 = %+v", result)
	}
	if string(result.AnalysisJSON) != string(input.AnalysisJSON) ||
		string(generator.input.AnalysisJSON) != string(input.AnalysisJSON) {
		t.Fatalf("权威分析被修改: result=%s input=%s", result.AnalysisJSON, generator.input.AnalysisJSON)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateDegradesOptionalProviderFailures(t *testing.T) {
	search := &searchStub{err: errors.New("搜索不可用")}
	generator := &generatorStub{err: errors.New("模型不可用")}
	service, err := New(search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), Input{
		Query: "滑坡", AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EvidenceAvailable || result.NarrativeAvailable || result.Narrative.Available {
		t.Fatalf("降级结果 = %+v", result)
	}
	if len(result.Limitations) != 2 {
		t.Fatalf("降级限制 = %v", result.Limitations)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateCapsExcessEvidence(t *testing.T) {
	values := make([]report.Evidence, maxEvidenceLimit+2)
	for index := range values {
		values[index] = validEvidence()
		values[index].URL = fmt.Sprintf("https://www.mnr.gov.cn/news/%d", index)
	}
	service, err := New(&searchStub{values: values}, nil, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), Input{
		Query: "滑坡", AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != maxEvidenceLimit || len(result.Limitations) != 2 {
		t.Fatalf("证据上限结果 = %d, limitations=%v", len(result.Evidence), result.Limitations)
	}
}

func TestGenerateTreatsContextCancellationAsError(t *testing.T) {
	service, err := New(&searchStub{err: context.DeadlineExceeded}, nil, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Generate(context.Background(), Input{
		Query: "滑坡", AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestGenerateRejectsInvalidInput(t *testing.T) {
	service, err := New(nil, nil, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Generate(context.Background(), Input{Query: "滑坡", AnalysisJSON: []byte("not-json")})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestResultValidateRejectsFlagMismatch(t *testing.T) {
	service, err := New(nil, nil, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), Input{
		Query: "滑坡", AnalysisJSON: []byte("{\"riskLevel\":\"high\"}"), ImmutableFields: []string{"riskLevel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result.EvidenceAvailable = true
	if err = result.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Validate() error = %v", err)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC) }

type searchStub struct {
	values []report.Evidence
	err    error
}

func (s *searchStub) Search(context.Context, string, int) ([]report.Evidence, error) {
	return s.values, s.err
}

type generatorStub struct {
	value report.Narrative
	err   error
	input report.NarrativeInput
}

func (s *generatorStub) Generate(_ context.Context, input report.NarrativeInput) (report.Narrative, error) {
	s.input = input
	return s.value, s.err
}

func validEvidence() report.Evidence {
	return report.Evidence{
		Title: "公开通报", URL: "https://www.mnr.gov.cn/news/1", Summary: "公开摘要",
		Source: validSource("bocha"),
	}
}

func validSource(provider string) provenance.Provenance {
	return provenance.Provenance{
		Provider: provider, Dataset: "test", SourceURI: "https://example.test/source",
		DataKind: provenance.DataKindObservation, FetchedAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC),
	}
}
