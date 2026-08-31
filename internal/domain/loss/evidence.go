package loss

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

const deduplicatedAreaToleranceRatio = 0.01

const (
	// EvidenceVersion 标识损失输入证据的规范化结构版本。
	EvidenceVersion = "ai-gdm-loss-evidence-v1"
	// RiskProjectionVersion 标识无几何风险区与真实 feature 投影的摘要规则。
	RiskProjectionVersion             = "ai-gdm-loss-risk-projection-v1"
	maxEvidenceItems                  = 1000
	maxEvidenceStringBytes            = 4096
	maxEvidenceTotalItems             = 5000
	maxEvidenceTotalChars             = 512 << 10
	maxProjectionLimitationItems      = 100
	maxProjectionLimitationBytes      = 4096
	maxProjectionLimitationTotalBytes = 64 << 10
)

var evidenceTimeType = reflect.TypeOf(time.Time{})

// SnapshotEvidence 固化生成损失评估的风险快照身份和来源。
type SnapshotEvidence struct {
	ID           string                `json:"id"`
	HazardType   string                `json:"hazardType"`
	ModelName    string                `json:"modelName"`
	ModelVersion string                `json:"modelVersion"`
	Status       string                `json:"status"`
	RunAt        time.Time             `json:"runAt"`
	ValidFrom    time.Time             `json:"validFrom"`
	ValidTo      time.Time             `json:"validTo"`
	Source       provenance.Provenance `json:"source"`
}

// SpatialAnalysisEvidence 固化空间分析版本、行政区域和来源引用。
type SpatialAnalysisEvidence struct {
	ID                     string    `json:"id"`
	Version                string    `json:"version"`
	Digest                 string    `json:"digest"`
	ProjectionID           string    `json:"projectionId"`
	ProjectionVersion      string    `json:"projectionVersion"`
	ProjectionDigest       string    `json:"projectionDigest"`
	ProjectionCollectedAt  time.Time `json:"projectionCollectedAt"`
	ProjectionValidFrom    time.Time `json:"projectionValidFrom"`
	ProjectionValidTo      time.Time `json:"projectionValidTo"`
	ProjectionLimitations  []string  `json:"projectionLimitations"`
	SourceReferenceDigests []string  `json:"sourceReferenceDigests"`
	AdminBoundaryID        string    `json:"adminBoundaryId"`
	AdminBoundaryDigest    string    `json:"adminBoundaryDigest"`
	Status                 string    `json:"status"`
	RegionCode             string    `json:"regionCode"`
	TotalAreaSquareM       float64   `json:"totalAreaSquareMeters"`
	CalculatedAt           time.Time `json:"calculatedAt"`
	InputReferences        []string  `json:"inputReferences"`
	DatasetReferences      []string  `json:"datasetReferences"`
}

// PopulationEvidence 固化影响人口统计所依赖的逐风险区空间结果。
type PopulationEvidence struct {
	FeatureID       string   `json:"featureId"`
	ZoneID          string   `json:"zoneId"`
	ZoneIDs         []string `json:"zoneIds"`
	Quantity        float64  `json:"quantity"`
	Unit            string   `json:"unit"`
	CoverageRatio   float64  `json:"coverageRatio"`
	Provided        bool     `json:"provided"`
	MetricStatus    string   `json:"metricStatus"`
	InputReferences []string `json:"inputReferences"`
}

// RiskZoneEvidence 固化强度和行政区域推导使用的风险区字段。
type RiskZoneEvidence struct {
	ID               string   `json:"id"`
	Level            string   `json:"level"`
	AreaSquareMeters float64  `json:"areaSquareMeters"`
	AdminCodes       []string `json:"adminCodes"`
}

// BaselineSetEvidence 标识一次评估选用的原子基线数据集。
type BaselineSetEvidence struct {
	Provider string `json:"provider"`
	Dataset  string `json:"dataset"`
	Version  string `json:"version"`
}

// AssessmentEvidence 保存可重放损失金额所需的全部确定性输入。
type AssessmentEvidence struct {
	Version         string                  `json:"version"`
	Snapshot        SnapshotEvidence        `json:"snapshot"`
	SpatialAnalysis SpatialAnalysisEvidence `json:"spatialAnalysis"`
	BaselineSet     BaselineSetEvidence     `json:"baselineSet"`
	IntensityBand   string                  `json:"intensityBand"`
	RiskZones       []RiskZoneEvidence      `json:"riskZones"`
	Population      []PopulationEvidence    `json:"population"`
	Exposures       []Exposure              `json:"exposures"`
	Costs           []CostBaseline          `json:"costBaselines"`
	Vulnerabilities []Vulnerability         `json:"vulnerabilities"`
}

