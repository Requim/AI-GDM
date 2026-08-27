// Package loss 编排风险快照、空间暴露和版本化基线的确定性损失估算。
package loss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

// EstimateInput 是一次损失计算的显式输入；暴露量必须来自真实可追溯数据。
type EstimateInput struct {
	SnapshotID    string                `json:"snapshotId"`
	RegionCode    string                `json:"regionCode"`
	HazardType    hazarddomain.Type     `json:"hazardType"`
	IntensityBand string                `json:"intensityBand"`
	Exposures     []lossdomain.Exposure `json:"exposures"`
}

// AssessmentService 是损失评估驱动适配器使用的最小端口。
type AssessmentService interface {
	Estimate(context.Context, EstimateInput) (lossdomain.Assessment, error)
}

// Service 只依赖应用层端口，不依赖 HTTP、数据库或供应商 SDK。
type Service struct {
	risks     ports.RiskDetailReader
	analyses  ports.SpatialAnalysisReader
	baselines ports.LossBaselineReader
	clock     ports.Clock
}

var _ AssessmentService = (*Service)(nil)

// NewService 创建确定性损失评估服务。
func NewService(risks ports.RiskDetailReader, analyses ports.SpatialAnalysisReader,
	baselines ports.LossBaselineReader, clock ports.Clock) (*Service, error) {
	if risks == nil || analyses == nil || baselines == nil || clock == nil {
		return nil, fmt.Errorf("%w: 损失评估服务依赖为空", domain.ErrInvalidInput)
	}
	return &Service{risks: risks, analyses: analyses, baselines: baselines, clock: clock}, nil
}

// Estimate 读取一致风险快照并计算低、中、高直接物理损失。
func (s *Service) Estimate(ctx context.Context, input EstimateInput) (lossdomain.Assessment, error) {
	if err := validateInput(input); err != nil {
		return lossdomain.Assessment{}, err
	}
	calculatedAt := s.clock.Now().UTC()
	if calculatedAt.IsZero() {
		return lossdomain.Assessment{}, fmt.Errorf("%w: 损失评估时间为空", domain.ErrInvalidInput)
	}
	snapshot, zones, err := s.risks.RiskDetail(ctx, input.SnapshotID)
	if err != nil {
		return lossdomain.Assessment{}, fmt.Errorf("读取损失评估风险快照 %s: %w", input.SnapshotID, err)
	}
	if err = validateRisk(snapshot, zones, input); err != nil {
		return lossdomain.Assessment{}, err
	}
	analysis, err := s.analyses.LatestBySnapshot(ctx, input.SnapshotID)
	if err != nil {
		return lossdomain.Assessment{}, fmt.Errorf("读取快照 %s 空间分析: %w", input.SnapshotID, err)
	}
	if err = validateAnalysis(analysis, input.SnapshotID); err != nil {
		return lossdomain.Assessment{}, err
	}
	costs, err := s.baselines.CostBaselines(ctx, input.RegionCode)
	if err != nil {
		return lossdomain.Assessment{}, insufficient("读取成本基线", err)
	}
	vulnerabilities, err := s.baselines.Vulnerabilities(ctx, string(input.HazardType))
	if err != nil {
		return lossdomain.Assessment{}, insufficient("读取脆弱性基线", err)
	}
	return calculate(input, snapshot, zones, analysis, costs, vulnerabilities, calculatedAt)
}

