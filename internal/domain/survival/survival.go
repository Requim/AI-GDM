package survival

import (
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// HistoricalEvent 保存不含可识别个人信息的事件级历史记录。
type HistoricalEvent struct {
	ID               string                `json:"id"`
	DatasetEventID   string                `json:"datasetEventId"`
	EventDate        time.Time             `json:"eventDate"`
	TimePrecision    string                `json:"timePrecision"`
	Category         string                `json:"category"`
	Trigger          string                `json:"trigger"`
	Size             string                `json:"size,omitempty"`
	Location         spatial.Point         `json:"location"`
	LocationAccuracy string                `json:"locationAccuracy"`
	Country          string                `json:"country"`
	AdminArea        string                `json:"adminArea"`
	Fatalities       int                   `json:"fatalities"`
	Injuries         int                   `json:"injuries"`
	Source           provenance.Provenance `json:"source"`
	Limitations      []string              `json:"limitations"`
}

// Priority 表示人工确认前的辅助搜救优先级。
type Priority string

const (
	PriorityRoutine   Priority = "routine"
	PriorityElevated  Priority = "elevated"
	PriorityUrgent    Priority = "urgent"
	PriorityImmediate Priority = "immediate"
)

// Scenario 保存匿名化搜救算法输入；首版只允许合成测试场景。
type Scenario struct {
	ID                string            `json:"id"`
	AsOf              time.Time         `json:"asOf"`
	ElapsedMinutes    int64             `json:"elapsedMinutes"`
	Environment       map[string]string `json:"environment"`
	Entrapment        map[string]string `json:"entrapment"`
	InputCompleteness float64           `json:"inputCompleteness"`
	Synthetic         bool              `json:"synthetic"`
}

// Assessment 保存搜救优先级和可解释因素，不代表经临床验证的个体生还概率。
type Assessment struct {
	ScenarioID        string    `json:"scenarioId"`
	ScoreBand         string    `json:"scoreBand"`
	Priority          Priority  `json:"priority"`
	Factors           []string  `json:"factors"`
	ModelVersion      string    `json:"modelVersion"`
	HumanReviewStatus string    `json:"humanReviewStatus"`
	CalculatedAt      time.Time `json:"calculatedAt"`
	Limitations       []string  `json:"limitations"`
}

// Validate 校验首版搜救场景不冒充真实人员记录。
func (s Scenario) Validate() error {
	if s.ID == "" || s.AsOf.IsZero() || s.ElapsedMinutes < 0 {
		return fmt.Errorf("%w: 搜救场景基础字段无效", domain.ErrInvalidInput)
	}
	if s.InputCompleteness < 0 || s.InputCompleteness > 1 {
		return fmt.Errorf("%w: 输入完整度超出零到一", domain.ErrInvalidInput)
	}
	if !s.Synthetic {
		return fmt.Errorf("%w: MVP 只允许合成匿名搜救场景", domain.ErrInvalidInput)
	}
	return nil
}