// BindAssessmentIdentity 计算输入摘要和排除执行时刻的确定性内容标识。
func BindAssessmentIdentity(value Assessment) (Assessment, error) {
	canonicalizeAssessmentIdentityTimes(&value)
	if err := value.Evidence.Validate(); err != nil {
		return Assessment{}, err
	}
	limitations := append([]string(nil), value.Limitations...)
	limitations = append(limitations, value.Evidence.SpatialAnalysis.ProjectionLimitations...)
	value.Limitations = normalizedStrings(append(limitations, requiredAssessmentLimitations(value.Status)...))
	digest, err := assessmentInputDigest(value.Evidence)
	if err != nil {
		return Assessment{}, err
	}
	value.InputDigest = digest
	value.ID = ""
	if err = validateAssessmentContent(value); err != nil {
		return Assessment{}, err
	}
	payload, err := assessmentContentBytes(value)
	if err != nil {
		return Assessment{}, err
	}
	value.ID = "loss-" + sha256Hex(payload)
	if err = value.Validate(); err != nil {
		return Assessment{}, err
	}
	return value, nil
}

func requiredAssessmentLimitations(status AssessmentStatus) []string {
	if status == AssessmentReferenceOnly {
		return []string{LimitationAdvisoryOnly, LimitationReferenceOnly,
			LimitationReferenceRoadOnly, LimitationReferenceTransfer}
	}
	return []string{LimitationDirectPhysicalLoss, LimitationAdvisoryOnly}
}

// Validate 校验损失证据的来源、规范顺序和计算输入绑定。
func (e AssessmentEvidence) Validate() error {
	if err := validateEvidenceBudget(e); err != nil {
		return err
	}
	if e.Version != EvidenceVersion || strings.TrimSpace(e.IntensityBand) == "" {
		return invalidEvidence("证据版本或强度带无效")
	}
	if err := validateSnapshotEvidence(e.Snapshot); err != nil {
		return err
	}
	if err := validateSpatialEvidence(e.SpatialAnalysis); err != nil {
		return err
	}
	if !projectionEvidenceWithinSnapshot(e.SpatialAnalysis, e.Snapshot) {
		return invalidEvidence("空间投影有效期超出风险快照或来源窗口")
	}
	if strings.TrimSpace(e.BaselineSet.Provider) == "" || strings.TrimSpace(e.BaselineSet.Dataset) == "" ||
		strings.TrimSpace(e.BaselineSet.Version) == "" {
		return invalidEvidence("基线数据集身份无效")
	}
	maximumIntensity, err := validateRiskZoneEvidence(e.RiskZones, e.SpatialAnalysis.RegionCode)
	if err != nil {
		return err
	}
	if !validEvidenceDeduplicatedArea(e.SpatialAnalysis.TotalAreaSquareM, e.RiskZones) {
		return invalidEvidence("空间投影总面积超出风险区并集边界")
	}
	if e.IntensityBand != maximumIntensity {
		return invalidEvidence("证据摘要强度与风险区最高等级不一致")
	}
	if err := validatePopulationEvidence(e.Population, e.RiskZones); err != nil {
		return err
	}
	if err := validateExposureEvidence(e.Exposures, e.SpatialAnalysis, e.RiskZones); err != nil {
		return err
	}
	if err := validateGlobalFeatureIDs(e.Population, e.Exposures); err != nil {
		return err
	}
	return validateBaselineEvidence(e)
}

type evidenceBudget struct {
	items int
	chars int
}

func validateEvidenceBudget(value AssessmentEvidence) error {
	budget := &evidenceBudget{}
	if err := walkEvidenceValue(reflect.ValueOf(value), budget, 0); err != nil {
		return err
	}
	if budget.items > maxEvidenceTotalItems || budget.chars > maxEvidenceTotalChars {
		return invalidEvidence("损失证据总项数或总字符预算超限")
	}
	return nil
}

