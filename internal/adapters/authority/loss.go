package authority

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func (r *Resolver) resolveLoss(ctx context.Context, id string) (report.Authority, error) {
	assessment, err := r.losses.GetAssessment(ctx, id)
	if err != nil {
		return report.Authority{}, fmt.Errorf("读取损失评估 %s: %w", id, err)
	}
	if err = assessment.Validate(); err != nil {
		return report.Authority{}, unsafeStored("损失评估记录损坏", err)
	}
	if assessment.ID != id || assessment.FormulaVersion != loss.FormulaVersion {
		return report.Authority{}, invalidBinding("损失评估标识或公式版本错配", domain.ErrInvalidInput)
	}
	if !validLossAuthorityEnums(assessment.Status, assessment.ConfidenceBand) {
		return report.Authority{}, unsafeStored("损失评估状态或置信度枚举损坏", domain.ErrInvalidInput)
	}
	dto := report.LossAuthorityAnalysis{
		AffectedPopulation: assessment.AffectedPopulation, AssessmentID: assessment.ID,
		ConditionalCentralCents: strconv.FormatInt(assessment.ConditionalMidCents, 10),
		ConditionalHighCents:    strconv.FormatInt(assessment.ConditionalHighCents, 10),
		ConditionalLowCents:     strconv.FormatInt(assessment.ConditionalLowCents, 10),
		Confidence:              assessment.Confidence, ConfidenceBand: assessment.ConfidenceBand,
		FormulaVersion: assessment.FormulaVersion, ImpactAreaSquareMeters: assessment.ImpactAreaSquareM,
		SnapshotID: assessment.SnapshotID, Status: string(assessment.Status),
	}
	return r.newAuthority(report.AuthorityLossAssessment, id, loss.FormulaVersion,
		report.AuthoritySchemaLossV1, dto)
}

func validLossAuthorityEnums(status loss.AssessmentStatus, confidenceBand string) bool {
	if status != loss.AssessmentAvailable && status != loss.AssessmentInsufficientData {
		return false
	}
	switch confidenceBand {
	case "high", "moderate", "low", "very_low":
		return true
	default:
		return false
	}
}
