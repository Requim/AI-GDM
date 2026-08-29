package survival

import (
	"fmt"
	"math"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// HistoricalEvent 保存不含可识别个人信息的事件级历史记录。
type HistoricalEvent struct {
	ID               string        `json:"id"`
	DatasetEventID   string        `json:"datasetEventId"`
	EventDate        time.Time     `json:"eventDate"`
	TimePrecision    string        `json:"timePrecision"`
	Category         string        `json:"category"`
	Trigger          string        `json:"trigger"`
	Size             string        `json:"size,omitempty"`
	Location         spatial.Point `json:"location"`
	LocationAccuracy string        `json:"locationAccuracy"`
	Country          string        `json:"country"`
	AdminArea        string        `json:"adminArea"`
	// nil 表示公开来源未按统一口径披露，不能用零值代替未知。
	Fatalities  *int                  `json:"fatalities"`
	Injuries    *int                  `json:"injuries"`
	Source      provenance.Provenance `json:"source"`
	Limitations []string              `json:"limitations"`
}

// Priority 表示人工确认前的辅助搜救优先级。
type Priority string

const (
	PriorityRoutine   Priority = "routine"
	PriorityElevated  Priority = "elevated"
	PriorityUrgent    Priority = "urgent"
	PriorityImmediate Priority = "immediate"
)

// SignalValue 表示可审计的三态环境或受困信号。
type SignalValue string

const (
	SignalUnknown SignalValue = "unknown"
	SignalYes     SignalValue = "yes"
	SignalNo      SignalValue = "no"
)

// InjurySeverity 表示回放场景中的合成伤情等级。
type InjurySeverity string

const (
	InjuryUnknown  InjurySeverity = "unknown"
	InjuryNone     InjurySeverity = "none"
	InjuryMinor    InjurySeverity = "minor"
	InjurySevere   InjurySeverity = "severe"
	InjuryCritical InjurySeverity = "critical"
)

// EnvironmentSignals 保存固定键集合的环境信号。
type EnvironmentSignals struct {
	AirPocket      SignalValue `json:"airPocket"`
	WaterAvailable SignalValue `json:"waterAvailable"`
	HazardStable   SignalValue `json:"hazardStable"`
}

// EntrapmentSignals 保存固定键集合的受困状态信号。
type EntrapmentSignals struct {
	Communication SignalValue    `json:"communication"`
	Injury        InjurySeverity `json:"injury"`
}

// Scenario 保存匿名化搜救算法输入；首版只允许合成测试场景。
type Scenario struct {
	ID                string             `json:"id"`
	CaseID            string             `json:"caseId"`
	AsOf              time.Time          `json:"asOf"`
	ElapsedMinutes    int64              `json:"elapsedMinutes"`
	Environment       EnvironmentSignals `json:"environment"`
	Entrapment        EntrapmentSignals  `json:"entrapment"`
	InputCompleteness float64            `json:"inputCompleteness"`
	Synthetic         bool               `json:"synthetic"`
}

// Assessment 保存搜救优先级和可解释因素，不代表经临床验证的个体生还概率。
type Assessment struct {
	ScenarioID        string          `json:"scenarioId"`
	Score             int             `json:"score"`
	ScoreBand         string          `json:"scoreBand"`
	ProbabilityLow    float64         `json:"probabilityLow"`
	ProbabilityHigh   float64         `json:"probabilityHigh"`
	ProbabilityBand   ProbabilityBand `json:"probabilityBand"`
	Priority          Priority        `json:"priority"`
	Factors           []string        `json:"factors"`
	ModelVersion      string          `json:"modelVersion"`
	HumanReviewStatus string          `json:"humanReviewStatus"`
	CalculatedAt      time.Time       `json:"calculatedAt"`
	Limitations       []string        `json:"limitations"`
}

// Validate 校验首版搜救场景不冒充真实人员记录。
func (s Scenario) Validate() error {
	if s.ID == "" || s.CaseID == "" || s.AsOf.IsZero() || !isUTC(s.AsOf) || s.ElapsedMinutes < 0 {
		return fmt.Errorf("%w: 搜救场景基础字段无效", domain.ErrInvalidInput)
	}
	if err := validateRequiredText("搜救场景标识", s.ID, maxIdentifierBytes); err != nil {
		return err
	}
	if err := validateRequiredText("搜救场景案例标识", s.CaseID, maxIdentifierBytes); err != nil {
		return err
	}
	if err := s.Environment.validate(); err != nil {
		return err
	}
	if err := s.Entrapment.validate(); err != nil {
		return err
	}
	if math.IsNaN(s.InputCompleteness) || math.IsInf(s.InputCompleteness, 0) ||
		s.InputCompleteness < 0 || s.InputCompleteness > 1 {
		return fmt.Errorf("%w: 输入完整度必须是零到一的有限数", domain.ErrInvalidInput)
	}
	if math.Abs(s.InputCompleteness-s.KnownFieldCoverage()) > 1e-9 {
		return fmt.Errorf("%w: 输入完整度与已知字段覆盖率不一致", domain.ErrInvalidInput)
	}
	if !s.Synthetic {
		return fmt.Errorf("%w: MVP 只允许合成匿名搜救场景", domain.ErrInvalidInput)
	}
	return nil
}

// KnownFieldCoverage 返回五个固定信号中非 unknown 字段的占比。
func (s Scenario) KnownFieldCoverage() float64 {
	known := 0
	for _, value := range []SignalValue{
		s.Environment.AirPocket, s.Environment.WaterAvailable,
		s.Environment.HazardStable, s.Entrapment.Communication,
	} {
		if value.known() {
			known++
		}
	}
	if s.Entrapment.Injury.known() {
		known++
	}
	return float64(known) / 5
}

func (s EnvironmentSignals) validate() error {
	for _, value := range []SignalValue{s.AirPocket, s.WaterAvailable, s.HazardStable} {
		if !value.valid() {
			return fmt.Errorf("%w: 环境信号值无效", domain.ErrInvalidInput)
		}
	}
	return nil
}

func (s EntrapmentSignals) validate() error {
	if !s.Communication.valid() || !s.Injury.valid() {
		return fmt.Errorf("%w: 受困状态信号值无效", domain.ErrInvalidInput)
	}
	return nil
}

func (s SignalValue) valid() bool {
	return s == SignalUnknown || s == SignalYes || s == SignalNo
}

func (s SignalValue) known() bool { return s == SignalYes || s == SignalNo }

func (s InjurySeverity) valid() bool {
	switch s {
	case InjuryUnknown, InjuryNone, InjuryMinor, InjurySevere, InjuryCritical:
		return true
	default:
		return false
	}
}

func (s InjurySeverity) known() bool { return s.valid() && s != InjuryUnknown }

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
