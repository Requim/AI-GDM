package loss

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// AssetType 表示损失评估中的资产类别。
type AssetType string

const (
	AssetBuilding AssetType = "building"
	AssetRoad     AssetType = "road"
	AssetFacility AssetType = "facility"
)

// ExposureKind 表示基线中的区域暴露量类别。
type ExposureKind string

const (
	ExposurePopulation ExposureKind = "population"
	ExposureRoad       ExposureKind = "road"
)

// BaselineStatus 标识基线是演示参数还是已审核数据。
type BaselineStatus string

const (
	BaselineDemoOnly BaselineStatus = "demo_only"
	BaselineApproved BaselineStatus = "approved"
)

// BaselineLevel 标识评估实际选用的是区域级还是国家级基线。
type BaselineLevel string

const (
	BaselineRegional BaselineLevel = "regional"
	BaselineNational BaselineLevel = "national"
	// BaselineReferenceCase 标识仅用于研究参考的跨区域案例基线。
	BaselineReferenceCase BaselineLevel = "reference_case"
)

// AssessmentStatus 表示损失评估是否具备完整输入。
type AssessmentStatus string

const (
	AssessmentAvailable        AssessmentStatus = "available"
	AssessmentInsufficientData AssessmentStatus = "insufficient_data"
	// AssessmentReferenceOnly 表示可计算但只能作为局部研究参考。
	AssessmentReferenceOnly AssessmentStatus = "reference_only"
)

// Exposure 描述风险区内暴露的资产或人口数量。
type Exposure struct {
	FeatureID       string    `json:"featureId"`
	ZoneID          string    `json:"zoneId"`
	ZoneIDs         []string  `json:"zoneIds"`
	AssetType       AssetType `json:"assetType"`
	Quantity        float64   `json:"quantity"`
	Unit            string    `json:"unit"`
	CoverageRatio   float64   `json:"coverageRatio"`
	Provided        bool      `json:"provided"`
	MetricStatus    string    `json:"metricStatus"`
	IntensityBand   string    `json:"intensityBand"`
	AnalysisID      string    `json:"analysisId"`
	AnalysisVersion string    `json:"analysisVersion"`
	InputReferences []string  `json:"inputReferences"`
}

// CostBaseline 保存直接物理损害所需的单位重置成本情景带，金额单位为人民币分。
type CostBaseline struct {
	ID            string                `json:"id"`
	AssetType     AssetType             `json:"assetType"`
	RegionCode    string                `json:"regionCode"`
	Unit          string                `json:"unit"`
	LowCents      int64                 `json:"lowCents"`
	CentralCents  int64                 `json:"centralCents"`
	HighCents     int64                 `json:"highCents"`
	Currency      string                `json:"currency"`
	PriceBaseDate time.Time             `json:"priceBaseDate"`
	Status        BaselineStatus        `json:"status"`
	Provided      bool                  `json:"provided"`
	BaselineLevel BaselineLevel         `json:"baselineLevel"`
	ApprovedBy    string                `json:"approvedBy,omitempty"`
	Source        provenance.Provenance `json:"source"`
}

// Vulnerability 保存有来源的影响比例和损伤率情景带。
type Vulnerability struct {
	ID                 string                `json:"id"`
	AssetType          AssetType             `json:"assetType"`
	HazardType         string                `json:"hazardType"`
	IntensityBand      string                `json:"intensityBand"`
	ImpactFractionLow  float64               `json:"impactFractionLow"`
	ImpactFractionMid  float64               `json:"impactFractionMid"`
	ImpactFractionHigh float64               `json:"impactFractionHigh"`
	DamageRatioLow     float64               `json:"damageRatioLow"`
	DamageRatioMid     float64               `json:"damageRatioMid"`
	DamageRatioHigh    float64               `json:"damageRatioHigh"`
	CalibrationRegion  string                `json:"calibrationRegion"`
	Status             BaselineStatus        `json:"status"`
	Provided           bool                  `json:"provided"`
	BaselineLevel      BaselineLevel         `json:"baselineLevel"`
	ApprovedBy         string                `json:"approvedBy,omitempty"`
	Source             provenance.Provenance `json:"source"`
}

// ExposureBaseline 保存人口或道路基线，数量可以是真实的零值。
type ExposureBaseline struct {
	ID            string                `json:"id"`
	RegionCode    string                `json:"regionCode"`
	Kind          ExposureKind          `json:"kind"`
	Quantity      float64               `json:"quantity"`
	Unit          string                `json:"unit"`
	DataYear      int                   `json:"dataYear"`
	CoverageRatio float64               `json:"coverageRatio"`
	Source        provenance.Provenance `json:"source"`
}