func walkEvidenceValue(value reflect.Value, budget *evidenceBudget, depth int) error {
	if !value.IsValid() || depth > 16 {
		return nil
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return walkEvidenceValue(value.Elem(), budget, depth+1)
	}
	if value.Type() == evidenceTimeType {
		timestamp := value.Interface().(time.Time)
		if !timestamp.IsZero() && !validIdentityTime(timestamp) {
			return invalidEvidence("损失证据时间必须使用 UTC 微秒精度")
		}
		return nil
	}
	if value.Kind() == reflect.String {
		budget.chars += value.Len()
	}
	if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		budget.items += value.Len()
	}
	return walkEvidenceChildren(value, budget, depth)
}

func walkEvidenceChildren(value reflect.Value, budget *evidenceBudget, depth int) error {
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				if err := walkEvidenceValue(value.Field(index), budget, depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := walkEvidenceValue(value.Index(index), budget, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSnapshotEvidence(value SnapshotEvidence) error {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.HazardType) == "" ||
		strings.TrimSpace(value.ModelName) == "" || strings.TrimSpace(value.ModelVersion) == "" || value.Status != "available" {
		return invalidEvidence("风险快照证据无效")
	}
	if !validIdentityTime(value.RunAt) || !validIdentityTime(value.ValidFrom) ||
		!validIdentityTime(value.ValidTo) || !value.ValidTo.After(value.ValidFrom) {
		return invalidEvidence("风险快照证据时间无效")
	}
	if err := value.Source.Validate(); err != nil {
		return fmt.Errorf("风险快照证据来源: %w", err)
	}
	return nil
}

func validateSpatialEvidence(value SpatialAnalysisEvidence) error {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Version) == "" || value.RegionCode != "CN" ||
		value.Status != "available" || !validSHA256(value.Digest) || !finite(value.TotalAreaSquareM) ||
		value.TotalAreaSquareM <= 0 || !validIdentityTime(value.CalculatedAt) ||
		value.ProjectionID != "exposure-"+value.ProjectionDigest || value.ProjectionVersion != RiskProjectionVersion ||
		!validSHA256(value.ProjectionDigest) || !validIdentityTime(value.ProjectionCollectedAt) ||
		!validProjectionEvidenceWindow(value) || !strings.HasPrefix(value.AdminBoundaryID, "CHN-ADM0-") ||
		!validSHA256(value.AdminBoundaryDigest) {
		return invalidEvidence("空间分析证据无效")
	}
	if err := validateStringList("空间投影脱敏来源摘要", value.SourceReferenceDigests, true); err != nil {
		return err
	}
	for _, digest := range value.SourceReferenceDigests {
		if !validSHA256(digest) {
			return invalidEvidence("空间投影脱敏来源摘要无效")
		}
	}
	if err := validateProjectionEvidenceLimitations(value.ProjectionLimitations); err != nil {
		return err
	}
	if err := validateStringList("空间分析输入引用", value.InputReferences, true); err != nil {
		return err
	}
	return validateStringList("空间分析数据集引用", value.DatasetReferences, false)
}

func validProjectionEvidenceWindow(value SpatialAnalysisEvidence) bool {
	return validIdentityTime(value.ProjectionValidFrom) && validIdentityTime(value.ProjectionValidTo) &&
		value.ProjectionValidTo.After(value.ProjectionValidFrom) &&
		!value.ProjectionCollectedAt.Before(value.ProjectionValidFrom) &&
		value.ProjectionCollectedAt.Before(value.ProjectionValidTo)
}

func projectionEvidenceWithinSnapshot(spatial SpatialAnalysisEvidence, snapshot SnapshotEvidence) bool {
	if spatial.ProjectionValidFrom.Before(snapshot.ValidFrom) || spatial.ProjectionValidTo.After(snapshot.ValidTo) {
		return false
	}
	source := snapshot.Source
	if !source.ValidFrom.IsZero() && spatial.ProjectionValidFrom.Before(source.ValidFrom) {
		return false
	}
	return source.ValidTo.IsZero() || !spatial.ProjectionValidTo.After(source.ValidTo)
}

func validatePopulationEvidence(values []PopulationEvidence, zones []RiskZoneEvidence) error {
	if len(values) == 0 || len(values) > maxEvidenceItems {
		return invalidEvidence("人口证据数量无效")
	}
	zoneLevels, previous := riskZoneLevelMap(zones), ""
	for _, value := range values {
		if !validEvidenceIdentifier(value.FeatureID) || value.FeatureID <= previous || value.Unit != "people" ||
			!value.Provided || value.MetricStatus != "available" || !finite(value.Quantity) || value.Quantity < 0 ||
			!finite(value.CoverageRatio) || value.CoverageRatio <= 0 || value.CoverageRatio > 1 {
			return invalidEvidence("人口证据字段或状态无效")
		}
		if !validFeatureZones(value.ZoneID, value.ZoneIDs, zoneLevels) {
			return invalidEvidence("人口证据风险区绑定无效")
		}
		if err := validateStringList("人口证据输入引用", value.InputReferences, true); err != nil {
			return err
		}
		previous = value.FeatureID
	}
	return nil
}

func validateRiskZoneEvidence(values []RiskZoneEvidence, region string) (string, error) {
	if len(values) == 0 || len(values) > maxEvidenceItems {
		return "", invalidEvidence("风险区证据数量无效")
	}
	previous, maximum, maximumRank := "", "", 0
	for _, value := range values {
		rank, ok := evidenceRiskLevelRank(value.Level)
		if value.ID == "" || value.ID <= previous || !ok || !finite(value.AreaSquareMeters) || value.AreaSquareMeters <= 0 {
			return "", invalidEvidence("风险区证据未规范化")
		}
		if err := validateStringList("风险区行政代码", value.AdminCodes, true); err != nil || !containsString(value.AdminCodes, region) {
			return "", invalidEvidence("风险区证据未绑定共同辖区")
		}
		if rank > maximumRank {
			maximum, maximumRank = value.Level, rank
		}
		previous = value.ID
	}
	return maximum, nil
}

func validEvidenceDeduplicatedArea(total float64, zones []RiskZoneEvidence) bool {
	sum, largest := 0.0, 0.0
	for _, zone := range zones {
		sum += zone.AreaSquareMeters
		largest = math.Max(largest, zone.AreaSquareMeters)
	}
	lowerTolerance := math.Max(1e-6, largest*deduplicatedAreaToleranceRatio)
	upperTolerance := math.Max(1e-6, sum*deduplicatedAreaToleranceRatio)
	return total >= largest-lowerTolerance && total <= sum+upperTolerance
}

func evidenceRiskLevelRank(value string) (int, bool) {
	switch value {
	case "low":
		return 1, true
	case "moderate":
		return 2, true
	case "high":
		return 3, true
	case "very_high":
		return 4, true
	default:
		return 0, false
	}
}

func validateExposureEvidence(values []Exposure, analysis SpatialAnalysisEvidence, zones []RiskZoneEvidence) error {
	if len(values) == 0 || len(values) > maxEvidenceItems {
		return invalidEvidence("暴露证据数量无效")
	}
	levels, assets, previous := riskZoneLevelMap(zones), make(map[AssetType]struct{}, 2), ""
	for _, value := range values {
		key := string(value.AssetType) + "\x00" + value.FeatureID
		if key <= previous || value.AnalysisID != analysis.ID || value.AnalysisVersion != analysis.Version ||
			!validFeatureZones(value.ZoneID, value.ZoneIDs, levels) {
			return invalidEvidence("暴露证据未规范化或分析绑定无效")
		}
		if err := value.Validate(); err != nil {
			return err
		}
		intensity, ok := maximumEvidenceIntensity(value.ZoneIDs, levels)
		if !ok || value.IntensityBand != intensity {
			return invalidEvidence("暴露证据风险等级绑定无效")
		}
		assets[value.AssetType] = struct{}{}
		previous = key
	}
	if len(assets) != 2 {
		return invalidEvidence("暴露证据缺少道路或设施真实零值分项")
	}
	return nil
}

func validateGlobalFeatureIDs(population []PopulationEvidence, exposures []Exposure) error {
	seen := make(map[string]struct{}, len(population)+len(exposures))
	for _, value := range population {
		seen[value.FeatureID] = struct{}{}
	}
	for _, value := range exposures {
		if _, exists := seen[value.FeatureID]; exists {
			return invalidEvidence("全局 featureId 重复")
		}
		seen[value.FeatureID] = struct{}{}
	}
	return nil
}

func riskZoneLevelMap(values []RiskZoneEvidence) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[value.ID] = value.Level
	}
	return result
}

