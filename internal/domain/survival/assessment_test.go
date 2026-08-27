package survival

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestAssessmentValidateRejectsMismatchedBandsAndMissingReview(t *testing.T) {
	value, err := Evaluate(Scenario{
		ID: "scenario-validation", AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
		InputCompleteness: 0.8, Synthetic: true,
	}, time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.ScoreBand = "low"
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝不匹配的分数等级")
	}
	value, err = Evaluate(Scenario{
		ID: "scenario-review", AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
		InputCompleteness: 0.8, Synthetic: true,
	}, time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.HumanReviewStatus = ""
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝缺少人工复核状态")
	}
}

func TestAssessmentValidateRejectsNonFiniteProbability(t *testing.T) {
	value, err := Evaluate(Scenario{
		ID: "scenario-finite", AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
		InputCompleteness: 0.8, Synthetic: true,
	}, time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	value.ProbabilityHigh = math.Inf(1)
	if !errors.Is(value.Validate(), domain.ErrInvalidInput) {
		t.Fatal("未拒绝非有限概率")
	}
}
