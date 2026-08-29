package authority

import (
	"context"
	"fmt"
	"time"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

const (
	maxSurvivalAuthorityCases         = 100
	survivalAssessmentFutureTolerance = 5 * time.Minute
)

func (r *Resolver) resolveSurvival(ctx context.Context, id string) (report.Authority, error) {
	replay, err := r.findReplay(ctx, id)
	if err != nil {
		return report.Authority{}, err
	}
	if replay.Assessment.ModelVersion != survivaldomain.ModelVersion || replay.AssessmentID != id {
		return report.Authority{}, invalidBinding("生还回放标识或模型版本错配", domain.ErrInvalidInput)
	}
	if err = validateSurvivalEnums(replay.Assessment); err != nil {
		return report.Authority{}, unsafeStored("生还回放枚举损坏", err)
	}
	dto := report.SurvivalAuthorityAnalysis{
		AssessmentID: replay.AssessmentID, CaseID: replay.CaseID,
		Factors:           append([]string(nil), replay.Assessment.Factors...),
		HumanReviewStatus: replay.Assessment.HumanReviewStatus,
		Limitations:       append([]string(nil), replay.Assessment.Limitations...),
		ModelVersion:      replay.Assessment.ModelVersion, Priority: string(replay.Assessment.Priority),
		ProbabilityBand: string(replay.Assessment.ProbabilityBand),
		ProbabilityHigh: replay.Assessment.ProbabilityHigh, ProbabilityLow: replay.Assessment.ProbabilityLow,
		ScenarioDigest: replay.ScenarioDigest, ScenarioID: replay.ScenarioID,
		Score: replay.Assessment.Score, ScoreBand: replay.Assessment.ScoreBand, Usage: replay.Usage,
	}
	return r.newAuthority(report.AuthoritySurvivalAssessment, id, survivaldomain.ModelVersion,
		report.AuthoritySchemaSurvivalV1, dto)
}

func (r *Resolver) findReplay(ctx context.Context, id string) (applicationsurvival.ReplayAssessment, error) {
	if err := ctx.Err(); err != nil {
		return applicationsurvival.ReplayAssessment{}, err
	}
	r.survivalMu.RLock()
	replay, found := r.survivalIndex[id]
	ready := r.survivalReady
	r.survivalMu.RUnlock()
	if ready {
		return replayOrNotFound(id, replay, found)
	}
	return r.buildAndFindReplay(ctx, id)
}

func (r *Resolver) buildAndFindReplay(ctx context.Context,
	id string,
) (applicationsurvival.ReplayAssessment, error) {
	r.survivalMu.Lock()
	defer r.survivalMu.Unlock()
	if err := ctx.Err(); err != nil {
		return applicationsurvival.ReplayAssessment{}, err
	}
	if !r.survivalReady {
		index, err := r.buildSurvivalIndex(ctx)
		if err != nil {
			return applicationsurvival.ReplayAssessment{}, err
		}
		r.survivalIndex, r.survivalReady = index, true
	}
	replay, found := r.survivalIndex[id]
	return replayOrNotFound(id, replay, found)
}

func (r *Resolver) buildSurvivalIndex(ctx context.Context) (
	map[string]applicationsurvival.ReplayAssessment, error,
) {
	now, err := r.resolvedAt()
	if err != nil {
		return nil, err
	}
	cases, err := r.catalog.ListCases(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出生还历史案例: %w", err)
	}
	if len(cases) > maxSurvivalAuthorityCases {
		return nil, unsafeStored("历史案例目录超过权威索引上限", domain.ErrInsufficientData)
	}
	index := make(map[string]applicationsurvival.ReplayAssessment, len(cases))
	seenCases := make(map[string]struct{}, len(cases))
	seenScenarios := make(map[string]struct{}, len(cases))
	for _, summary := range cases {
		replay, entryErr := r.loadReplayEntry(ctx, summary, seenCases, seenScenarios, now)
		if entryErr != nil {
			return nil, entryErr
		}
		if _, exists := index[replay.AssessmentID]; exists {
			return nil, invalidBinding("历史回放目录包含重复评估标识", domain.ErrInvalidInput)
		}
		index[replay.AssessmentID] = replay
	}
	return index, nil
}

func (r *Resolver) loadReplayEntry(ctx context.Context, summary applicationsurvival.HistoricalCase,
	seenCases, seenScenarios map[string]struct{}, now time.Time,
) (applicationsurvival.ReplayAssessment, error) {
	if err := validateCaseSummary(summary, seenCases, seenScenarios); err != nil {
		return applicationsurvival.ReplayAssessment{}, err
	}
	detail, err := r.catalog.GetCase(ctx, summary.Event.ID)
	if err != nil {
		return applicationsurvival.ReplayAssessment{}, fmt.Errorf("读取回放案例 %s 详情: %w", summary.Event.ID, err)
	}
	replay, err := r.survival.AssessCase(ctx, summary.Event.ID)
	if err != nil {
		return applicationsurvival.ReplayAssessment{}, fmt.Errorf("重算历史回放 %s: %w", summary.Event.ID, err)
	}
	return validateReplayEntry(summary, detail, replay, now)
}

func validateCaseSummary(value applicationsurvival.HistoricalCase,
	seenCases, seenScenarios map[string]struct{},
) error {
	if err := value.Event.Validate(); err != nil {
		return unsafeStored("历史案例摘要损坏", err)
	}
	if err := applicationsurvival.ValidateIdentifier(value.Event.ID); err != nil {
		return unsafeStored("历史案例标识损坏", err)
	}
	if err := applicationsurvival.ValidateIdentifier(value.ScenarioID); err != nil {
		return unsafeStored("历史场景标识损坏", err)
	}
	if duplicateIdentifier(seenCases, value.Event.ID) || duplicateIdentifier(seenScenarios, value.ScenarioID) {
		return invalidBinding("历史案例目录包含重复案例或场景", domain.ErrInvalidInput)
	}
	return nil
}

func duplicateIdentifier(seen map[string]struct{}, id string) bool {
	if _, exists := seen[id]; exists {
		return true
	}
	seen[id] = struct{}{}
	return false
}

func validateReplayEntry(summary applicationsurvival.HistoricalCase,
	detail applicationsurvival.HistoricalCaseDetail, replay applicationsurvival.ReplayAssessment,
	now time.Time,
) (applicationsurvival.ReplayAssessment, error) {
	if err := detail.Validate(); err != nil {
		return applicationsurvival.ReplayAssessment{}, unsafeStored("历史案例详情损坏", err)
	}
	if err := replay.Validate(); err != nil {
		return applicationsurvival.ReplayAssessment{}, unsafeStored("生还回放结果损坏", err)
	}
	if detail.Event.ID != summary.Event.ID || detail.Event.ID != replay.CaseID ||
		detail.Scenario.ID != summary.ScenarioID || detail.Scenario.ID != replay.ScenarioID ||
		detail.ScenarioDigest != replay.ScenarioDigest || detail.Usage != replay.Usage {
		return applicationsurvival.ReplayAssessment{}, invalidBinding(
			"生还回放跨案例或场景绑定不一致", domain.ErrInvalidInput)
	}
	if replay.Usage != survivaldomain.HistoricalReplayUsage() {
		return applicationsurvival.ReplayAssessment{}, invalidBinding(
			"生还回放用途声明不是固定历史合成场景", domain.ErrInvalidInput)
	}
	if err := validateReplayTime(detail, replay, now); err != nil {
		return applicationsurvival.ReplayAssessment{}, err
	}
	return replay, nil
}

func validateReplayTime(detail applicationsurvival.HistoricalCaseDetail,
	replay applicationsurvival.ReplayAssessment, now time.Time,
) error {
	calculatedAt := replay.Assessment.CalculatedAt
	if calculatedAt.Before(detail.Scenario.AsOf) {
		return unsafeStored("生还回放评估时刻早于场景", domain.ErrInsufficientData)
	}
	if calculatedAt.After(now.Add(survivalAssessmentFutureTolerance)) {
		return unsafeStored("生还回放评估时刻超过当前时钟容差", domain.ErrInsufficientData)
	}
	return nil
}

func replayOrNotFound(id string, replay applicationsurvival.ReplayAssessment,
	found bool,
) (applicationsurvival.ReplayAssessment, error) {
	if !found {
		return applicationsurvival.ReplayAssessment{}, fmt.Errorf("%w: 生还评估 %s 不存在", domain.ErrNotFound, id)
	}
	return replay, nil
}

func validateSurvivalEnums(value survivaldomain.Assessment) error {
	if value.HumanReviewStatus != "required" || !validSurvivalScoreBand(value.ScoreBand) {
		return fmt.Errorf("%w: 生还评估人工复核或分数等级无效", domain.ErrInvalidInput)
	}
	switch value.Priority {
	case survivaldomain.PriorityRoutine, survivaldomain.PriorityElevated,
		survivaldomain.PriorityUrgent, survivaldomain.PriorityImmediate:
	default:
		return fmt.Errorf("%w: 生还评估优先级无效", domain.ErrInvalidInput)
	}
	switch value.ProbabilityBand {
	case survivaldomain.ProbabilityVeryLow, survivaldomain.ProbabilityLow,
		survivaldomain.ProbabilityModerate, survivaldomain.ProbabilityHigh:
		return nil
	default:
		return fmt.Errorf("%w: 生还评估概率等级无效", domain.ErrInvalidInput)
	}
}

func validSurvivalScoreBand(value string) bool {
	return value == "very_low" || value == "low" || value == "moderate" || value == "high"
}