func validFeatureZones(canonical string, zoneIDs []string, levels map[string]string) bool {
	if err := validateStringList("暴露风险区", zoneIDs, true); err != nil || canonical != zoneIDs[0] {
		return false
	}
	for _, zoneID := range zoneIDs {
		if _, exists := levels[zoneID]; !exists {
			return false
		}
	}
	return true
}

func maximumEvidenceIntensity(zoneIDs []string, levels map[string]string) (string, bool) {
	maximum, maximumRank := "", 0
	for _, zoneID := range zoneIDs {
		rank, ok := evidenceRiskLevelRank(levels[zoneID])
		if !ok {
			return "", false
		}
		if rank > maximumRank {
			maximum, maximumRank = levels[zoneID], rank
		}
	}
	return maximum, maximumRank > 0
}

func validateBaselineEvidence(e AssessmentEvidence) error {
	if len(e.Costs) == 0 || len(e.Costs) > len(e.Exposures) || len(e.Vulnerabilities) == 0 || len(e.Vulnerabilities) > len(e.Exposures) {
		return invalidEvidence("损失基线证据数量无效")
	}
	status, err := baselineEvidenceStatus(e.Costs, e.Vulnerabilities)
	if err != nil {
		return err
	}
	if err = validateCostEvidence(e.Costs, e.SpatialAnalysis.RegionCode, status); err != nil {
		return err
	}
	if err = validateVulnerabilityEvidence(e.Vulnerabilities, e, status); err != nil {
		return err
	}
	if err := validateBaselineSetSources(e); err != nil {
		return err
	}
	costs, vulnerabilities := mapBaselines(e.Costs, e.Vulnerabilities)
	assets, pairs := requiredBaselineKeys(e.Exposures)
	if status == BaselineDemoOnly {
		assets, pairs = referenceBaselineKeys(e.Exposures, costs)
	}
	if len(costs) != len(e.Costs) || len(costs) != len(assets) ||
		len(vulnerabilities) != len(e.Vulnerabilities) || len(vulnerabilities) != len(pairs) {
		return invalidEvidence("基线证据与实际计算分项不一致")
	}
	for _, exposure := range e.Exposures {
		cost, costOK := costs[exposure.AssetType]
		if status == BaselineDemoOnly && !costOK {
			continue
		}
		vulnerability, vulnerabilityOK := vulnerabilities[vulnerabilityKey(exposure.AssetType, exposure.IntensityBand)]
		if !costOK || !vulnerabilityOK || cost.Unit != exposure.Unit ||
			!baselineRegionMatches(cost.BaselineLevel, cost.RegionCode, e.SpatialAnalysis.RegionCode) {
			return invalidEvidence("暴露与成本基线未完整绑定")
		}
		if vulnerability.HazardType != e.Snapshot.HazardType || vulnerability.IntensityBand != exposure.IntensityBand {
			return invalidEvidence("暴露与脆弱性基线未完整绑定")
		}
	}
	return nil
}

