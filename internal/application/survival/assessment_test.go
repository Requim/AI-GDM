package survival

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestAssessmentServiceEvaluatesCaseReplay(t *testing.T) {
	const caseID = "case-1"
	source := catalogReaderStub{scenarios: map[string]survivaldomain.Scenario{
		caseID: validScenarioForCase(caseID),
	}}
	service, err := NewAssessmentService(source, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.AssessCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	if value.CaseID != caseID || value.ScenarioID != "replay-case-1" || value.ScenarioDigest == "" ||
		value.AssessmentID == "" || value.Assessment.ModelVersion != survivaldomain.ModelVersion ||
		value.Assessment.HumanReviewStatus != "required" {
		t.Fatalf("assessment = %+v", value)
	}
	if value.Usage.LiveUseAllowed || value.Usage.Mode != survivaldomain.ReplayModeHistorical {
		t.Fatalf("usage=%+v", value.Usage)
	}
	if err = value.Validate(); err != nil {
		t.Fatalf("response validation=%v", err)
	}
	if _, err = service.AssessCase(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing case error = %v", err)
	}
}

func TestReplayAssessmentIDIsStableAcrossCalculationTimeAndChangesWithInputs(t *testing.T) {
	const caseID = "case-1"
	source := catalogReaderStub{scenarios: map[string]survivaldomain.Scenario{
		caseID: validScenarioForCase(caseID),
	}}
	firstService, err := NewAssessmentService(source, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := NewAssessmentService(source, alternateClock{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstService.AssessCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondService.AssessCase(context.Background(), caseID)
	if err != nil || first.AssessmentID != second.AssessmentID ||
		first.Assessment.CalculatedAt.Equal(second.Assessment.CalculatedAt) {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	assertAssessmentIdentityChanges(t, first, source.scenarios[caseID])
}

func TestReplayAssessmentRejectsWrongReviewAndCrossBinding(t *testing.T) {
	const caseID = "case-1"
	service, err := NewAssessmentService(catalogReaderStub{scenarios: map[string]survivaldomain.Scenario{
		caseID: validScenarioForCase("case-other"),
	}}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AssessCase(context.Background(), caseID); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("cross binding error=%v", err)
	}
	validService, err := NewAssessmentService(catalogReaderStub{scenarios: map[string]survivaldomain.Scenario{
		caseID: validScenarioForCase(caseID),
	}}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := validService.AssessCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	value.Assessment.HumanReviewStatus = "approved"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("ReplayAssessment.Validate() 未拒绝非 required 评估")
	}
	value, err = validService.AssessCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	value.ScenarioDigest = "sha256:" + "00"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("ReplayAssessment.Validate() 未拒绝错误场景摘要")
	}
	value, err = validService.AssessCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	value.AssessmentID = "sha256:" + "00"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("ReplayAssessment.Validate() 未拒绝错误评估标识")
	}
}

func assertAssessmentIdentityChanges(t *testing.T, value ReplayAssessment, scenario survivaldomain.Scenario) {
	t.Helper()
	modelChanged := value.Assessment
	modelChanged.ModelVersion += "-next"
	modelID, err := replayAssessmentID(value.CaseID, value.ScenarioDigest, modelChanged)
	if err != nil || modelID == value.AssessmentID {
		t.Fatalf("model identity=%q original=%q err=%v", modelID, value.AssessmentID, err)
	}
	scenario.ElapsedMinutes++
	digest, err := scenario.Digest()
	if err != nil {
		t.Fatal(err)
	}
	scenarioID, err := replayAssessmentID(value.CaseID, digest, value.Assessment)
	if err != nil || scenarioID == value.AssessmentID {
		t.Fatalf("scenario identity=%q original=%q err=%v", scenarioID, value.AssessmentID, err)
	}
}

func TestNewAssessmentServiceRejectsMissingDependencies(t *testing.T) {
	if _, err := NewAssessmentService(nil, fixedClock{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil scenario reader error = %v", err)
	}
	if _, err := NewAssessmentService(catalogReaderStub{}, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil clock error = %v", err)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC) }

type alternateClock struct{}

func (alternateClock) Now() time.Time { return time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) }