// BaselineSet 是同一数据集版本的一组可原子替换基线记录。
type BaselineSet struct {
	Version         string             `json:"version"`
	Population      []ExposureBaseline `json:"population"`
	Roads           []ExposureBaseline `json:"roads"`
	Costs           []CostBaseline     `json:"costs"`
	Vulnerabilities []Vulnerability    `json:"vulnerabilities"`
}

// Assessment 保存低、中、高情景下的直接物理损害估算。
type Assessment struct {
	ID                   string             `json:"id"`
	SnapshotID           string             `json:"snapshotId"`
	FormulaVersion       string             `json:"formulaVersion"`
	ScenarioMethod       string             `json:"scenarioMethod"`
	HazardType           string             `json:"hazardType"`
	RegionCode           string             `json:"regionCode"`
	ConditionalLowCents  int64              `json:"conditionalLowCents"`
	ConditionalMidCents  int64              `json:"conditionalCentralCents"`
	ConditionalHighCents int64              `json:"conditionalHighCents"`
	ExpectedLowCents     *int64             `json:"expectedLowCents,omitempty"`
	ExpectedMidCents     *int64             `json:"expectedCentralCents,omitempty"`
	ExpectedHighCents    *int64             `json:"expectedHighCents,omitempty"`
	ImpactAreaSquareM    float64            `json:"impactAreaSquareMeters"`
	AffectedPopulation   float64            `json:"affectedPopulation"`
	AffectedRoadMeters   float64            `json:"affectedRoadMeters"`
	AffectedFacilities   int                `json:"affectedFacilities"`
	InputReferences      []string           `json:"inputReferences"`
	IncludedAssets       []AssetType        `json:"includedAssets"`
	ExcludedLosses       []string           `json:"excludedLosses"`
	Status               AssessmentStatus   `json:"status"`
	Confidence           float64            `json:"confidence"`
	ConfidenceBand       string             `json:"confidenceBand"`
	Limitations          []string           `json:"limitations"`
	CalculatedAt         time.Time          `json:"calculatedAt"`
	InputDigest          string             `json:"inputDigest"`
	Evidence             AssessmentEvidence `json:"evidence"`
}

const (
	// FormulaVersion 标识当前可回放损失计算公式。
	FormulaVersion = "ai-gdm-loss-formula-v2"
	// LimitationDirectPhysicalLoss 明确当前金额只覆盖道路和风险区内设施的直接物理损失。
	LimitationDirectPhysicalLoss = "仅估算道路和风险区内 POI 设施的直接物理损失"
	// LimitationAdvisoryOnly 明确评估不能替代法定灾损核定。
	LimitationAdvisoryOnly = "结果用于辅助研判，不替代法定灾损核定"
	// LimitationReferenceOnly 明确局部参考结果不得外推。
	LimitationReferenceOnly = "本次金额为局部热点研究参考区间，不代表全国或法定灾损"
	// LimitationReferenceRoadOnly 明确参考金额只覆盖道路。
	LimitationReferenceRoadOnly = "研究参考金额仅计算道路；人口和设施仅作暴露背景，未货币化"
	// LimitationReferenceTransfer 明确参考参数的地域和换算限制。
	LimitationReferenceTransfer = "道路条件损失参数来自西藏吉隆藏布流域案例并按历史欧元汇率换算，跨区域外推不确定性高"
)

// Validate 校验风险区暴露记录的数值、单位和来源。
func (e Exposure) Validate() error {
	if !validEvidenceIdentifier(e.FeatureID) || strings.TrimSpace(e.IntensityBand) == "" ||
		strings.TrimSpace(e.AnalysisID) == "" || strings.TrimSpace(e.AnalysisVersion) == "" ||
		!finite(e.Quantity) || e.Quantity < 0 || !finite(e.CoverageRatio) || e.CoverageRatio <= 0 || e.CoverageRatio > 1 {
		return fmt.Errorf("%w: 损失暴露字段或数值无效", domain.ErrInvalidInput)
	}
	if e.AssetType != AssetRoad && e.AssetType != AssetFacility {
		return fmt.Errorf("%w: 损失暴露资产类别无效", domain.ErrInvalidInput)
	}
	wantUnit := "meters"
	if e.AssetType == AssetFacility {
		wantUnit = "count"
	}
	if e.Unit != wantUnit || !e.Provided || e.MetricStatus != "available" {
		return fmt.Errorf("%w: 损失暴露单位或状态无效", domain.ErrInvalidInput)
	}
	if err := validateStringList("损失暴露风险区", e.ZoneIDs, true); err != nil || e.ZoneID != e.ZoneIDs[0] {
		return fmt.Errorf("%w: 损失暴露风险区绑定无效", domain.ErrInvalidInput)
	}
	if err := validateStringList("损失暴露输入引用", e.InputReferences, true); err != nil {
		return err
	}
	return nil
}