func baselineEvidenceStatus(costs []CostBaseline, vulnerabilities []Vulnerability) (BaselineStatus, error) {
	status := costs[0].Status
	if status != BaselineApproved && status != BaselineDemoOnly {
		return "", invalidEvidence("损失基线状态无效")
	}
	for _, value := range costs {
		if value.Status != status {
			return "", invalidEvidence("成本基线状态混用")
		}
	}
	for _, value := range vulnerabilities {
		if value.Status != status {
			return "", invalidEvidence("成本与脆弱性基线状态混用")
		}
	}
	return status, nil
}

func referenceBaselineKeys(values []Exposure, costs map[AssetType]CostBaseline) (map[AssetType]struct{}, map[string]struct{}) {
	assets := make(map[AssetType]struct{}, len(costs))
	pairs := make(map[string]struct{})
	for asset := range costs {
		assets[asset] = struct{}{}
	}
	for _, value := range values {
		if _, selected := assets[value.AssetType]; selected {
			pairs[vulnerabilityKey(value.AssetType, value.IntensityBand)] = struct{}{}
		}
	}
	return assets, pairs
}

func requiredBaselineKeys(values []Exposure) (map[AssetType]struct{}, map[string]struct{}) {
	assets := make(map[AssetType]struct{}, len(values))
	pairs := make(map[string]struct{}, len(values))
	for _, value := range values {
		assets[value.AssetType] = struct{}{}
		pairs[vulnerabilityKey(value.AssetType, value.IntensityBand)] = struct{}{}
	}
	return assets, pairs
}

func validateBaselineSetSources(e AssessmentEvidence) error {
	validate := func(source provenance.Provenance) bool {
		return source.Provider == e.BaselineSet.Provider && source.Dataset == e.BaselineSet.Dataset &&
			source.DatasetVersion == e.BaselineSet.Version
	}
	for _, value := range e.Costs {
		if !validate(value.Source) {
			return invalidEvidence("成本基线不属于同一数据集")
		}
	}
	for _, value := range e.Vulnerabilities {
		if !validate(value.Source) {
			return invalidEvidence("脆弱性基线不属于同一数据集")
		}
	}
	return nil
}

