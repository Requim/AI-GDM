package survival

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
)

// ReplayAssessment 是案例级历史回放的完整安全响应。
type ReplayAssessment struct {
	AssessmentID   string                     `json:"assessmentId"`
	CaseID         string                     `json:"caseId"`
	ScenarioID     string                     `json:"scenarioId"`
	ScenarioDigest string                     `json:"scenarioDigest"`
	Usage          survivaldomain.ReplayUsage `json:"usage"`
	Assessment     survivaldomain.Assessment  `json:"assessment"`
}

// Validate 校验案例、场景摘要、用途声明和评估结果的一致性。
func (r ReplayAssessment) Validate() error {
	if err := ValidateIdentifier(r.CaseID); err != nil {
		return err
	}
	if err := ValidateIdentifier(r.ScenarioID); err != nil {
		return err
	}
	if r.Assessment.ScenarioID != r.ScenarioID {
		return fmt.Errorf("%w: 回放场景与评估结果不一致", domain.ErrInvalidInput)
	}
	if !validScenarioDigest(r.ScenarioDigest) {
		return fmt.Errorf("%w: 回放场景摘要无效", domain.ErrInvalidInput)
	}
	if r.Usage != survivaldomain.HistoricalReplayUsage() {
		return fmt.Errorf("%w: 历史回放用途声明无效", domain.ErrInvalidInput)
	}
	if err := r.Assessment.Validate(); err != nil {
		return fmt.Errorf("校验历史回放评估: %w", err)
	}
	expectedID, err := replayAssessmentID(r.CaseID, r.ScenarioDigest, r.Assessment)
	if err != nil || r.AssessmentID != expectedID {
		return fmt.Errorf("%w: 历史回放评估标识不一致", domain.ErrInvalidInput)
	}
	return nil
}

// NewReplayAssessment 创建并校验稳定标识的案例级回放结果。
func NewReplayAssessment(caseID, scenarioDigest string,
	assessment survivaldomain.Assessment,
) (ReplayAssessment, error) {
	assessmentID, err := replayAssessmentID(caseID, scenarioDigest, assessment)
	if err != nil {
		return ReplayAssessment{}, err
	}
	result := ReplayAssessment{
		AssessmentID: assessmentID, CaseID: caseID, ScenarioID: assessment.ScenarioID,
		ScenarioDigest: scenarioDigest, Usage: survivaldomain.HistoricalReplayUsage(), Assessment: assessment,
	}
	if err = result.Validate(); err != nil {
		return ReplayAssessment{}, err
	}
	return result, nil
}

// AssessmentService 是案例级确定性历史回放驱动适配器使用的最小端口。
type AssessmentService interface {
	AssessCase(context.Context, string) (ReplayAssessment, error)
}

// AssessmentServiceImpl 读取案例绑定的合成场景并调用领域规则。
type AssessmentServiceImpl struct {
	scenarios CaseScenarioReader
	clock     ports.Clock
}

var _ AssessmentService = (*AssessmentServiceImpl)(nil)

// NewAssessmentService 创建案例级历史回放评估用例。
func NewAssessmentService(scenarios CaseScenarioReader, clock ports.Clock) (*AssessmentServiceImpl, error) {
	if scenarios == nil || clock == nil {
		return nil, fmt.Errorf("%w: 生还评估依赖为空", domain.ErrInvalidInput)
	}
	return &AssessmentServiceImpl{scenarios: scenarios, clock: clock}, nil
}

// AssessCase 对指定公开历史案例的唯一合成匿名场景进行确定性回放。
func (s *AssessmentServiceImpl) AssessCase(ctx context.Context, caseID string) (ReplayAssessment, error) {
	if err := ValidateIdentifier(caseID); err != nil {
		return ReplayAssessment{}, err
	}
	scenario, err := s.scenarios.ScenarioForEvent(ctx, caseID)
	if err != nil {
		return ReplayAssessment{}, fmt.Errorf("读取案例 %s 回放场景: %w", caseID, err)
	}
	if err = validateScenarioBinding(caseID, scenario); err != nil {
		return ReplayAssessment{}, err
	}
	digest, err := scenario.Digest()
	if err != nil {
		return ReplayAssessment{}, fmt.Errorf("生成案例 %s 场景摘要: %w", caseID, err)
	}
	assessment, err := survivaldomain.Evaluate(scenario, s.clock.Now())
	if err != nil {
		return ReplayAssessment{}, fmt.Errorf("评估案例 %s 回放场景: %w", caseID, err)
	}
	return NewReplayAssessment(caseID, digest, assessment)
}

type replayAssessmentIdentity struct {
	CaseID            string                         `json:"caseId"`
	ScenarioID        string                         `json:"scenarioId"`
	ScenarioDigest    string                         `json:"scenarioDigest"`
	ModelVersion      string                         `json:"modelVersion"`
	Score             int                            `json:"score"`
	ScoreBand         string                         `json:"scoreBand"`
	ProbabilityLow    float64                        `json:"probabilityLow"`
	ProbabilityHigh   float64                        `json:"probabilityHigh"`
	ProbabilityBand   survivaldomain.ProbabilityBand `json:"probabilityBand"`
	Priority          survivaldomain.Priority        `json:"priority"`
	Factors           []string                       `json:"factors"`
	HumanReviewStatus string                         `json:"humanReviewStatus"`
	Limitations       []string                       `json:"limitations"`
}

func replayAssessmentID(caseID, scenarioDigest string, assessment survivaldomain.Assessment) (string, error) {
	identity := replayAssessmentIdentity{
		CaseID: caseID, ScenarioID: assessment.ScenarioID, ScenarioDigest: scenarioDigest,
		ModelVersion: assessment.ModelVersion, Score: assessment.Score, ScoreBand: assessment.ScoreBand,
		ProbabilityLow: assessment.ProbabilityLow, ProbabilityHigh: assessment.ProbabilityHigh,
		ProbabilityBand: assessment.ProbabilityBand, Priority: assessment.Priority,
		Factors: assessment.Factors, HumanReviewStatus: assessment.HumanReviewStatus,
		Limitations: assessment.Limitations,
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("编码历史回放评估标识: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validScenarioDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}