// Validate 校验损失评估结果可回放且金额、置信度有界。
func (a Assessment) Validate() error {
	if err := validateAssessmentCore(a); err != nil {
		return err
	}
	if err := a.Evidence.Validate(); err != nil {
		return fmt.Errorf("校验损失评估证据: %w", err)
	}
	return validateAssessmentIdentity(a)
}

func validateAssessmentCore(a Assessment) error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.SnapshotID) == "" || strings.TrimSpace(a.HazardType) == "" ||
		strings.TrimSpace(a.RegionCode) == "" || a.FormulaVersion != FormulaVersion || strings.TrimSpace(a.ScenarioMethod) == "" ||
		a.CalculatedAt.IsZero() || !validIdentityTime(a.CalculatedAt) || !validSHA256(a.InputDigest) {
		return fmt.Errorf("%w: 损失评估身份、公式或时间无效", domain.ErrInvalidInput)
	}
	if a.ConditionalLowCents < 0 || a.ConditionalLowCents > a.ConditionalMidCents || a.ConditionalMidCents > a.ConditionalHighCents {
		return fmt.Errorf("%w: 损失情景金额区间无效", domain.ErrInvalidInput)
	}
	if !finite(a.ImpactAreaSquareM) || a.ImpactAreaSquareM < 0 || !finite(a.AffectedPopulation) || a.AffectedPopulation < 0 ||
		!finite(a.AffectedRoadMeters) || a.AffectedRoadMeters < 0 || a.AffectedFacilities < 0 || !finite(a.Confidence) ||
		a.Confidence < 0 || a.Confidence > 1 {
		return fmt.Errorf("%w: 损失影响范围或置信度无效", domain.ErrInvalidInput)
	}
	if a.Status != AssessmentAvailable && a.Status != AssessmentInsufficientData && a.Status != AssessmentReferenceOnly {
		return fmt.Errorf("%w: 损失评估状态无效", domain.ErrInvalidInput)
	}
	if a.ConfidenceBand != "high" && a.ConfidenceBand != "moderate" &&
		a.ConfidenceBand != "low" && a.ConfidenceBand != "very_low" {
		return fmt.Errorf("%w: 损失评估置信度等级无效", domain.ErrInvalidInput)
	}
	if (a.Status == AssessmentAvailable || a.Status == AssessmentReferenceOnly) && len(a.InputReferences) == 0 {
		return fmt.Errorf("%w: 可用损失评估缺少输入依据", domain.ErrInvalidInput)
	}
	if a.Status == AssessmentReferenceOnly && (a.Confidence >= 0.5 ||
		(a.ConfidenceBand != "low" && a.ConfidenceBand != "very_low")) {
		return fmt.Errorf("%w: 研究参考损失评估置信度过高", domain.ErrInvalidInput)
	}
	if err := validateStringList("损失评估输入引用", a.InputReferences, true); err != nil {
		return err
	}
	if err := validateStringList("损失评估排除项", a.ExcludedLosses, false); err != nil {
		return err
	}
	if err := validateStringList("损失评估限制", a.Limitations, true); err != nil {
		return err
	}
	if !containsString(a.Limitations, LimitationAdvisoryOnly) || !hasRequiredAssessmentLimitations(a) {
		return fmt.Errorf("%w: 损失评估缺少强制适用范围说明", domain.ErrInvalidInput)
	}
	return nil
}

func hasRequiredAssessmentLimitations(value Assessment) bool {
	if value.Status != AssessmentReferenceOnly {
		return containsString(value.Limitations, LimitationDirectPhysicalLoss)
	}
	return containsString(value.Limitations, LimitationReferenceOnly) &&
		containsString(value.Limitations, LimitationReferenceRoadOnly) &&
		containsString(value.Limitations, LimitationReferenceTransfer)
}