func validateCostEvidence(values []CostBaseline, region string, status BaselineStatus) error {
	previous, previousAsset := "", AssetType("")
	for _, value := range values {
		key := string(value.AssetType) + "\x00" + value.ID
		if key <= previous || value.AssetType == previousAsset || !value.Provided || value.Status != status ||
			!baselineRegionMatches(value.BaselineLevel, value.RegionCode, region) {
			return invalidEvidence("成本基线证据未规范化或状态不一致")
		}
		if status == BaselineDemoOnly && (value.AssetType != AssetRoad || value.BaselineLevel != BaselineReferenceCase) {
			return invalidEvidence("研究参考成本基线只能覆盖道路")
		}
		if err := value.Validate(); err != nil {
			return err
		}
		previous, previousAsset = key, value.AssetType
	}
	return nil
}

func validateVulnerabilityEvidence(values []Vulnerability, evidence AssessmentEvidence, status BaselineStatus) error {
	previous := ""
	for _, value := range values {
		key := vulnerabilityKey(value.AssetType, value.IntensityBand) + "\x00" + value.ID
		validRegion := baselineRegionMatches(value.BaselineLevel, value.CalibrationRegion, evidence.SpatialAnalysis.RegionCode)
		if key <= previous || !value.Provided || !validRegion || value.Status != status {
			return invalidEvidence("脆弱性基线证据未规范化或状态不一致")
		}
		if status == BaselineDemoOnly && (value.AssetType != AssetRoad || value.BaselineLevel != BaselineReferenceCase) {
			return invalidEvidence("研究参考脆弱性基线只能覆盖道路")
		}
		if err := value.Validate(); err != nil {
			return err
		}
		previous = key
	}
	return nil
}

func mapBaselines(costs []CostBaseline, vulnerabilities []Vulnerability) (map[AssetType]CostBaseline, map[string]Vulnerability) {
	costMap := make(map[AssetType]CostBaseline, len(costs))
	for _, value := range costs {
		costMap[value.AssetType] = value
	}
	vulnerabilityMap := make(map[string]Vulnerability, len(vulnerabilities))
	for _, value := range vulnerabilities {
		vulnerabilityMap[vulnerabilityKey(value.AssetType, value.IntensityBand)] = value
	}
	return costMap, vulnerabilityMap
}

func vulnerabilityKey(asset AssetType, intensity string) string {
	return string(asset) + "\x00" + intensity
}

func baselineRegionMatches(level BaselineLevel, actual, region string) bool {
	if level == BaselineReferenceCase {
		return strings.TrimSpace(actual) != ""
	}
	if level == BaselineRegional {
		return actual == region
	}
	return level == BaselineNational && actual == "CN"
}

func validateAssessmentIdentity(value Assessment) error {
	if err := validateAssessmentBindings(value); err != nil {
		return err
	}
	digest, err := assessmentInputDigest(value.Evidence)
	if err != nil || digest != value.InputDigest {
		return invalidEvidence("损失评估输入摘要不一致")
	}
	payload, err := assessmentContentBytes(value)
	if err != nil {
		return err
	}
	if value.ID != "loss-"+sha256Hex(payload) {
		return invalidEvidence("损失评估标识与完整内容不一致")
	}
	return nil
}

