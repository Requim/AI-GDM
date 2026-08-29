package survival

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestAssessmentValidateRejectsMismatchedBandsAndMissingReview(t *testing.T) {
	value, err := Evaluate(validScenario("scenario-validation", "case-validation"),
		time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.ScoreBand = "low"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝不匹配的分数等级")
	}
	value, err = Evaluate(validScenario("scenario-review", "case-review"),
		time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.HumanReviewStatus = "approved"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝非 required 人工复核状态")
	}
}

func TestAssessmentValidateRejectsNonFiniteProbability(t *testing.T) {
	value, err := Evaluate(validScenario("scenario-finite", "case-finite"),
		time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.ProbabilityHigh = math.Inf(1)
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝非有限概率")
	}
}

func TestAssessmentValidateRejectsOversizedNarrativeFields(t *testing.T) {
	value, err := Evaluate(validScenario("scenario-bounds", "case-bounds"),
		time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.Factors[0] = strings.Repeat("x", maxLongTextBytes+1)
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Assessment.Validate() 未拒绝超长因素")
	}
	value, err = Evaluate(validScenario("scenario-items", "case-items"),
		time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.Limitations = make([]string, maxTextItems+1)
	for index := range value.Limitations {
		value.Limitations[index] = "限制"
	}
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("Assessment.Validate() 未拒绝超量限制")
	}
}

func TestEvaluateRejectsCalculatedAtBeforeScenario(t *testing.T) {
	scenario := validScenario("scenario-time", "case-time")
	if _, err := Evaluate(scenario, scenario.AsOf.Add(-time.Second)); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Evaluate() early calculatedAt error=%v", err)
	}
}
