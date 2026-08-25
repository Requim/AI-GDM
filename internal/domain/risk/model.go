package risk

import (
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const (
	// RuleVersion 固定首版确定性研判规则，修改规则必须提升版本。
	RuleVersion = "ai-gdm-risk-rules-v1"
	// ConfidenceSemantics 明确置信度只描述输入质量，不是灾害概率。
	ConfidenceSemantics = "主模型输入的数据质量等级，不是灾害发生概率或模型校准置信区间"
)

// DataStatus 表示主模型数据在评估时刻的可用状态。
type DataStatus string

const (
	DataCurrent  DataStatus = "current"
	DataFallback DataStatus = "fallback"
	DataExpired  DataStatus = "expired"
)

// ContextStatus 表示调用方已完成空间选择的气象上下文状态。
type ContextStatus string

const (
	ContextAbsent      ContextStatus = "absent"
	ContextCurrent     ContextStatus = "current"
	ContextFallback    ContextStatus = "fallback"
	ContextPartial     ContextStatus = "partial"
	ContextUnavailable ContextStatus = "unavailable"
)

// AssessmentStatus 表示研判是否可以提供可操作等级。
type AssessmentStatus string

const (
	AssessmentAvailable        AssessmentStatus = "available"
	AssessmentDegraded         AssessmentStatus = "degraded"
	AssessmentInsufficientData AssessmentStatus = "insufficient_data"
)

// ConfidenceLevel 是主模型输入质量的有序等级。
type ConfidenceLevel string

const (
	ConfidenceHigh        ConfidenceLevel = "high"
	ConfidenceMedium      ConfidenceLevel = "medium"
	ConfidenceLow         ConfidenceLevel = "low"
	ConfidenceUnavailable ConfidenceLevel = "unavailable"
)

// FactorRole 区分决定等级的依据和不参与加权的上下文。
type FactorRole string

const (
	FactorDecisionBasis FactorRole = "decision_basis"
	FactorContextOnly   FactorRole = "context_only"
	FactorDataQuality   FactorRole = "data_quality"
)

// Metric 保存触发因素中的原始、未归一化数值。
type Metric struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Factor 保存某项决定依据、上下文证据或质量限制。
type Factor struct {
	Code            string     `json:"code"`
	Role            FactorRole `json:"role"`
	AffectsLevel    bool       `json:"affectsLevel"`
	DataStatus      DataStatus `json:"dataStatus"`
	ContextID       string     `json:"contextId,omitempty"`
	Description     string     `json:"description"`
	Metrics         []Metric   `json:"metrics,omitempty"`
	WindowStart     time.Time  `json:"windowStart,omitempty,omitzero"`
	WindowEnd       time.Time  `json:"windowEnd,omitempty,omitzero"`
	InputReferences []string   `json:"inputReferences"`
}

// Confidence 保存主模型输入质量等级及其可解释依据。
type Confidence struct {
	Level     ConfidenceLevel `json:"level"`
	Scope     string          `json:"scope"`
	Semantics string          `json:"semantics"`
	Reasons   []string        `json:"reasons"`
}

// Decision 保存确定性风险等级及其聚合依据。
type Decision struct {
	Level          hazard.RiskLevel `json:"level"`
	ZoneCount      int              `json:"zoneCount"`
	HighestZoneIDs []string         `json:"highestZoneIds"`
	Basis          string           `json:"basis"`
}

// WeatherContext 是已由调用方完成空间选择的气象时间序列。
type WeatherContext struct {
	Snapshot        hazard.WeatherSnapshot
	SelectionMethod string
}

// ModelCapability 将一个风险引擎实例绑定到专用灾种、模型和数据集。
type ModelCapability struct {
	HazardType hazard.Type
	ModelName  string
	Dataset    string
}

// Input 保存一次纯领域风险研判所需的全部显式输入。
type Input struct {
	Snapshot        hazard.Snapshot
	Zones           []hazard.RiskZone
	WeatherContexts []WeatherContext
	EvaluatedAt     time.Time
}

// Assessment 保存可审计、可重放的确定性风险研判结果。
type Assessment struct {
	ID              string           `json:"id"`
	HazardType      hazard.Type      `json:"hazardType"`
	SnapshotID      string           `json:"snapshotId"`
	Decision        *Decision        `json:"decision,omitempty"`
	Status          AssessmentStatus `json:"status"`
	DataStatus      DataStatus       `json:"dataStatus"`
	ContextStatus   ContextStatus    `json:"contextStatus"`
	Confidence      Confidence       `json:"confidence"`
	Factors         []Factor         `json:"triggerFactors"`
	RuleVersion     string           `json:"ruleVersion"`
	EvaluatedAt     time.Time        `json:"evaluatedAt"`
	InputReferences []string         `json:"inputReferences"`
	Limitations     []string         `json:"limitations"`
}