func validateAssessmentBindings(value Assessment) error {
	evidence := value.Evidence
	if value.SnapshotID != evidence.Snapshot.ID || value.HazardType != evidence.Snapshot.HazardType ||
		value.RegionCode != evidence.SpatialAnalysis.RegionCode || !sameFloat(value.ImpactAreaSquareM, evidence.SpatialAnalysis.TotalAreaSquareM) {
		return invalidEvidence("损失评估与权威输入身份不一致")
	}
	population, roads, facilities := evidenceContextMetrics(evidence)
	if !sameFloat(value.AffectedPopulation, population) || !sameFloat(value.AffectedRoadMeters, roads) || value.AffectedFacilities != facilities {
		return invalidEvidence("损失评估影响范围与空间证据不一致")
	}
	wantAssets := evidenceAssets(evidence.Exposures)
	if value.Status == AssessmentReferenceOnly {
		wantAssets = evidenceCostAssets(evidence.Costs)
	}
	if !sameStrings(value.InputReferences, EvidenceReferences(evidence)) || !sameAssets(value.IncludedAssets, wantAssets) {
		return invalidEvidence("损失评估来源或资产类别与证据不一致")
	}
	if !assessmentMatchesBaselineMode(value.Status, evidence.Costs, evidence.Vulnerabilities) {
		return invalidEvidence("损失评估状态与基线状态不一致")
	}
	if value.CalculatedAt.Before(latestEvidenceAuthorityTime(evidence)) {
		return invalidEvidence("损失评估时间早于权威输入时间")
	}
	for _, limitation := range evidence.SpatialAnalysis.ProjectionLimitations {
		if !containsString(value.Limitations, limitation) {
			return invalidEvidence("损失评估未公开空间投影限制")
		}
	}
	if len(evidence.SpatialAnalysis.ProjectionLimitations) > 0 &&
		(value.Confidence >= 0.8 || value.ConfidenceBand == "high") {
		return invalidEvidence("存在空间投影限制时置信度未下调")
	}
	return nil
}

func assessmentMatchesBaselineMode(status AssessmentStatus, costs []CostBaseline, vulnerabilities []Vulnerability) bool {
	baselineStatus, err := baselineEvidenceStatus(costs, vulnerabilities)
	if err != nil {
		return false
	}
	if status == AssessmentReferenceOnly {
		return baselineStatus == BaselineDemoOnly
	}
	return (status == AssessmentAvailable || status == AssessmentInsufficientData) &&
		baselineStatus == BaselineApproved
}

func latestEvidenceAuthorityTime(evidence AssessmentEvidence) time.Time {
	result := evidence.SpatialAnalysis.CalculatedAt
	values := []time.Time{evidence.SpatialAnalysis.ProjectionCollectedAt, evidence.Snapshot.RunAt,
		provenance.LatestAuthorityTime(evidence.Snapshot.Source)}
	for _, value := range evidence.Costs {
		values = append(values, provenance.LatestAuthorityTime(value.Source), value.PriceBaseDate)
	}
	for _, value := range evidence.Vulnerabilities {
		values = append(values, provenance.LatestAuthorityTime(value.Source))
	}
	for _, value := range values {
		if value.After(result) {
			result = value
		}
	}
	return result
}

// EvidenceReferences 返回规范化、去重且排序的损失证据来源引用。
func EvidenceReferences(evidence AssessmentEvidence) []string {
	values := provenanceReferences(evidence.Snapshot.Source)
	values = append(values, evidence.SpatialAnalysis.InputReferences...)
	values = append(values, evidence.SpatialAnalysis.DatasetReferences...)
	for _, value := range evidence.Population {
		values = append(values, value.InputReferences...)
	}
	for _, value := range evidence.Exposures {
		values = append(values, value.InputReferences...)
	}
	for _, value := range evidence.Costs {
		values = append(values, provenanceReferences(value.Source)...)
	}
	for _, value := range evidence.Vulnerabilities {
		values = append(values, provenanceReferences(value.Source)...)
	}
	return normalizedStrings(values)
}

func provenanceReferences(value provenance.Provenance) []string {
	result := []string{value.SourceURI}
	for _, part := range value.SourceParts {
		result = append(result, part.Reference)
	}
	return result
}

func evidenceContextMetrics(evidence AssessmentEvidence) (float64, float64, int) {
	population, roads := 0.0, 0.0
	facilities := 0
	for _, value := range evidence.Population {
		population += value.Quantity
	}
	for _, value := range evidence.Exposures {
		switch value.AssetType {
		case AssetRoad:
			roads += value.Quantity
		case AssetFacility:
			facilities += int(math.Round(value.Quantity))
		}
	}
	return population, roads, facilities
}

