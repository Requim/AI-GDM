package survival

import (
	"context"
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
)

// AssessmentService 是确定性生还评估驱动适配器使用的最小端口。
type AssessmentService interface {
	Assess(context.Context, string) (survivaldomain.Assessment, error)
}

// AssessmentServiceImpl 读取匿名场景并调用领域规则。
type AssessmentServiceImpl struct {
	scenarios ports.RescueScenarioReader
	clock     ports.Clock
}

var _ AssessmentService = (*AssessmentServiceImpl)(nil)

// NewAssessmentService 创建生还评估用例。
func NewAssessmentService(scenarios ports.RescueScenarioReader, clock ports.Clock) (*AssessmentServiceImpl, error) {
	if scenarios == nil || clock == nil {
		return nil, fmt.Errorf("%w: 生还评估依赖为空", domain.ErrInvalidInput)
	}
	return &AssessmentServiceImpl{scenarios: scenarios, clock: clock}, nil
}

// Assess 对指定合成匿名场景进行确定性回放。
func (s *AssessmentServiceImpl) Assess(ctx context.Context, scenarioID string) (survivaldomain.Assessment, error) {
	if err := validateID(scenarioID); err != nil {
		return survivaldomain.Assessment{}, err
	}
	scenario, err := s.scenarios.GetScenario(ctx, scenarioID)
	if err != nil {
		return survivaldomain.Assessment{}, fmt.Errorf("读取回放场景 %s: %w", scenarioID, err)
	}
	now := s.clock.Now()
	if now.IsZero() {
		return survivaldomain.Assessment{}, fmt.Errorf("%w: 生还评估时钟为空", domain.ErrInvalidInput)
	}
	assessment, err := survivaldomain.Evaluate(scenario, now)
	if err != nil {
		return survivaldomain.Assessment{}, fmt.Errorf("评估回放场景 %s: %w", scenarioID, err)
	}
	return assessment, nil
}
