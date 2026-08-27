package survival

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

// ProbabilityBand 表示辅助评估的宽区间，不代表经过临床校准的个体概率。
type ProbabilityBand string

const (
	ProbabilityVeryLow  ProbabilityBand = "very_low"
	ProbabilityLow      ProbabilityBand = "low"
	ProbabilityModerate ProbabilityBand = "moderate"
	ProbabilityHigh     ProbabilityBand = "high"
)

// ModelVersion 是首版确定性规则的可回放版本号。
const ModelVersion = "ai-gdm-survival-rules-v1"

// Validate 校验辅助评估结果在概率和版本上的边界。
func (a Assessment) Validate() error {
	if strings.TrimSpace(a.ScenarioID) == "" || a.ModelVersion == "" ||
		a.ModelVersion != ModelVersion || a.CalculatedAt.IsZero() || !isUTC(a.CalculatedAt) {
		return fmt.Errorf("%w: 生还评估标识、版本或时间无效", domain.ErrInvalidInput)
	}
	if a.Score < 0 || a.Score > 100 || a.ProbabilityLow < 0 || a.ProbabilityHigh > 1 ||
		a.ProbabilityLow > a.ProbabilityHigh || !finite(a.ProbabilityLow) || !finite(a.ProbabilityHigh) {
		return fmt.Errorf("%w: 生还评估分数或概率区间无效", domain.ErrInvalidInput)
	}
	if a.ScoreBand != scoreBand(a.Score) {
		return fmt.Errorf("%w: 生还评估分数等级不匹配", domain.ErrInvalidInput)
	}
	_, _, expectedBand := probabilityRange(a.Score)
	if a.ProbabilityBand != expectedBand {
		return fmt.Errorf("%w: 生还评估概率等级不匹配", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(a.HumanReviewStatus) == "" || len(a.Factors) == 0 {
		return fmt.Errorf("%w: 生还评估缺少人工复核或解释因素", domain.ErrInvalidInput)
	}
	switch a.Priority {
	case PriorityRoutine, PriorityElevated, PriorityUrgent, PriorityImmediate:
	default:
		return fmt.Errorf("%w: 生还评估优先级无效", domain.ErrInvalidInput)
	}
	switch a.ProbabilityBand {
	case ProbabilityVeryLow, ProbabilityLow, ProbabilityModerate, ProbabilityHigh:
	default:
		return fmt.Errorf("%w: 生还评估概率等级无效", domain.ErrInvalidInput)
	}
	return nil
}

// Evaluate 使用固定规则生成可解释的匿名场景评估。
func Evaluate(s Scenario, calculatedAt time.Time) (Assessment, error) {
	if err := s.Validate(); err != nil {
		return Assessment{}, err
	}
	if calculatedAt.IsZero() || !isUTC(calculatedAt) {
		return Assessment{}, fmt.Errorf("%w: 生还评估时间必须使用 UTC", domain.ErrInvalidInput)
	}
	score := 50
	factors := make([]string, 0, 6)
	applyElapsed(&score, &factors, s.ElapsedMinutes)
	applyCompleteness(&score, &factors, s.InputCompleteness)
	applySignals(&score, &factors, s.Environment, s.Entrapment)
	score = clampScore(score)
	low, high, band := probabilityRange(score)
	assessment := Assessment{
		ScenarioID: s.ID, Score: score, ScoreBand: scoreBand(score),
		ProbabilityLow: low, ProbabilityHigh: high, ProbabilityBand: band,
		Priority: priorityFor(score, s.ElapsedMinutes), Factors: nonEmptyFactors(factors),
		ModelVersion: ModelVersion, HumanReviewStatus: "required", CalculatedAt: calculatedAt,
		Limitations: []string{
			"规则未经过个体层面的临床校准",
			"历史案例存在报告偏差，不能外推到具体个人",
			"风险变化、伤情和现场核验必须由专业人员确认",
		},
	}
	if err := assessment.Validate(); err != nil {
		return Assessment{}, err
	}
	return assessment, nil
}

func applyElapsed(score *int, factors *[]string, minutes int64) {
	switch {
	case minutes <= 60:
		*score += 20
		*factors = append(*factors, "失联时间在一小时内")
	case minutes <= 240:
		*score += 5
		*factors = append(*factors, "失联时间处于四小时内")
	case minutes <= 720:
		*score -= 15
		*factors = append(*factors, "失联时间超过四小时")
	default:
		*score -= 30
		*factors = append(*factors, "失联时间超过十二小时")
	}
}

func applyCompleteness(score *int, factors *[]string, completeness float64) {
	switch {
	case completeness >= 0.8:
		*factors = append(*factors, "搜救输入完整度较高")
	case completeness >= 0.5:
		*score -= 5
		*factors = append(*factors, "搜救输入仍有缺口")
	default:
		*score -= 15
		*factors = append(*factors, "搜救输入完整度较低")
	}
}

func applySignals(score *int, factors *[]string, environment, entrapment map[string]string) {
	if signal(environment, "air_pocket", "yes", "true") {
		*score += 15
		*factors = append(*factors, "记录有可呼吸空间")
	}
	if signal(environment, "water_available", "yes", "true") {
		*score += 8
		*factors = append(*factors, "记录有可用饮水")
	}
	if signal(environment, "hazard_stable", "yes", "true") {
		*score += 8
		*factors = append(*factors, "现场危险源暂时稳定")
	}
	if signal(environment, "hazard_stable", "no", "false") || signal(entrapment, "injury", "severe", "critical") {
		*score -= 20
		*factors = append(*factors, "存在持续危险或重伤信号")
	}
	if signal(entrapment, "communication", "yes", "true") {
		*score += 10
		*factors = append(*factors, "仍可建立通信")
	}
}

func signal(values map[string]string, key string, accepted ...string) bool {
	value := strings.ToLower(strings.TrimSpace(values[key]))
	for _, candidate := range accepted {
		if value == candidate {
			return true
		}
	}
	return false
}

func clampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func scoreBand(score int) string {
	switch {
	case score >= 75:
		return "high"
	case score >= 50:
		return "moderate"
	case score >= 25:
		return "low"
	default:
		return "very_low"
	}
}

func probabilityRange(score int) (float64, float64, ProbabilityBand) {
	switch {
	case score >= 75:
		return 0.60, 0.85, ProbabilityHigh
	case score >= 50:
		return 0.35, 0.59, ProbabilityModerate
	case score >= 25:
		return 0.15, 0.34, ProbabilityLow
	default:
		return 0.05, 0.14, ProbabilityVeryLow
	}
}

func priorityFor(score int, elapsed int64) Priority {
	switch {
	case score >= 75 || (elapsed <= 60 && score >= 55):
		return PriorityImmediate
	case score >= 50:
		return PriorityUrgent
	case score >= 25:
		return PriorityElevated
	default:
		return PriorityRoutine
	}
}

func nonEmptyFactors(values []string) []string {
	if len(values) == 0 {
		return []string{"缺少可解释输入因素"}
	}
	return values
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
