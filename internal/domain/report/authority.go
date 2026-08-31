package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	evacuationdomain "github.com/Requim/AI-GDM/internal/domain/evacuation"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	riskdomain "github.com/Requim/AI-GDM/internal/domain/risk"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

const (
	maxAuthorityToken           = 128
	maxAuthorityDisclaimerRunes = 256
	maxSurvivalAuthorityItems   = 32
	maxSurvivalAuthorityRunes   = 1024
)

var (
	// ErrInvalidAuthority 表示 resolver 返回的权威对象损坏或绑定不一致。
	ErrInvalidAuthority = errors.New("权威分析无效")
	// ErrUnsafeStoredAnalysis 表示存储分析含 schema 外字段，禁止发送给搜索或大模型。
	ErrUnsafeStoredAnalysis = errors.New("存储的权威分析不安全")
)

// AuthorityKind 标识可由服务端重新解析的确定性评估类型。
type AuthorityKind string

const (
	AuthorityHazardSnapshot     AuthorityKind = "hazard_snapshot"
	AuthorityEvacuationRoute    AuthorityKind = "evacuation_route"
	AuthorityLossAssessment     AuthorityKind = "loss_assessment"
	AuthoritySurvivalAssessment AuthorityKind = "survival_assessment"
)

const (
	AuthoritySchemaHazardV1   = "ai-gdm-authority-hazard-v1"
	AuthoritySchemaRouteV1    = "ai-gdm-authority-route-v1"
	AuthoritySchemaLossV1     = "ai-gdm-authority-loss-v1"
	AuthoritySchemaSurvivalV1 = "ai-gdm-authority-survival-v1"
)

// AnalysisReference 是浏览器允许提交的最小权威分析引用。
type AnalysisReference struct {
	Kind AuthorityKind `json:"kind"`
	ID   string        `json:"id"`
}

// Authority 保存由服务端确定性用例解析并按固定 schema 投影的分析结果。
type Authority struct {
	Kind            AuthorityKind   `json:"kind"`
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	SchemaVersion   string          `json:"schemaVersion"`
	AnalysisJSON    json.RawMessage `json:"analysis"`
	ImmutableFields []string        `json:"immutableFields"`
	ResolvedAt      time.Time       `json:"resolvedAt"`
}

// HazardAuthorityAnalysis 是风险快照允许发送给 LLM 的无个人信息投影。
type HazardAuthorityAnalysis struct {
	AffectedAreaSquareMeters float64 `json:"affectedAreaSquareMeters"`
	ConfidenceLevel          string  `json:"confidenceLevel"`
	DataStatus               string  `json:"dataStatus"`
	HazardType               string  `json:"hazardType"`
	RiskLevel                string  `json:"riskLevel"`
	RiskZoneCount            int     `json:"riskZoneCount"`
	RuleVersion              string  `json:"ruleVersion"`
	SnapshotID               string  `json:"snapshotId"`
}

// RouteAuthorityAnalysis 是候选路线允许发送给 LLM 的无地址投影。
type RouteAuthorityAnalysis struct {
	DistanceMeters     float64 `json:"distanceMeters"`
	DurationSeconds    int64   `json:"durationSeconds"`
	IntersectsRiskZone bool    `json:"intersectsRiskZone"`
	Mode               string  `json:"mode"`
	Rank               int     `json:"rank"`
	RiskScore          float64 `json:"riskScore"`
	RiskScoreAvailable bool    `json:"riskScoreAvailable"`
	RouteAnalysisID    string  `json:"routeAnalysisId"`
	RouteID            string  `json:"routeId"`
	RuleVersion        string  `json:"ruleVersion"`
	SnapshotID         string  `json:"snapshotId"`
}

// LossAuthorityAnalysis 是损失评估允许发送给 LLM 的最小数值投影。
type LossAuthorityAnalysis struct {
	AffectedPopulation      float64 `json:"affectedPopulation"`
	AssessmentID            string  `json:"assessmentId"`
	ConditionalCentralCents string  `json:"conditionalCentralCents"`
	ConditionalHighCents    string  `json:"conditionalHighCents"`
	ConditionalLowCents     string  `json:"conditionalLowCents"`
	Confidence              float64 `json:"confidence"`
	ConfidenceBand          string  `json:"confidenceBand"`
	FormulaVersion          string  `json:"formulaVersion"`
	ImpactAreaSquareMeters  float64 `json:"impactAreaSquareMeters"`
	SnapshotID              string  `json:"snapshotId"`
	Status                  string  `json:"status"`
}