// Validate 校验成本情景带、币种和审核要求。
func (b CostBaseline) Validate() error {
	if strings.TrimSpace(b.ID) == "" || b.RegionCode == "" || strings.TrimSpace(b.RegionCode) != b.RegionCode {
		return fmt.Errorf("%w: 成本基线标识或区域无效", domain.ErrInvalidInput)
	}
	if b.AssetType != AssetBuilding && b.AssetType != AssetRoad && b.AssetType != AssetFacility {
		return fmt.Errorf("%w: 成本基线资产类别无效", domain.ErrInvalidInput)
	}
	if b.LowCents < 0 || b.LowCents > b.CentralCents || b.CentralCents > b.HighCents {
		return fmt.Errorf("%w: 单位成本情景带无序", domain.ErrInvalidInput)
	}
	if b.Currency != "CNY" || b.PriceBaseDate.IsZero() || b.Unit == "" || !isUTC(b.PriceBaseDate) {
		return fmt.Errorf("%w: 成本币种、单位或基准日无效", domain.ErrInvalidInput)
	}
	if err := validateBaselineSource(b.Source); err != nil {
		return fmt.Errorf("校验成本基线来源: %w", err)
	}
	if b.Status == BaselineApproved && !validApprovedBy(b.ApprovedBy) {
		return fmt.Errorf("%w: 已审核基线缺少审核人", domain.ErrInvalidInput)
	}
	if b.Status != BaselineDemoOnly && b.Status != BaselineApproved {
		return fmt.Errorf("%w: 成本基线状态无效", domain.ErrInvalidInput)
	}
	if !validSelectionMetadata(b.Provided, b.BaselineLevel) {
		return fmt.Errorf("%w: 成本基线选用状态无效", domain.ErrInvalidInput)
	}
	return nil
}

// Validate 校验脆弱性系数、情景带和基线来源。
func (v Vulnerability) Validate() error {
	if strings.TrimSpace(v.ID) == "" || v.HazardType == "" || strings.TrimSpace(v.HazardType) != v.HazardType ||
		v.IntensityBand == "" || strings.TrimSpace(v.IntensityBand) != v.IntensityBand ||
		v.CalibrationRegion == "" || strings.TrimSpace(v.CalibrationRegion) != v.CalibrationRegion {
		return fmt.Errorf("%w: 脆弱性基线标识或分类无效", domain.ErrInvalidInput)
	}
	if v.AssetType != AssetBuilding && v.AssetType != AssetRoad && v.AssetType != AssetFacility {
		return fmt.Errorf("%w: 脆弱性基线资产类别无效", domain.ErrInvalidInput)
	}
	if err := validateFractionBand(v.ImpactFractionLow, v.ImpactFractionMid, v.ImpactFractionHigh); err != nil {
		return fmt.Errorf("影响比例情景带: %w", err)
	}
	if err := validateFractionBand(v.DamageRatioLow, v.DamageRatioMid, v.DamageRatioHigh); err != nil {
		return fmt.Errorf("损伤率情景带: %w", err)
	}
	if v.Status != BaselineDemoOnly && v.Status != BaselineApproved {
		return fmt.Errorf("%w: 脆弱性基线状态无效", domain.ErrInvalidInput)
	}
	if v.Status == BaselineApproved && !validApprovedBy(v.ApprovedBy) {
		return fmt.Errorf("%w: 已审核脆弱性基线缺少审核人", domain.ErrInvalidInput)
	}
	if err := validateBaselineSource(v.Source); err != nil {
		return fmt.Errorf("校验脆弱性基线来源: %w", err)
	}
	if !validSelectionMetadata(v.Provided, v.BaselineLevel) {
		return fmt.Errorf("%w: 脆弱性基线选用状态无效", domain.ErrInvalidInput)
	}
	return nil
}

// Validate 校验人口或道路基线的数量、覆盖率和来源。
func (b ExposureBaseline) Validate() error {
	if strings.TrimSpace(b.ID) == "" || b.RegionCode == "" || strings.TrimSpace(b.RegionCode) != b.RegionCode {
		return fmt.Errorf("%w: 暴露基线标识或区域无效", domain.ErrInvalidInput)
	}
	if b.Kind != ExposurePopulation && b.Kind != ExposureRoad {
		return fmt.Errorf("%w: 暴露基线类别无效", domain.ErrInvalidInput)
	}
	wantUnit := "people"
	if b.Kind == ExposureRoad {
		wantUnit = "meters"
	}
	if b.Unit != wantUnit || !finite(b.Quantity) || b.Quantity < 0 || b.DataYear < 1900 || b.DataYear > 9999 ||
		!finite(b.CoverageRatio) || b.CoverageRatio <= 0 || b.CoverageRatio > 1 {
		return fmt.Errorf("%w: 暴露基线数值、单位或年份无效", domain.ErrInvalidInput)
	}
	if err := validateBaselineSource(b.Source); err != nil {
		return fmt.Errorf("校验暴露基线来源: %w", err)
	}
	return nil
}