func validateInput(input EstimateInput) error {
	if strings.TrimSpace(input.SnapshotID) == "" || strings.TrimSpace(input.RegionCode) == "" ||
		input.SnapshotID != strings.TrimSpace(input.SnapshotID) || input.RegionCode != strings.TrimSpace(input.RegionCode) ||
		(input.HazardType != hazarddomain.TypeLandslide && input.HazardType != hazarddomain.TypeDebrisFlow) {
		return fmt.Errorf("%w: 损失评估快照、区域或灾种无效", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(input.IntensityBand) == "" || len(input.Exposures) == 0 {
		return fmt.Errorf("%w: 损失评估缺少强度带或真实资产暴露", domain.ErrInsufficientData)
	}
	for index, exposure := range input.Exposures {
		if err := exposure.Validate(); err != nil {
			return fmt.Errorf("校验第 %d 个损失暴露: %w", index, err)
		}
		if exposure.AssetType != string(lossdomain.AssetBuilding) && exposure.AssetType != string(lossdomain.AssetRoad) && exposure.AssetType != string(lossdomain.AssetFacility) {
			return fmt.Errorf("%w: 损失暴露资产类别无效", domain.ErrInvalidInput)
		}
	}
	return nil
}

func validateRisk(snapshot hazarddomain.Snapshot, zones []hazarddomain.RiskZone, input EstimateInput) error {
	if snapshot.ID != input.SnapshotID || snapshot.HazardType != input.HazardType ||
		(snapshot.Status != hazarddomain.SnapshotAvailable && snapshot.Status != hazarddomain.SnapshotStale) {
		return insufficient("校验风险快照", domain.ErrInsufficientData)
	}
	if len(zones) == 0 {
		return insufficient("读取风险区", domain.ErrInsufficientData)
	}
	for _, zone := range zones {
		if zone.SnapshotID != input.SnapshotID || zone.ID == "" || !finite(zone.AreaSquareM) || zone.AreaSquareM < 0 {
			return insufficient("校验风险区", domain.ErrInsufficientData)
		}
	}
	return nil
}

func validateAnalysis(value spatialdomain.Analysis, snapshotID string) error {
	if value.SnapshotID != snapshotID {
		return insufficient("校验空间分析快照", domain.ErrInsufficientData)
	}
	if err := value.Validate(); err != nil {
		return insufficient("校验空间分析结果", err)
	}
	return nil
}

func calculate(input EstimateInput, snapshot hazarddomain.Snapshot, zones []hazarddomain.RiskZone,
	analysis spatialdomain.Analysis, costs []lossdomain.CostBaseline, vulnerabilities []lossdomain.Vulnerability,
	calculatedAt time.Time) (lossdomain.Assessment, error) {
	zoneIDs := make(map[string]struct{}, len(zones))
	area := 0.0
	for _, zone := range zones {
		zoneIDs[zone.ID] = struct{}{}
		area += zone.AreaSquareM
	}
	costByType := indexCosts(costs, input.RegionCode)
	var low, mid, high int64
	confidence := 1.0
	refs := []string{snapshot.Source.SourceURI}
	included := make(map[lossdomain.AssetType]struct{})
	for _, exposure := range input.Exposures {
		if _, exists := zoneIDs[exposure.ZoneID]; !exists {
			return lossdomain.Assessment{}, insufficient("匹配暴露风险区", domain.ErrInsufficientData)
		}
		assetType := lossdomain.AssetType(exposure.AssetType)
		cost, exists := costByType[assetType]
		if !exists {
			return lossdomain.Assessment{}, insufficient("匹配资产成本基线", domain.ErrNotFound)
		}
		vulnerability, matched := findVulnerability(vulnerabilities, input, assetType)
		if !matched {
			return lossdomain.Assessment{}, insufficient("匹配脆弱性基线", domain.ErrNotFound)
		}
		partLow, err := damageCents(exposure, cost.LowCents, vulnerability.ImpactFractionLow, vulnerability.DamageRatioLow)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		partMid, err := damageCents(exposure, cost.CentralCents, vulnerability.ImpactFractionMid, vulnerability.DamageRatioMid)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		partHigh, err := damageCents(exposure, cost.HighCents, vulnerability.ImpactFractionHigh, vulnerability.DamageRatioHigh)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		low, err = addCents(low, partLow)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		mid, err = addCents(mid, partMid)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		high, err = addCents(high, partHigh)
		if err != nil {
			return lossdomain.Assessment{}, err
		}
		confidence *= exposure.CoverageRatio
		included[assetType] = struct{}{}
		refs = append(refs, exposure.Source.SourceURI, cost.Source.SourceURI, vulnerability.Source.SourceURI)
	}
	confidence *= baselineQuality(costs, vulnerabilities)
	if snapshot.Status == hazarddomain.SnapshotStale {
		confidence *= 0.6
		refs = append(refs, "snapshot:stale")
	}
	if analysis.Status != spatialdomain.AnalysisAvailable {
		confidence *= 0.7
	}
	refs = uniqueStrings(refs)
	sort.Strings(refs)
	assessment := lossdomain.Assessment{ID: assessmentID(input, refs), SnapshotID: input.SnapshotID,
		HazardType: string(input.HazardType), RegionCode: input.RegionCode, FormulaVersion: lossdomain.FormulaVersion,
		ScenarioMethod: "暴露量/覆盖率 × 单位重置成本 × 影响比例 × 损伤率", ConditionalLowCents: low,
		ConditionalMidCents: mid, ConditionalHighCents: high, ImpactAreaSquareM: area, InputReferences: refs,
		IncludedAssets: sortedAssets(included), Status: lossdomain.AssessmentAvailable, Confidence: clamp(confidence),
		ConfidenceBand: confidenceBand(confidence), CalculatedAt: calculatedAt.UTC()}
	setContextMetrics(&assessment, analysis)
	if err := assessment.Validate(); err != nil {
		return lossdomain.Assessment{}, fmt.Errorf("校验损失评估结果: %w", err)
	}
	return assessment, nil
}

func indexCosts(values []lossdomain.CostBaseline, region string) map[lossdomain.AssetType]lossdomain.CostBaseline {
	result := make(map[lossdomain.AssetType]lossdomain.CostBaseline)
	for _, value := range values {
		if value.RegionCode == region {
			result[value.AssetType] = value
		}
	}
	return result
}

func findVulnerability(values []lossdomain.Vulnerability, input EstimateInput, assetType lossdomain.AssetType) (lossdomain.Vulnerability, bool) {
	for _, value := range values {
		if value.AssetType == assetType && value.HazardType == string(input.HazardType) && value.IntensityBand == input.IntensityBand && value.CalibrationRegion == input.RegionCode {
			return value, true
		}
	}
	for _, value := range values {
		if value.AssetType == assetType && value.HazardType == string(input.HazardType) && value.IntensityBand == input.IntensityBand && value.CalibrationRegion == "CN" {
			return value, true
		}
	}
	return lossdomain.Vulnerability{}, false
}

func damageCents(exposure lossdomain.Exposure, unitCents int64, impact, damage float64) (int64, error) {
	factor := exposure.Quantity / exposure.CoverageRatio * float64(unitCents) * impact * damage
	if !finite(factor) || factor < 0 || factor > float64(math.MaxInt64) {
		return 0, fmt.Errorf("%w: 损失金额超出整数范围", domain.ErrInvalidInput)
	}
	return int64(math.Round(factor)), nil
}

func addCents(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("%w: 损失金额累计溢出", domain.ErrInvalidInput)
	}
	return left + right, nil
}

func baselineQuality(costs []lossdomain.CostBaseline, vulnerabilities []lossdomain.Vulnerability) float64 {
	quality := 1.0
	for _, value := range costs {
		if value.Status == lossdomain.BaselineDemoOnly {
			quality *= 0.8
			break
		}
	}
	for _, value := range vulnerabilities {
		if value.Status == lossdomain.BaselineDemoOnly {
			quality *= 0.8
			break
		}
	}
	return quality
}

func setContextMetrics(value *lossdomain.Assessment, analysis spatialdomain.Analysis) {
	for _, zone := range analysis.Zones {
		if zone.Population.Quantity != nil {
			value.AffectedPopulation += *zone.Population.Quantity
		}
		if zone.Roads.Quantity != nil {
			value.AffectedRoadMeters += *zone.Roads.Quantity
		}
		if zone.POIs.Quantity != nil && *zone.POIs.Quantity >= 0 {
			value.AffectedFacilities += int(math.Round(*zone.POIs.Quantity))
		}
	}
}

func sortedAssets(values map[lossdomain.AssetType]struct{}) []lossdomain.AssetType {
	result := make([]lossdomain.AssetType, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func assessmentID(input EstimateInput, refs []string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(input.SnapshotID + "|" + input.RegionCode + "|" + string(input.HazardType) + "|" + input.IntensityBand + "|" + lossdomain.FormulaVersion))
	for _, value := range refs {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "loss-" + hex.EncodeToString(hash.Sum(nil))[:24]
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func confidenceBand(value float64) string {
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

func insufficient(label string, err error) error {
	if err == nil {
		err = domain.ErrInsufficientData
	}
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("%s: %w", label, errors.Join(domain.ErrInsufficientData, err))
	}
	if errors.Is(err, domain.ErrInsufficientData) {
		return fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Errorf("%s: %w", label, err)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