// SurvivalAuthorityAnalysis 是历史回放允许发送给 LLM 的匿名化规则投影。
type SurvivalAuthorityAnalysis struct {
	AssessmentID      string                     `json:"assessmentId"`
	CaseID            string                     `json:"caseId"`
	Factors           []string                   `json:"factors"`
	HumanReviewStatus string                     `json:"humanReviewStatus"`
	Limitations       []string                   `json:"limitations"`
	ModelVersion      string                     `json:"modelVersion"`
	Priority          string                     `json:"priority"`
	ProbabilityBand   string                     `json:"probabilityBand"`
	ProbabilityHigh   float64                    `json:"probabilityHigh"`
	ProbabilityLow    float64                    `json:"probabilityLow"`
	ScenarioDigest    string                     `json:"scenarioDigest"`
	ScenarioID        string                     `json:"scenarioId"`
	Score             int                        `json:"score"`
	ScoreBand         string                     `json:"scoreBand"`
	Usage             survivaldomain.ReplayUsage `json:"usage"`
}

// Normalize 去除引用边界空白并校验类型白名单和标识格式。
func (r AnalysisReference) Normalize() (AnalysisReference, error) {
	r.Kind = AuthorityKind(strings.TrimSpace(string(r.Kind)))
	r.ID = strings.TrimSpace(r.ID)
	if err := r.Validate(); err != nil {
		return AnalysisReference{}, err
	}
	return r, nil
}

// Validate 校验引用只能指向受支持的服务端权威对象。
func (r AnalysisReference) Validate() error {
	if !supportedAuthorityKind(r.Kind) {
		return fmt.Errorf("%w: 不支持的权威分析类型 %q", domain.ErrInvalidInput, r.Kind)
	}
	if !validAuthorityID(r.ID) {
		return fmt.Errorf("%w: 权威分析引用标识无效", domain.ErrInvalidInput)
	}
	return nil
}

// Reference 返回 Authority 绑定的外部引用。
func (a Authority) Reference() AnalysisReference {
	return AnalysisReference{Kind: a.Kind, ID: a.ID}
}

// Canonical 使用固定 schema 拒绝额外字段，并返回稳定的 JSON 和不可变字段列表。
func (a Authority) Canonical() (Authority, error) {
	reference, err := a.Reference().Normalize()
	if err != nil {
		return Authority{}, fmt.Errorf("%w: %w", ErrInvalidAuthority, err)
	}
	a.Kind, a.ID = reference.Kind, reference.ID
	a.Version, a.SchemaVersion = strings.TrimSpace(a.Version), strings.TrimSpace(a.SchemaVersion)
	if err = validateAuthorityMetadata(a); err != nil {
		return Authority{}, err
	}
	analysis, fields, err := canonicalAnalysis(a)
	if err != nil {
		return Authority{}, err
	}
	if len(a.ImmutableFields) > 0 && !sameCanonicalFields(a.ImmutableFields, fields) {
		return Authority{}, fmt.Errorf("%w: resolver 返回的不可变字段与固定 schema 不一致", ErrInvalidAuthority)
	}
	a.AnalysisJSON, a.ImmutableFields = analysis, fields
	return a, nil
}

// Validate 校验 Authority 已经采用固定 schema 的规范形式。
func (a Authority) Validate() error {
	canonical, err := a.Canonical()
	if err != nil {
		return err
	}
	if canonical.Kind != a.Kind || canonical.ID != a.ID || canonical.Version != a.Version ||
		canonical.SchemaVersion != a.SchemaVersion || !bytes.Equal(canonical.AnalysisJSON, a.AnalysisJSON) ||
		!sameStrings(canonical.ImmutableFields, a.ImmutableFields) {
		return fmt.Errorf("%w: 权威分析未采用规范形式", ErrInvalidAuthority)
	}
	return nil
}

func canonicalAnalysis(value Authority) (json.RawMessage, []string, error) {
	switch value.Kind {
	case AuthorityHazardSnapshot:
		var analysis HazardAuthorityAnalysis
		return canonicalTypedAnalysis(value, &analysis, hazardFields, func(value Authority) error {
			return analysis.validate(value)
		})
	case AuthorityEvacuationRoute:
		var analysis RouteAuthorityAnalysis
		return canonicalTypedAnalysis(value, &analysis, routeFields, func(value Authority) error {
			return analysis.validate(value)
		})
	case AuthorityLossAssessment:
		var analysis LossAuthorityAnalysis
		return canonicalTypedAnalysis(value, &analysis, lossFields, func(value Authority) error {
			return analysis.validate(value)
		})
	case AuthoritySurvivalAssessment:
		var analysis SurvivalAuthorityAnalysis
		return canonicalTypedAnalysis(value, &analysis, survivalFields, func(value Authority) error {
			return analysis.validate(value)
		})
	default:
		return nil, nil, fmt.Errorf("%w: 权威分析类型未进入白名单", ErrInvalidAuthority)
	}
}