// Validate 校验同一版本基线记录的完整性和唯一性。
func (s BaselineSet) Validate() error {
	if strings.TrimSpace(s.Version) != s.Version || s.Version == "" || len(s.Version) > 128 {
		return fmt.Errorf("%w: 基线数据集版本无效", domain.ErrInvalidInput)
	}
	if len(s.Population) == 0 || len(s.Roads) == 0 || len(s.Costs) == 0 || len(s.Vulnerabilities) == 0 {
		return fmt.Errorf("%w: 基线数据集缺少人口、道路、成本或脆弱性记录", domain.ErrInvalidInput)
	}
	total := len(s.Population) + len(s.Roads) + len(s.Costs) + len(s.Vulnerabilities)
	seen := make(map[string]struct{}, total)
	if err := validateExposureSet(s.Version, s.Population, ExposurePopulation, seen); err != nil {
		return err
	}
	if err := validateExposureSet(s.Version, s.Roads, ExposureRoad, seen); err != nil {
		return err
	}
	if err := validateCostSet(s.Version, s.Costs, seen); err != nil {
		return err
	}
	return validateVulnerabilitySet(s.Version, s.Vulnerabilities, seen)
}

func validateExposureSet(version string, values []ExposureBaseline, expected ExposureKind, seen map[string]struct{}) error {
	label := "人口"
	if expected == ExposureRoad {
		label = "道路"
	}
	for _, value := range values {
		if err := validateBaselineRecord(version, value.ID, value.Source, value.Validate()); err != nil {
			return err
		}
		if value.Kind != expected {
			return fmt.Errorf("%w: %s基线类别不匹配", domain.ErrInvalidInput, label)
		}
		if err := addBaselineID(seen, value.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateCostSet(version string, values []CostBaseline, seen map[string]struct{}) error {
	for _, value := range values {
		if err := validateBaselineRecord(version, value.ID, value.Source, value.Validate()); err != nil {
			return err
		}
		if err := addBaselineID(seen, value.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateVulnerabilitySet(version string, values []Vulnerability, seen map[string]struct{}) error {
	for _, value := range values {
		if err := validateBaselineRecord(version, value.ID, value.Source, value.Validate()); err != nil {
			return err
		}
		if err := addBaselineID(seen, value.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateBaselineRecord(version, id string, source provenance.Provenance, recordErr error) error {
	if recordErr != nil {
		return recordErr
	}
	if source.DatasetVersion != version {
		return fmt.Errorf("%w: 基线记录 %s 的数据集版本不一致", domain.ErrInvalidInput, id)
	}
	return nil
}

func addBaselineID(seen map[string]struct{}, id string) error {
	if _, exists := seen[id]; exists {
		return fmt.Errorf("%w: 基线记录标识重复 %s", domain.ErrInvalidInput, id)
	}
	seen[id] = struct{}{}
	return nil
}

func validateBaselineSource(source provenance.Provenance) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if source.DataKind != provenance.DataKindBaseline || strings.TrimSpace(source.DatasetVersion) == "" {
		return fmt.Errorf("%w: 基线来源必须声明 baseline 分类和数据集版本", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(source.SourceRevision) == "" || strings.TrimSpace(source.Citation) == "" ||
		strings.TrimSpace(source.License) == "" || strings.TrimSpace(source.TransformVersion) == "" ||
		len(source.QualityFlags) == 0 || !validBaselineSHA(source.SHA256) {
		return fmt.Errorf("%w: 基线来源缺少修订、引用、许可、校验和或质量标志", domain.ErrInvalidInput)
	}
	if source.ValidFrom.IsZero() || !isUTC(source.ValidFrom) || (!source.ValidTo.IsZero() && !isUTC(source.ValidTo)) ||
		(!source.ValidTo.IsZero() && !source.ValidTo.After(source.ValidFrom)) {
		return fmt.Errorf("%w: 基线来源有效期无效", domain.ErrInvalidInput)
	}
	return nil
}

func validBaselineSHA(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateFractionBand(low, mid, high float64) error {
	if !finite(low) || !finite(mid) || !finite(high) || low < 0 || low > mid || mid > high || high > 1 {
		return fmt.Errorf("%w: 比例必须位于零到一且有序", domain.ErrInvalidInput)
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validApprovedBy(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func validSelectionMetadata(provided bool, level BaselineLevel) bool {
	if !provided && level == "" {
		return true
	}
	return provided && (level == BaselineRegional || level == BaselineNational || level == BaselineReferenceCase)
}

func validEvidenceIdentifier(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