func evidenceAssets(values []Exposure) []AssetType {
	seen := make(map[AssetType]struct{}, len(values))
	for _, value := range values {
		seen[value.AssetType] = struct{}{}
	}
	result := make([]AssetType, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func evidenceCostAssets(values []CostBaseline) []AssetType {
	result := make([]AssetType, len(values))
	for index, value := range values {
		result[index] = value.AssetType
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
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

func sameAssets(left, right []AssetType) bool {
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

func sameFloat(left, right float64) bool {
	return math.Abs(left-right) <= math.Max(1e-9, math.Max(math.Abs(left), math.Abs(right))*1e-12)
}

func validateAssessmentContent(value Assessment) error {
	copy := value
	copy.ID = "pending"
	if err := validateAssessmentCore(copy); err != nil {
		return err
	}
	if err := copy.Evidence.Validate(); err != nil {
		return err
	}
	return validateAssessmentBindings(copy)
}

func assessmentInputDigest(evidence AssessmentEvidence) (string, error) {
	payload, err := json.Marshal(struct {
		FormulaVersion string             `json:"formulaVersion"`
		Evidence       AssessmentEvidence `json:"evidence"`
	}{FormulaVersion: FormulaVersion, Evidence: evidence})
	if err != nil {
		return "", fmt.Errorf("编码损失评估输入证据: %w", err)
	}
	return sha256Hex(payload), nil
}

func assessmentContentBytes(value Assessment) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("编码损失评估完整内容: %w", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("规范化损失评估完整内容: %w", err)
	}
	delete(fields, "id")
	delete(fields, "calculatedAt")
	return json.Marshal(fields)
}

func validateStringList(name string, values []string, required bool) error {
	if (required && len(values) == 0) || len(values) > maxEvidenceItems {
		return invalidEvidence("%s数量无效", name)
	}
	for index, value := range values {
		if value == "" || len(value) > maxEvidenceStringBytes || strings.TrimSpace(value) != value || (index > 0 && value <= values[index-1]) {
			return invalidEvidence("%s必须有界、排序且不能重复", name)
		}
	}
	return nil
}

func validateProjectionEvidenceLimitations(values []string) error {
	if values == nil || len(values) > maxProjectionLimitationItems {
		return invalidEvidence("空间投影限制数量无效")
	}
	total := 0
	for index, value := range values {
		if value == "" || len(value) > maxProjectionLimitationBytes ||
			strings.TrimSpace(value) != value || (index > 0 && value <= values[index-1]) {
			return invalidEvidence("空间投影限制必须有界、排序且不能重复")
		}
		total += len(value)
		if total > maxProjectionLimitationTotalBytes {
			return invalidEvidence("空间投影限制总字符预算超限")
		}
	}
	return nil
}

func canonicalizeAssessmentIdentityTimes(value *Assessment) {
	value.CalculatedAt = canonicalIdentityTime(value.CalculatedAt)
	value.Evidence.Snapshot.RunAt = canonicalIdentityTime(value.Evidence.Snapshot.RunAt)
	value.Evidence.Snapshot.ValidFrom = canonicalIdentityTime(value.Evidence.Snapshot.ValidFrom)
	value.Evidence.Snapshot.ValidTo = canonicalIdentityTime(value.Evidence.Snapshot.ValidTo)
	canonicalizeIdentityProvenance(&value.Evidence.Snapshot.Source)
	spatial := &value.Evidence.SpatialAnalysis
	spatial.CalculatedAt = canonicalIdentityTime(spatial.CalculatedAt)
	spatial.ProjectionCollectedAt = canonicalIdentityTime(spatial.ProjectionCollectedAt)
	spatial.ProjectionValidFrom = canonicalIdentityTime(spatial.ProjectionValidFrom)
	spatial.ProjectionValidTo = canonicalIdentityTime(spatial.ProjectionValidTo)
	for index := range value.Evidence.Costs {
		value.Evidence.Costs[index].PriceBaseDate = canonicalIdentityTime(value.Evidence.Costs[index].PriceBaseDate)
		canonicalizeIdentityProvenance(&value.Evidence.Costs[index].Source)
	}
	for index := range value.Evidence.Vulnerabilities {
		canonicalizeIdentityProvenance(&value.Evidence.Vulnerabilities[index].Source)
	}
}

func canonicalizeIdentityProvenance(value *provenance.Provenance) {
	value.ObservedAt = canonicalIdentityTime(value.ObservedAt)
	value.PublishedAt = canonicalIdentityTime(value.PublishedAt)
	value.RevisionFirstSeenAt = canonicalIdentityTime(value.RevisionFirstSeenAt)
	value.FetchedAt = canonicalIdentityTime(value.FetchedAt)
	value.ValidFrom = canonicalIdentityTime(value.ValidFrom)
	value.ValidTo = canonicalIdentityTime(value.ValidTo)
}

func canonicalIdentityTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func validIdentityTime(value time.Time) bool {
	return validUTC(value) && value.Nanosecond()%1_000 == 0
}

func containsString(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func validUTC(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func invalidEvidence(format string, values ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidInput, fmt.Sprintf(format, values...))
}