func canonicalTypedAnalysis(value Authority, destination any, fields []string,
	validate func(Authority) error,
) (json.RawMessage, []string, error) {
	object, err := analysisObject(value.AnalysisJSON)
	if err != nil {
		return nil, nil, err
	}
	if err = validateObjectFields(object, fields, "Authority"); err != nil {
		return nil, nil, err
	}
	if err = validateNestedAuthorityFields(value.Kind, object); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(value.AnalysisJSON, destination); err != nil {
		return nil, nil, fmt.Errorf("%w: 解码固定 schema: %w", ErrInvalidAuthority, err)
	}
	if err = validate(value); err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(destination)
	if err != nil {
		return nil, nil, fmt.Errorf("编码固定 Authority schema: %w", err)
	}
	return payload, append([]string(nil), fields...), nil
}

func analysisObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: 权威分析必须是 JSON 对象", ErrInvalidAuthority)
	}
	return object, nil
}

func validateObjectFields(object map[string]json.RawMessage, fields []string, label string) error {
	allowed := stringSet(fields)
	for field := range object {
		if _, exists := allowed[field]; !exists {
			return fmt.Errorf("%w: %s schema 外字段 %q", ErrUnsafeStoredAnalysis, label, field)
		}
	}
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return fmt.Errorf("%w: %s 缺少 schema 字段 %q", ErrInvalidAuthority, label, field)
		}
	}
	return nil
}

func validateNestedAuthorityFields(kind AuthorityKind, object map[string]json.RawMessage) error {
	if kind != AuthoritySurvivalAssessment {
		return nil
	}
	usage, err := analysisObject(object["usage"])
	if err != nil {
		return fmt.Errorf("生还回放用途声明: %w", err)
	}
	return validateObjectFields(usage, survivalUsageFields, "生还回放 usage")
}

func (a HazardAuthorityAnalysis) validate(authority Authority) error {
	if a.SnapshotID != authority.ID || a.RuleVersion != authority.Version ||
		!validHazardType(a.HazardType) || !validRiskLevel(a.RiskLevel) {
		return fmt.Errorf("%w: 风险快照或规则版本绑定不一致", ErrInvalidAuthority)
	}
	if !validHazardQuality(a.DataStatus, a.ConfidenceLevel) || !finite(a.AffectedAreaSquareMeters) ||
		!validHazardGeometrySummary(a.RiskZoneCount, a.RiskLevel, a.AffectedAreaSquareMeters) {
		return fmt.Errorf("%w: 风险快照数值或数据质量组合无效", ErrInvalidAuthority)
	}
	return nil
}

func (a RouteAuthorityAnalysis) validate(authority Authority) error {
	if a.RouteAnalysisID != authority.ID || a.RouteID == "" || a.RuleVersion != authority.Version || a.SnapshotID == "" ||
		!validTravelMode(a.Mode) || a.IntersectsRiskZone || a.Rank < 1 || a.DistanceMeters <= 0 || a.DurationSeconds <= 0 {
		return fmt.Errorf("%w: 路线快照、交通方式或安全绑定不一致", ErrInvalidAuthority)
	}
	if !finite(a.DistanceMeters) || !finite(a.RiskScore) || a.RiskScore < 0 || a.RiskScore > 100 ||
		(!a.RiskScoreAvailable && a.RiskScore != 0) {
		return fmt.Errorf("%w: 路线安全数值无效", ErrInvalidAuthority)
	}
	return nil
}

func (a LossAuthorityAnalysis) validate(authority Authority) error {
	if a.AssessmentID != authority.ID || a.FormulaVersion != authority.Version ||
		a.SnapshotID == "" || !validLossStatus(a.Status) || !validLossConfidenceBand(a.ConfidenceBand) {
		return fmt.Errorf("%w: 损失评估、快照或公式绑定不一致", ErrInvalidAuthority)
	}
	low, lowErr := nonNegativeDecimal(a.ConditionalLowCents)
	central, centralErr := nonNegativeDecimal(a.ConditionalCentralCents)
	high, highErr := nonNegativeDecimal(a.ConditionalHighCents)
	if lowErr != nil || centralErr != nil || highErr != nil || low.Cmp(central) > 0 || central.Cmp(high) > 0 {
		return fmt.Errorf("%w: 损失金额区间无效", ErrInvalidAuthority)
	}
	if !finite(a.ImpactAreaSquareMeters) || a.ImpactAreaSquareMeters < 0 ||
		!finite(a.AffectedPopulation) || a.AffectedPopulation < 0 ||
		!finite(a.Confidence) || a.Confidence < 0 || a.Confidence > 1 {
		return fmt.Errorf("%w: 损失影响或置信度无效", ErrInvalidAuthority)
	}
	if a.ConfidenceBand != lossConfidenceBand(a.Confidence) {
		return fmt.Errorf("%w: 损失置信度等级与数值不一致", ErrInvalidAuthority)
	}
	return nil
}

func (a SurvivalAuthorityAnalysis) validate(authority Authority) error {
	if a.AssessmentID != authority.ID || !validSHA256Digest(a.AssessmentID) ||
		!validAuthorityID(a.CaseID) || !validAuthorityID(a.ScenarioID) ||
		!validSHA256Digest(a.ScenarioDigest) || a.ModelVersion != authority.Version ||
		a.HumanReviewStatus != "required" || !validReplayUsage(a.Usage) {
		return fmt.Errorf("%w: 生还场景、模型或人工复核绑定不一致", ErrInvalidAuthority)
	}
	if a.Score < 0 || a.Score > 100 || !finite(a.ProbabilityLow) || !finite(a.ProbabilityHigh) ||
		!validSurvivalBands(a) || !validSurvivalPriority(a.Score, a.Priority) {
		return fmt.Errorf("%w: 生还评估分数、概率或优先级组合无效", ErrInvalidAuthority)
	}
	if !validSurvivalAuthorityText(a.Factors, true) || !validSurvivalAuthorityText(a.Limitations, true) {
		return fmt.Errorf("%w: 生还评估因素或限制无效", ErrInvalidAuthority)
	}
	return nil
}

func validateAuthorityMetadata(value Authority) error {
	expectedSchema := schemaForKind(value.Kind)
	if value.Version == "" || len(value.Version) > maxAuthorityToken ||
		value.SchemaVersion != expectedSchema {
		return fmt.Errorf("%w: 权威分析版本或固定 schema 无效", ErrInvalidAuthority)
	}
	if value.ResolvedAt.IsZero() {
		return fmt.Errorf("%w: 权威分析解析时间为空", ErrInvalidAuthority)
	}
	if _, offset := value.ResolvedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 权威分析解析时间必须使用 UTC", ErrInvalidAuthority)
	}
	return nil
}

func schemaForKind(kind AuthorityKind) string {
	switch kind {
	case AuthorityHazardSnapshot:
		return AuthoritySchemaHazardV1
	case AuthorityEvacuationRoute:
		return AuthoritySchemaRouteV1
	case AuthorityLossAssessment:
		return AuthoritySchemaLossV1
	case AuthoritySurvivalAssessment:
		return AuthoritySchemaSurvivalV1
	default:
		return ""
	}
}

func supportedAuthorityKind(kind AuthorityKind) bool { return schemaForKind(kind) != "" }

func validAuthorityID(value string) bool {
	if len(value) == 0 || len(value) > maxAuthorityToken || !asciiLetterOrDigit(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !asciiLetterOrDigit(char) && !strings.ContainsRune("._:-", rune(char)) {
			return false
		}
	}
	return true
}

func asciiLetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func sameCanonicalFields(values, expected []string) bool {
	copyValues := make([]string, len(values))
	for index, value := range values {
		copyValues[index] = strings.TrimSpace(value)
	}
	sort.Strings(copyValues)
	return sameStrings(copyValues, expected)
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validHazardType(value string) bool {
	return value == string(hazarddomain.TypeLandslide) || value == string(hazarddomain.TypeDebrisFlow)
}

func validRiskLevel(value string) bool {
	switch hazarddomain.RiskLevel(value) {
	case hazarddomain.RiskLow, hazarddomain.RiskModerate, hazarddomain.RiskHigh, hazarddomain.RiskVeryHigh:
		return true
	default:
		return false
	}
}

func validHazardQuality(status, confidence string) bool {
	switch riskdomain.DataStatus(status) {
	case riskdomain.DataCurrent:
		return confidence == string(riskdomain.ConfidenceHigh) || confidence == string(riskdomain.ConfidenceMedium)
	case riskdomain.DataFallback:
		return confidence == string(riskdomain.ConfidenceLow)
	default:
		return false
	}
}

func validHazardGeometrySummary(zoneCount int, riskLevel string, area float64) bool {
	if zoneCount < 0 || area < 0 {
		return false
	}
	if zoneCount == 0 {
		return riskLevel == string(hazarddomain.RiskLow) && area == 0
	}
	return area > 0
}

func validTravelMode(value string) bool {
	switch evacuationdomain.TravelMode(value) {
	case evacuationdomain.TravelDriving, evacuationdomain.TravelWalking, evacuationdomain.TravelTransit:
		return true
	default:
		return false
	}
}

func validLossStatus(value string) bool {
	status := lossdomain.AssessmentStatus(value)
	return status == lossdomain.AssessmentAvailable || status == lossdomain.AssessmentInsufficientData ||
		status == lossdomain.AssessmentReferenceOnly
}

func validLossConfidenceBand(value string) bool {
	return value == "high" || value == "moderate" || value == "low" || value == "very_low"
}

func lossConfidenceBand(value float64) string {
	switch {
	case value >= 0.8:
		return "high"
	case value >= 0.5:
		return "moderate"
	case value >= 0.25:
		return "low"
	default:
		return "very_low"
	}
}

func validSurvivalBands(value SurvivalAuthorityAnalysis) bool {
	low, high, scoreBand, probabilityBand := expectedSurvivalBands(value.Score)
	return value.ScoreBand == scoreBand && value.ProbabilityBand == probabilityBand &&
		value.ProbabilityLow == low && value.ProbabilityHigh == high
}

func expectedSurvivalBands(score int) (float64, float64, string, string) {
	switch {
	case score >= 75:
		return 0.60, 0.85, "high", string(survivaldomain.ProbabilityHigh)
	case score >= 50:
		return 0.35, 0.59, "moderate", string(survivaldomain.ProbabilityModerate)
	case score >= 25:
		return 0.15, 0.34, "low", string(survivaldomain.ProbabilityLow)
	default:
		return 0.05, 0.14, "very_low", string(survivaldomain.ProbabilityVeryLow)
	}
}

func validSurvivalPriority(score int, value string) bool {
	priority := survivaldomain.Priority(value)
	switch {
	case score >= 75:
		return priority == survivaldomain.PriorityImmediate
	case score >= 55:
		return priority == survivaldomain.PriorityUrgent || priority == survivaldomain.PriorityImmediate
	case score >= 50:
		return priority == survivaldomain.PriorityUrgent
	case score >= 25:
		return priority == survivaldomain.PriorityElevated
	default:
		return priority == survivaldomain.PriorityRoutine
	}
}

func validReplayUsage(value survivaldomain.ReplayUsage) bool {
	expected := survivaldomain.HistoricalReplayUsage()
	return value == expected && len([]rune(value.Disclaimer)) <= maxAuthorityDisclaimerRunes
}

func validSurvivalAuthorityText(values []string, required bool) bool {
	if len(values) > maxSurvivalAuthorityItems || required && len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len([]rune(value)) > maxSurvivalAuthorityRunes {
			return false
		}
	}
	return true
}

func validSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if !asciiDigitOrLowerHex(char) {
			return false
		}
	}
	return true
}

func asciiDigitOrLowerHex(value rune) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

func nonNegativeDecimal(value string) (*big.Int, error) {
	if value == "" || value != "0" && value[0] == '0' {
		return nil, fmt.Errorf("十进制金额不是规范非负整数")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return nil, fmt.Errorf("十进制金额包含非数字字符")
		}
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("十进制金额无法解析")
	}
	return result, nil
}

var hazardFields = []string{
	"affectedAreaSquareMeters", "confidenceLevel", "dataStatus", "hazardType",
	"riskLevel", "riskZoneCount", "ruleVersion", "snapshotId",
}

var routeFields = []string{
	"distanceMeters", "durationSeconds", "intersectsRiskZone", "mode", "rank",
	"riskScore", "riskScoreAvailable", "routeAnalysisId", "routeId", "ruleVersion", "snapshotId",
}

var lossFields = []string{
	"affectedPopulation", "assessmentId", "conditionalCentralCents", "conditionalHighCents",
	"conditionalLowCents", "confidence", "confidenceBand", "formulaVersion",
	"impactAreaSquareMeters", "snapshotId", "status",
}

var survivalFields = []string{
	"assessmentId", "caseId", "factors", "humanReviewStatus", "limitations", "modelVersion", "priority", "probabilityBand",
	"probabilityHigh", "probabilityLow", "scenarioDigest", "scenarioId", "score", "scoreBand", "usage",
}

var survivalUsageFields = []string{
	"disclaimer", "liveUseAllowed", "mode", "syntheticInput",
}
