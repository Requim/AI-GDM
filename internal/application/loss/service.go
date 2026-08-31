// Package loss 编排风险快照、空间暴露和版本化基线的确定性损失估算。
package loss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	maxLossZones                          = 500
	maxLossGeometryPointsPerZone          = 10_000
	maxLossGeometryBytesPerZone           = 1 << 20
	maxLossTotalGeometryPoints            = 500_000
	maxLossTotalGeometryBytes             = 32 << 20
	maxLossSpatialJSONBytes               = 16 << 20
	maxLossFeatures                       = 1_000
	maxLossReferences                     = 2_000
	maxLossProjectionBytes                = 1 << 20
	maxLossReferenceBytes                 = 4096
	maxLossProjectionLimitations          = 100
	maxLossProjectionLimitationBytes      = 4096
	maxLossProjectionLimitationTotalBytes = 64 << 10
	maxLimitedProjectionConfidence        = 0.79
	maxReferenceConfidence                = 0.49
	maxStaleReferenceConfidence           = lossdomain.MaxStaleReferenceConfidence
	deduplicatedAreaToleranceRatio        = 0.01
)

// MaxReferenceProjectionStaleness 限制最后成功风险与暴露投影的降级使用窗口。
const MaxReferenceProjectionStaleness = 72 * time.Hour

// EstimateInput 只接受已持久化风险快照标识，禁止调用方提交计算数值。
type EstimateInput struct {
	SnapshotID string `json:"snapshotId"`
}

// AssessmentService 是损失评估驱动适配器使用的最小端口。
type AssessmentService interface {
	Estimate(context.Context, EstimateInput) (lossdomain.Assessment, error)
}

// LossRiskZone 是损失用例所需的无几何风险区投影。
type LossRiskZone struct {
	ID             string
	SnapshotID     string
	Level          hazarddomain.RiskLevel
	AreaSquareM    float64
	AreaCalculated bool
	AdminCodes     []string
}

// LossFeatureKind 标识全局去重暴露特征的计量类别。
type LossFeatureKind string

const (
	LossFeaturePopulation LossFeatureKind = "population"
	LossFeatureRoad       LossFeatureKind = "road"
	LossFeatureFacility   LossFeatureKind = "facility"
)

// LossExposureFeature 是空间层按 featureId 全局去重后的唯一暴露记录。
type LossExposureFeature struct {
	FeatureID       string
	Kind            LossFeatureKind
	ZoneIDs         []string
	Quantity        float64
	Unit            string
	CoverageRatio   float64
	Status          spatialdomain.MetricStatus
	Provided        bool
	InputReferences []string
}

// LossSpatialProjection 保存与风险摘要同一事务读取的去重空间输入。
type LossSpatialProjection struct {
	ID                     string
	Version                string
	Digest                 string
	ProjectionID           string
	ProjectionVersion      string
	ProjectionDigest       string
	ProjectionCollectedAt  time.Time
	ProjectionValidFrom    time.Time
	ProjectionValidTo      time.Time
	ProjectionLimitations  []string
	SourceReferenceDigests []string
	AdminBoundaryID        string
	AdminBoundaryDigest    string
	AdminBoundaryReference string
	SnapshotID             string
	Status                 spatialdomain.AnalysisStatus
	RegionCode             string
	TotalAreaSquareMeters  float64
	CalculatedAt           time.Time
	InputReferences        []string
	DatasetReferences      []string
	Features               []LossExposureFeature
}

// LossInputProjection 是损失用例的单事务权威输入。
type LossInputProjection struct {
	Snapshot hazarddomain.Snapshot
	Zones    []LossRiskZone
	Analysis LossSpatialProjection
	Stats    RiskProjectionStats
}

// RiskProjectionLimits 要求仓储在 SQL 计数和聚合后、物化行之前 fail-closed。
type RiskProjectionLimits struct {
	MaxZones                          int
	MaxGeometryPointsPerZone          int64
	MaxGeometryBytesPerZone           int64
	MaxTotalGeometryPoints            int64
	MaxTotalGeometryBytes             int64
	MaxSpatialJSONBytes               int64
	MaxFeatures                       int
	MaxReferences                     int
	MaxUniqueReferences               int
	MaxProjectionBytes                int64
	MaxProjectionLimitations          int
	MaxProjectionLimitationBytes      int64
	MaxProjectionLimitationTotalBytes int64
}

// RiskProjectionStats 返回仓储前置边界查询的聚合结果。
type RiskProjectionStats struct {
	ZoneCount                    int
	MaxGeometryPoints            int64
	MaxGeometryBytes             int64
	TotalGeometryPoints          int64
	TotalGeometryBytes           int64
	SpatialJSONBytes             int64
	FeatureCount                 int
	ReferenceCount               int
	UniqueReferenceCount         int
	ProjectionBytes              int64
	AnalysisID                   string
	AnalysisDigest               string
	ProjectionID                 string
	ProjectionVersion            string
	ProjectionDigest             string
	ProjectionCollectedAt        time.Time
	ProjectionValidFrom          time.Time
	ProjectionValidTo            time.Time
	ProjectionLimitationCount    int
	MaxProjectionLimitationBytes int64
	ProjectionLimitationBytes    int64
}

// LossInputProjectionReader 在同一可重复读事务中返回无几何、全局去重的损失输入。
type LossInputProjectionReader interface {
	ReadLossInput(context.Context, string, time.Time, RiskProjectionLimits) (LossInputProjection, error)
}

// CostBaselineRequirement 描述一次评估实际需要的资产与计量单位。
type CostBaselineRequirement struct {
	AssetType lossdomain.AssetType
	Unit      string
}

// VulnerabilityBaselineRequirement 描述一次评估实际需要的资产与风险强度。
type VulnerabilityBaselineRequirement struct {
	AssetType     lossdomain.AssetType
	IntensityBand string
}

// BaselineRequirements 只包含服务端权威暴露投影派生出的基线语义键。
type BaselineRequirements struct {
	Costs           []CostBaselineRequirement
	Vulnerabilities []VulnerabilityBaselineRequirement
}

// Validate 校验基线需求有界、唯一且可由损失模型解释。
func (r BaselineRequirements) Validate() error {
	if len(r.Costs) == 0 || len(r.Costs) > 3 || len(r.Vulnerabilities) == 0 || len(r.Vulnerabilities) > 12 {
		return fmt.Errorf("%w: 损失基线需求数量无效", domain.ErrInvalidInput)
	}
	seenCosts := make(map[string]struct{}, len(r.Costs))
	for _, value := range r.Costs {
		key := string(value.AssetType) + "\x00" + value.Unit
		if !validRequiredAsset(value.AssetType) || !validRequiredText(value.Unit) || !addRequirementKey(seenCosts, key) {
			return fmt.Errorf("%w: 成本基线需求无效或重复", domain.ErrInvalidInput)
		}
	}
	seenVulnerabilities := make(map[string]struct{}, len(r.Vulnerabilities))
	for _, value := range r.Vulnerabilities {
		key := vulnerabilityMapKey(value.AssetType, value.IntensityBand)
		if !validRequiredAsset(value.AssetType) || !validRequiredIntensity(value.IntensityBand) ||
			!addRequirementKey(seenVulnerabilities, key) {
			return fmt.Errorf("%w: 脆弱性基线需求无效或重复", domain.ErrInvalidInput)
		}
	}
	return nil
}

// BaselineQuery 将权威区域、灾种、语义键和统一评估时间绑定为一次读取。
type BaselineQuery struct {
	RegionCode    string
	HazardType    string
	Requirements  BaselineRequirements
	At            time.Time
	ReferenceOnly bool
}

// BaselineSetReader 在一次一致性读取中返回完整损失基线集合。
type BaselineSetReader interface {
	BaselineSet(context.Context, BaselineQuery) (lossdomain.BaselineSet, error)
}

// Service 只依赖应用层端口，不依赖 HTTP、数据库或供应商 SDK。
type Service struct {
	inputs    LossInputProjectionReader
	baselines BaselineSetReader
	clock     ports.Clock
}

var _ AssessmentService = (*Service)(nil)

type authoritativeInput struct {
	snapshot      hazarddomain.Snapshot
	analysis      LossSpatialProjection
	stale         bool
	regionCode    string
	intensityBand string
	riskZones     []lossdomain.RiskZoneEvidence
	population    []lossdomain.PopulationEvidence
	exposures     []lossdomain.Exposure
}

type calculationPlan struct {
	input              authoritativeInput
	monetizedExposures []lossdomain.Exposure
	costs              []lossdomain.CostBaseline
	vulnerabilities    []lossdomain.Vulnerability
	baselineSet        lossdomain.BaselineSetEvidence
	referenceOnly      bool
}

// NewService 创建确定性损失评估服务。
func NewService(inputs LossInputProjectionReader, baselines BaselineSetReader, clock ports.Clock) (*Service, error) {
	if inputs == nil || baselines == nil || clock == nil {
		return nil, fmt.Errorf("%w: 损失评估服务依赖为空", domain.ErrInvalidInput)
	}
	return &Service{inputs: inputs, baselines: baselines, clock: clock}, nil
}

// Estimate 从服务端持久化数据派生全部输入并生成可重放评估。
func (s *Service) Estimate(ctx context.Context, input EstimateInput) (lossdomain.Assessment, error) {
	if err := validateInput(input); err != nil {
		return lossdomain.Assessment{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return lossdomain.Assessment{}, fmt.Errorf("%w: 损失评估时间为空", domain.ErrInvalidInput)
	}
	limits := DefaultRiskProjectionLimits()
	projection, err := s.inputs.ReadLossInput(ctx, input.SnapshotID, now, limits)
	if err != nil {
		return lossdomain.Assessment{}, projectionReadError(err)
	}
	if err = validateProjectionStats(projection, limits); err != nil {
		return lossdomain.Assessment{}, insufficient("校验有界风险区读取", err)
	}
	if err = ValidateRiskProjectionIdentity(projection); err != nil {
		return lossdomain.Assessment{}, insufficient("校验损失输入投影摘要", err)
	}
	riskStale, err := validateRiskForEstimate(projection.Snapshot, projection.Zones, input.SnapshotID, now)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	projectionStale, err := validateAuthoritativeProjection(projection.Analysis, projection.Snapshot, projection.Zones, now)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	derived, err := deriveAuthoritativeInput(projection)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	derived.stale = riskStale || projectionStale
	return s.estimateDerived(ctx, derived, now)
}

func (s *Service) estimateDerived(ctx context.Context, input authoritativeInput, now time.Time) (lossdomain.Assessment, error) {
	requirements, err := deriveBaselineRequirements(input.exposures)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	query := BaselineQuery{RegionCode: input.regionCode, HazardType: string(input.snapshot.HazardType),
		Requirements: requirements, At: now, ReferenceOnly: input.stale}
	set, err := s.baselines.BaselineSet(ctx, query)
	if err != nil {
		return lossdomain.Assessment{}, insufficient("读取一致损失基线", err)
	}
	if err = set.Validate(); err != nil {
		return lossdomain.Assessment{}, insufficient("校验一致损失基线", err)
	}
	plan, err := preparePlan(input, set, now)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	value, err := calculate(plan, calculationTime(plan))
	if err != nil {
		return lossdomain.Assessment{}, insufficient("计算并绑定损失评估", err)
	}
	return value, nil
}

func validateInput(input EstimateInput) error {
	if strings.TrimSpace(input.SnapshotID) == "" || input.SnapshotID != strings.TrimSpace(input.SnapshotID) || len(input.SnapshotID) > 128 {
		return fmt.Errorf("%w: 损失评估快照标识无效", domain.ErrInvalidInput)
	}
	return nil
}

func lossRiskProjectionLimits() RiskProjectionLimits {
	return RiskProjectionLimits{MaxZones: maxLossZones, MaxGeometryPointsPerZone: maxLossGeometryPointsPerZone,
		MaxGeometryBytesPerZone: maxLossGeometryBytesPerZone, MaxTotalGeometryPoints: maxLossTotalGeometryPoints,
		MaxTotalGeometryBytes: maxLossTotalGeometryBytes, MaxSpatialJSONBytes: maxLossSpatialJSONBytes,
		MaxFeatures: maxLossFeatures, MaxReferences: maxLossReferences, MaxUniqueReferences: 1_000,
		MaxProjectionBytes:                maxLossProjectionBytes,
		MaxProjectionLimitations:          maxLossProjectionLimitations,
		MaxProjectionLimitationBytes:      maxLossProjectionLimitationBytes,
		MaxProjectionLimitationTotalBytes: maxLossProjectionLimitationTotalBytes}
}

// DefaultRiskProjectionLimits 返回生产损失评估使用的有界读取限制。
func DefaultRiskProjectionLimits() RiskProjectionLimits {
	return lossRiskProjectionLimits()
}

func validateProjectionStats(value LossInputProjection, limits RiskProjectionLimits) error {
	stats := value.Stats
	if stats.ZoneCount != len(value.Zones) || stats.ZoneCount <= 0 || stats.ZoneCount > limits.MaxZones {
		return domain.ErrInsufficientData
	}
	if stats.MaxGeometryPoints <= 0 || stats.MaxGeometryPoints > limits.MaxGeometryPointsPerZone ||
		stats.MaxGeometryBytes <= 0 || stats.MaxGeometryBytes > limits.MaxGeometryBytesPerZone {
		return domain.ErrInsufficientData
	}
	if stats.TotalGeometryPoints < stats.MaxGeometryPoints || stats.TotalGeometryPoints > limits.MaxTotalGeometryPoints ||
		stats.TotalGeometryBytes < stats.MaxGeometryBytes || stats.TotalGeometryBytes > limits.MaxTotalGeometryBytes ||
		stats.SpatialJSONBytes <= 0 || stats.SpatialJSONBytes > limits.MaxSpatialJSONBytes {
		return domain.ErrInsufficientData
	}
	if stats.FeatureCount != len(value.Analysis.Features) || stats.FeatureCount <= 0 || stats.FeatureCount > limits.MaxFeatures ||
		stats.ReferenceCount != projectionReferenceCount(value) || stats.ReferenceCount <= 0 ||
		stats.ReferenceCount > limits.MaxReferences || stats.UniqueReferenceCount != projectionUniqueReferenceCount(value) ||
		stats.UniqueReferenceCount <= 0 || stats.UniqueReferenceCount > limits.MaxUniqueReferences ||
		stats.ProjectionBytes <= 0 || stats.ProjectionBytes > limits.MaxProjectionBytes {
		return domain.ErrInsufficientData
	}
	if stats.AnalysisID != value.Analysis.ID || stats.AnalysisDigest != value.Analysis.Digest ||
		stats.ProjectionID != value.Analysis.ProjectionID ||
		stats.ProjectionVersion != value.Analysis.ProjectionVersion ||
		stats.ProjectionDigest != value.Analysis.ProjectionDigest ||
		!stats.ProjectionCollectedAt.Equal(value.Analysis.ProjectionCollectedAt) ||
		!stats.ProjectionValidFrom.Equal(value.Analysis.ProjectionValidFrom) ||
		!stats.ProjectionValidTo.Equal(value.Analysis.ProjectionValidTo) {
		return domain.ErrInsufficientData
	}
	count, maximum, total := projectionLimitationStats(value.Analysis.ProjectionLimitations)
	if stats.ProjectionLimitationCount != count || stats.MaxProjectionLimitationBytes != maximum ||
		stats.ProjectionLimitationBytes != total || count > limits.MaxProjectionLimitations ||
		maximum > limits.MaxProjectionLimitationBytes || total > limits.MaxProjectionLimitationTotalBytes {
		return domain.ErrInsufficientData
	}
	return nil
}

func projectionLimitationStats(values []string) (int, int64, int64) {
	var maximum, total int64
	for _, value := range values {
		size := int64(len(value))
		if size > maximum {
			maximum = size
		}
		total += size
	}
	return len(values), maximum, total
}

func projectionReferenceCount(value LossInputProjection) int {
	return len(projectionReferences(value))
}

func projectionUniqueReferenceCount(value LossInputProjection) int {
	seen := make(map[string]struct{}, projectionReferenceCount(value))
	for _, reference := range projectionReferences(value) {
		seen[reference] = struct{}{}
	}
	return len(seen)
}

func projectionReferences(value LossInputProjection) []string {
	result := []string{value.Snapshot.RasterReference, value.Snapshot.Source.SourceURI,
		value.Analysis.AdminBoundaryReference}
	for _, part := range value.Snapshot.Source.SourceParts {
		result = append(result, part.Reference)
	}
	result = append(result, value.Analysis.InputReferences...)
	result = append(result, value.Analysis.DatasetReferences...)
	for _, feature := range value.Analysis.Features {
		result = append(result, feature.InputReferences...)
	}
	return result
}

func validateRiskForEstimate(snapshot hazarddomain.Snapshot, zones []LossRiskZone, snapshotID string,
	now time.Time,
) (bool, error) {
	if snapshot.ID != snapshotID || !supportedHazard(snapshot.HazardType) || snapshot.Status != hazarddomain.SnapshotAvailable ||
		strings.TrimSpace(snapshot.ModelName) == "" || strings.TrimSpace(snapshot.ModelVersion) == "" {
		return false, insufficient("校验风险快照", domain.ErrInvalidInput)
	}
	stale, ok := snapshotWindowState(snapshot, now)
	if !ok {
		return false, insufficient("校验风险快照有效期", domain.ErrInsufficientData)
	}
	sourceStale, err := riskSourceState(snapshot.Source, now)
	if err != nil {
		return false, insufficient("校验风险快照来源", err)
	}
	if snapshot.ValidFrom.Before(snapshot.Source.ValidFrom) || snapshot.ValidTo.After(snapshot.Source.ValidTo) {
		return false, insufficient("校验风险快照与来源时效", domain.ErrInsufficientData)
	}
	if len(zones) == 0 || len(zones) > maxLossZones {
		return false, insufficient("校验风险区数量", domain.ErrInsufficientData)
	}
	seen := make(map[string]struct{}, len(zones))
	for index, zone := range zones {
		if index > 0 && zone.ID <= zones[index-1].ID {
			return false, insufficient("校验风险区规范顺序", domain.ErrInvalidInput)
		}
		if err := validateRiskZone(zone, snapshotID, seen); err != nil {
			return false, insufficient("校验风险区", err)
		}
	}
	return stale || sourceStale, nil
}

func snapshotWindowState(value hazarddomain.Snapshot, now time.Time) (bool, bool) {
	if value.RunAt.IsZero() || value.ValidFrom.IsZero() || value.ValidTo.IsZero() || value.RunAt.After(now) ||
		value.ValidFrom.After(now) || value.RunAt.Before(value.ValidFrom) || !value.RunAt.Before(value.ValidTo) ||
		value.ValidTo.Before(now.Add(-MaxReferenceProjectionStaleness)) {
		return false, false
	}
	valid := utc(value.RunAt) && utc(value.ValidFrom) && utc(value.ValidTo)
	return !value.ValidTo.After(now), valid
}

func riskSourceState(value provenance.Provenance, now time.Time) (bool, error) {
	if err := value.Validate(); err != nil {
		return false, err
	}
	if provenance.LatestAuthorityTime(value).After(now) || value.ValidFrom.IsZero() || value.ValidFrom.After(now) ||
		value.ValidTo.IsZero() || value.ValidTo.Before(now.Add(-MaxReferenceProjectionStaleness)) {
		return false, domain.ErrInsufficientData
	}
	return value.Stale || !value.ValidTo.After(now), nil
}

func utc(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

func validateRiskZone(value LossRiskZone, snapshotID string, seen map[string]struct{}) error {
	if !validProjectionIdentifier(value.ID) || value.SnapshotID != snapshotID || !value.AreaCalculated ||
		!finite(value.AreaSquareM) || value.AreaSquareM <= 0 {
		return domain.ErrInvalidInput
	}
	if _, exists := seen[value.ID]; exists {
		return fmt.Errorf("%w: 风险区标识重复", domain.ErrInvalidInput)
	}
	if _, ok := riskLevelRank(value.Level); !ok {
		return fmt.Errorf("%w: 风险区等级无效", domain.ErrInvalidInput)
	}
	seen[value.ID] = struct{}{}
	return nil
}

func validateAuthoritativeProjection(value LossSpatialProjection, snapshot hazarddomain.Snapshot,
	zones []LossRiskZone, now time.Time,
) (bool, error) {
	if value.SnapshotID != snapshot.ID || !validProjectionIdentifier(value.ID) || !validProjectionIdentifier(value.Version) ||
		value.RegionCode != "CN" || value.Status != spatialdomain.AnalysisAvailable ||
		!validDigest(value.Digest) ||
		!strings.HasPrefix(value.AdminBoundaryID, "CHN-ADM0-") || !validDigest(value.AdminBoundaryDigest) ||
		!validProjectionStrings([]string{value.AdminBoundaryReference}, true) {
		return false, insufficient("校验去重空间投影身份", domain.ErrInsufficientData)
	}
	stale, windowOK := projectionWindowState(value, now)
	if !finite(value.TotalAreaSquareMeters) || value.TotalAreaSquareMeters <= 0 || !utc(value.CalculatedAt) ||
		value.CalculatedAt.Before(snapshot.RunAt) || value.CalculatedAt.After(now) ||
		!windowOK {
		return false, insufficient("校验去重空间投影时效与面积", domain.ErrInsufficientData)
	}
	if value.ProjectionValidFrom.Before(snapshot.ValidFrom) || value.ProjectionValidTo.After(snapshot.ValidTo) ||
		value.ProjectionValidFrom.Before(snapshot.Source.ValidFrom) ||
		value.ProjectionValidTo.After(snapshot.Source.ValidTo) {
		return false, insufficient("校验空间投影与风险来源有效期", domain.ErrInsufficientData)
	}
	if !validProjectionStrings(value.InputReferences, true) || !validProjectionStrings(value.DatasetReferences, false) {
		return false, insufficient("校验去重空间投影来源", domain.ErrInsufficientData)
	}
	if !validProjectionLimitations(value.ProjectionLimitations) {
		return false, insufficient("校验空间投影限制", domain.ErrInsufficientData)
	}
	zoneLevels, err := validateProjectionZones(zones, value.RegionCode)
	if err != nil {
		return false, insufficient("校验去重投影行政绑定", err)
	}
	if !validDeduplicatedArea(value.TotalAreaSquareMeters, zones) {
		return false, insufficient("校验去重投影总面积", domain.ErrInsufficientData)
	}
	if err = validateProjectionFeatures(value.Features, zoneLevels); err != nil {
		return false, err
	}
	return stale, nil
}

func projectionWindowState(value LossSpatialProjection, now time.Time) (bool, bool) {
	valid := validProjectionWindow(value) && !value.ProjectionCollectedAt.After(now) &&
		!value.ProjectionValidFrom.After(now) &&
		!value.ProjectionValidTo.Before(now.Add(-MaxReferenceProjectionStaleness))
	return !value.ProjectionValidTo.After(now), valid
}

func validateProjectionZones(zones []LossRiskZone, region string) (map[string]hazarddomain.RiskLevel, error) {
	levels := make(map[string]hazarddomain.RiskLevel, len(zones))
	for _, zone := range zones {
		if !validProjectionStrings(zone.AdminCodes, true) || !contains(zone.AdminCodes, region) {
			return nil, domain.ErrInsufficientData
		}
		levels[zone.ID] = zone.Level
	}
	return levels, nil
}

func validateProjectionFeatures(values []LossExposureFeature, zones map[string]hazarddomain.RiskLevel) error {
	if len(values) == 0 || len(values) > maxLossFeatures {
		return insufficient("校验去重暴露数量", domain.ErrInsufficientData)
	}
	seen, kinds, previous := make(map[string]struct{}, len(values)), make(map[LossFeatureKind]struct{}, 3), ""
	for _, value := range values {
		key := string(value.Kind) + "\x00" + value.FeatureID
		if key <= previous || !validProjectionFeature(value, zones) {
			return insufficient("校验去重暴露分项", domain.ErrInsufficientData)
		}
		if _, exists := seen[value.FeatureID]; exists {
			return insufficient("校验全局 featureId 唯一性", domain.ErrInsufficientData)
		}
		seen[value.FeatureID], kinds[value.Kind], previous = struct{}{}, struct{}{}, key
	}
	if len(kinds) != 3 {
		return insufficient("校验真实零值与暴露类别完整性", domain.ErrInsufficientData)
	}
	return nil
}

func validProjectionFeature(value LossExposureFeature, zones map[string]hazarddomain.RiskLevel) bool {
	if !validProjectionIdentifier(value.FeatureID) || !value.Provided || value.Status != spatialdomain.MetricAvailable ||
		!finite(value.Quantity) || value.Quantity < 0 || !finite(value.CoverageRatio) || value.CoverageRatio <= 0 || value.CoverageRatio > 1 {
		return false
	}
	if value.Unit != featureUnit(value.Kind) ||
		(value.Kind == LossFeatureFacility && value.Quantity != math.Trunc(value.Quantity)) ||
		!validProjectionStrings(value.ZoneIDs, true) ||
		!validProjectionStrings(value.InputReferences, true) {
		return false
	}
	for _, zoneID := range value.ZoneIDs {
		if _, exists := zones[zoneID]; !exists {
			return false
		}
	}
	return true
}

func featureUnit(value LossFeatureKind) string {
	switch value {
	case LossFeaturePopulation:
		return "people"
	case LossFeatureRoad:
		return "meters"
	case LossFeatureFacility:
		return "count"
	default:
		return ""
	}
}

func validDeduplicatedArea(total float64, zones []LossRiskZone) bool {
	sum, largest := 0.0, 0.0
	for _, zone := range zones {
		sum += zone.AreaSquareM
		largest = math.Max(largest, zone.AreaSquareM)
	}
	lowerTolerance := math.Max(1e-6, largest*deduplicatedAreaToleranceRatio)
	upperTolerance := math.Max(1e-6, sum*deduplicatedAreaToleranceRatio)
	return total >= largest-lowerTolerance && total <= sum+upperTolerance
}

func deriveAuthoritativeInput(projection LossInputProjection) (authoritativeInput, error) {
	intensity, err := highestIntensity(projection.Zones)
	if err != nil {
		return authoritativeInput{}, err
	}
	riskByID := make(map[string]LossRiskZone, len(projection.Zones))
	for _, zone := range projection.Zones {
		riskByID[zone.ID] = zone
	}
	riskEvidence, population, exposures, err := deriveFeatureEvidence(projection, riskByID)
	if err != nil {
		return authoritativeInput{}, err
	}
	return authoritativeInput{snapshot: projection.Snapshot, analysis: projection.Analysis,
		regionCode: projection.Analysis.RegionCode, intensityBand: intensity,
		riskZones: riskEvidence, population: population, exposures: exposures}, nil
}

func deriveFeatureEvidence(projection LossInputProjection, risks map[string]LossRiskZone) (
	[]lossdomain.RiskZoneEvidence, []lossdomain.PopulationEvidence, []lossdomain.Exposure, error,
) {
	riskEvidence := make([]lossdomain.RiskZoneEvidence, 0, len(projection.Zones))
	population := make([]lossdomain.PopulationEvidence, 0, len(projection.Analysis.Features))
	exposures := make([]lossdomain.Exposure, 0, len(projection.Analysis.Features))
	for _, zone := range projection.Zones {
		riskEvidence = append(riskEvidence, lossdomain.RiskZoneEvidence{ID: zone.ID, Level: string(zone.Level),
			AreaSquareMeters: zone.AreaSquareM, AdminCodes: cloneStrings(zone.AdminCodes)})
	}
	for _, feature := range projection.Analysis.Features {
		featureIntensity, err := featureIntensity(feature.ZoneIDs, risks)
		if err != nil {
			return nil, nil, nil, err
		}
		if feature.Kind == LossFeaturePopulation {
			population = append(population, populationEvidence(feature))
			continue
		}
		exposures = append(exposures, assetExposure(feature, projection.Analysis, featureIntensity))
	}
	sort.Slice(exposures, func(left, right int) bool { return exposureKey(exposures[left]) < exposureKey(exposures[right]) })
	return riskEvidence, population, exposures, nil
}

func populationEvidence(value LossExposureFeature) lossdomain.PopulationEvidence {
	return lossdomain.PopulationEvidence{FeatureID: value.FeatureID, ZoneID: value.ZoneIDs[0], ZoneIDs: cloneStrings(value.ZoneIDs),
		Quantity: value.Quantity, Unit: value.Unit, CoverageRatio: value.CoverageRatio, Provided: value.Provided,
		MetricStatus: string(value.Status), InputReferences: cloneStrings(value.InputReferences)}
}

func assetExposure(value LossExposureFeature, analysis LossSpatialProjection, intensity string) lossdomain.Exposure {
	asset := lossdomain.AssetFacility
	if value.Kind == LossFeatureRoad {
		asset = lossdomain.AssetRoad
	}
	return lossdomain.Exposure{FeatureID: value.FeatureID, ZoneID: value.ZoneIDs[0], ZoneIDs: cloneStrings(value.ZoneIDs),
		AssetType: asset, Quantity: value.Quantity, Unit: value.Unit, CoverageRatio: value.CoverageRatio,
		Provided: value.Provided, MetricStatus: string(value.Status), IntensityBand: intensity,
		AnalysisID: analysis.ID, AnalysisVersion: analysis.Version, InputReferences: cloneStrings(value.InputReferences)}
}

func featureIntensity(zoneIDs []string, risks map[string]LossRiskZone) (string, error) {
	zones := make([]LossRiskZone, 0, len(zoneIDs))
	for _, zoneID := range zoneIDs {
		zone, exists := risks[zoneID]
		if !exists {
			return "", insufficient("匹配去重 feature 风险区", domain.ErrInsufficientData)
		}
		zones = append(zones, zone)
	}
	return highestIntensity(zones)
}

func highestIntensity(zones []LossRiskZone) (string, error) {
	maximum, selected := 0, hazarddomain.RiskLevel("")
	for _, zone := range zones {
		rank, ok := riskLevelRank(zone.Level)
		if !ok {
			return "", insufficient("推导风险强度", domain.ErrInsufficientData)
		}
		if rank > maximum {
			maximum, selected = rank, zone.Level
		}
	}
	return string(selected), nil
}

func riskLevelRank(value hazarddomain.RiskLevel) (int, bool) {
	switch value {
	case hazarddomain.RiskLow:
		return 1, true
	case hazarddomain.RiskModerate:
		return 2, true
	case hazarddomain.RiskHigh:
		return 3, true
	case hazarddomain.RiskVeryHigh:
		return 4, true
	default:
		return 0, false
	}
}

func preparePlan(input authoritativeInput, set lossdomain.BaselineSet, now time.Time) (calculationPlan, error) {
	referenceOnly, err := referenceBaselineSet(set)
	if err != nil {
		return calculationPlan{}, err
	}
	if input.stale && !referenceOnly {
		return calculationPlan{}, insufficient("拒绝用过期投影生成已批准损失评估", domain.ErrInsufficientData)
	}
	monetized := input.exposures
	if referenceOnly {
		monetized = referenceRoadExposures(input.exposures)
		if len(monetized) == 0 {
			return calculationPlan{}, insufficient("匹配研究参考道路暴露", domain.ErrInsufficientData)
		}
	}
	assets := exposureAssets(monetized)
	selectedCosts := make([]lossdomain.CostBaseline, 0, len(assets))
	for _, asset := range assets {
		unit := exposureUnit(monetized, asset)
		cost, err := selectCost(set.Costs, asset, input.regionCode, unit, now)
		if err != nil {
			return calculationPlan{}, err
		}
		selectedCosts = append(selectedCosts, cost)
	}
	pairs := exposurePairs(monetized)
	selectedVulnerabilities := make([]lossdomain.Vulnerability, 0, len(pairs))
	for _, pair := range pairs {
		vulnerability, err := selectVulnerability(set.Vulnerabilities, pair.asset, pair.intensity, input, now)
		if err != nil {
			return calculationPlan{}, err
		}
		selectedVulnerabilities = append(selectedVulnerabilities, vulnerability)
	}
	baselineSet, err := consistentBaselineSet(set.Version, selectedCosts, selectedVulnerabilities)
	if err != nil {
		return calculationPlan{}, err
	}
	return calculationPlan{input: input, monetizedExposures: monetized, costs: selectedCosts,
		vulnerabilities: selectedVulnerabilities, baselineSet: baselineSet, referenceOnly: referenceOnly}, nil
}

func referenceBaselineSet(set lossdomain.BaselineSet) (bool, error) {
	status := set.Costs[0].Status
	if status != lossdomain.BaselineApproved && status != lossdomain.BaselineDemoOnly {
		return false, insufficient("识别损失基线状态", domain.ErrInsufficientData)
	}
	for _, value := range set.Costs {
		if value.Status != status {
			return false, insufficient("拒绝混用成本基线状态", domain.ErrInsufficientData)
		}
	}
	for _, value := range set.Vulnerabilities {
		if value.Status != status {
			return false, insufficient("拒绝混用成本与脆弱性基线状态", domain.ErrInsufficientData)
		}
	}
	return status == lossdomain.BaselineDemoOnly, nil
}

func referenceRoadExposures(values []lossdomain.Exposure) []lossdomain.Exposure {
	result := make([]lossdomain.Exposure, 0, len(values))
	for _, value := range values {
		if value.AssetType == lossdomain.AssetRoad {
			result = append(result, value)
		}
	}
	return result
}

func consistentBaselineSet(version string, costs []lossdomain.CostBaseline,
	vulnerabilities []lossdomain.Vulnerability) (lossdomain.BaselineSetEvidence, error) {
	if len(costs) == 0 || len(vulnerabilities) == 0 {
		return lossdomain.BaselineSetEvidence{}, insufficient("校验基线数据集", domain.ErrInsufficientData)
	}
	first := costs[0].Source
	identity := lossdomain.BaselineSetEvidence{Provider: first.Provider, Dataset: first.Dataset, Version: first.DatasetVersion}
	if identity.Version != version {
		return lossdomain.BaselineSetEvidence{}, insufficient("校验基线集合版本", domain.ErrInsufficientData)
	}
	matches := func(value provenance.Provenance) bool {
		return value.Provider == identity.Provider && value.Dataset == identity.Dataset && value.DatasetVersion == identity.Version
	}
	for _, value := range costs {
		if !matches(value.Source) {
			return lossdomain.BaselineSetEvidence{}, insufficient("校验成本基线数据集版本", domain.ErrInsufficientData)
		}
	}
	for _, value := range vulnerabilities {
		if !matches(value.Source) {
			return lossdomain.BaselineSetEvidence{}, insufficient("校验脆弱性基线数据集版本", domain.ErrInsufficientData)
		}
	}
	return identity, nil
}

func selectCost(values []lossdomain.CostBaseline, asset lossdomain.AssetType, region, unit string,
	now time.Time) (lossdomain.CostBaseline, error) {
	exact, national := make([]lossdomain.CostBaseline, 0, 1), make([]lossdomain.CostBaseline, 0, 1)
	reference := make([]lossdomain.CostBaseline, 0, 1)
	for _, value := range values {
		if value.AssetType != asset || value.Unit != unit {
			continue
		}
		if value.Status == lossdomain.BaselineDemoOnly {
			reference = append(reference, value)
		} else if value.RegionCode == region {
			exact = append(exact, value)
		} else if value.RegionCode == "CN" {
			national = append(national, value)
		}
	}
	candidates, level := national, lossdomain.BaselineNational
	if len(exact) > 0 {
		candidates = exact
		if region != "CN" {
			level = lossdomain.BaselineRegional
		}
	}
	if len(exact) == 0 && len(national) == 0 && len(reference) > 0 {
		candidates, level = reference, lossdomain.BaselineReferenceCase
	}
	if len(candidates) != 1 {
		return lossdomain.CostBaseline{}, insufficient("匹配唯一成本基线", domain.ErrInsufficientData)
	}
	value := candidates[0]
	value.Provided, value.BaselineLevel = true, level
	if err := validateSelectedCost(value, unit, now); err != nil {
		return lossdomain.CostBaseline{}, err
	}
	return value, nil
}

func validateSelectedCost(value lossdomain.CostBaseline, unit string, now time.Time) error {
	if err := value.Validate(); err != nil {
		return insufficient("校验成本基线", err)
	}
	if value.Unit != unit || value.PriceBaseDate.After(now) {
		return insufficient("校验成本基线单位或基准日", domain.ErrInsufficientData)
	}
	if value.Status == lossdomain.BaselineDemoOnly {
		return validateReferenceBaseline(value.Source, now)
	}
	if value.Status != lossdomain.BaselineApproved {
		return insufficient("校验已批准成本基线", domain.ErrInsufficientData)
	}
	if err := validateCurrentSource(value.Source, now); err != nil {
		return insufficient("校验成本基线时效", err)
	}
	return nil
}

func selectVulnerability(values []lossdomain.Vulnerability, asset lossdomain.AssetType, intensity string,
	input authoritativeInput, now time.Time) (lossdomain.Vulnerability, error) {
	exact, national := make([]lossdomain.Vulnerability, 0, 1), make([]lossdomain.Vulnerability, 0, 1)
	reference := make([]lossdomain.Vulnerability, 0, 1)
	for _, value := range values {
		if value.AssetType != asset || value.HazardType != string(input.snapshot.HazardType) || value.IntensityBand != intensity {
			continue
		}
		if value.Status == lossdomain.BaselineDemoOnly {
			reference = append(reference, value)
		} else if value.CalibrationRegion == input.regionCode {
			exact = append(exact, value)
		} else if value.CalibrationRegion == "CN" {
			national = append(national, value)
		}
	}
	candidates, level := national, lossdomain.BaselineNational
	if len(exact) > 0 {
		candidates = exact
	}
	if len(exact) == 0 && len(national) == 0 && len(reference) > 0 {
		candidates, level = reference, lossdomain.BaselineReferenceCase
	}
	if len(candidates) != 1 {
		return lossdomain.Vulnerability{}, insufficient("匹配唯一脆弱性基线", domain.ErrInsufficientData)
	}
	value := candidates[0]
	if len(exact) > 0 && input.regionCode != "CN" {
		level = lossdomain.BaselineRegional
	}
	value.Provided, value.BaselineLevel = true, level
	if err := validateSelectedVulnerability(value, now); err != nil {
		return lossdomain.Vulnerability{}, err
	}
	return value, nil
}

func validateSelectedVulnerability(value lossdomain.Vulnerability, now time.Time) error {
	if err := value.Validate(); err != nil {
		return insufficient("校验脆弱性基线", err)
	}
	if value.Status == lossdomain.BaselineDemoOnly {
		return validateReferenceBaseline(value.Source, now)
	}
	if value.Status != lossdomain.BaselineApproved {
		return insufficient("校验已批准脆弱性基线", domain.ErrInsufficientData)
	}
	if err := validateCurrentSource(value.Source, now); err != nil {
		return insufficient("校验脆弱性基线时效", err)
	}
	return nil
}

func validateReferenceBaseline(source provenance.Provenance, now time.Time) error {
	if source.Stale || source.FetchedAt.IsZero() || source.FetchedAt.After(now) ||
		(!source.PublishedAt.IsZero() && source.PublishedAt.After(now)) {
		return insufficient("校验研究参考基线时间", domain.ErrInsufficientData)
	}
	return nil
}

func calculate(plan calculationPlan, calculatedAt time.Time) (lossdomain.Assessment, error) {
	low, mid, high, err := calculateAmounts(plan)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	evidence := buildEvidence(plan)
	population, roads, facilities := contextMetrics(plan.input)
	confidence := exposureConfidence(plan.input.exposures)
	if len(plan.input.analysis.ProjectionLimitations) > 0 && confidence > maxLimitedProjectionConfidence {
		confidence = maxLimitedProjectionConfidence
	}
	if plan.referenceOnly && confidence > maxReferenceConfidence {
		confidence = maxReferenceConfidence
	}
	if plan.input.stale && confidence > maxStaleReferenceConfidence {
		confidence = maxStaleReferenceConfidence
	}
	excluded := sortedStrings([]string{"建筑物损失未纳入：缺少权威建筑暴露面积", "间接经济损失和人员伤亡未纳入"})
	status, method := lossdomain.AssessmentAvailable, "暴露量/覆盖率 × 单位重置成本 × 影响比例 × 损伤率"
	if plan.referenceOnly {
		status, method = lossdomain.AssessmentReferenceOnly, "局部热点道路长度 × 西藏案例条件损失单价区间"
		excluded = sortedStrings(append(excluded, "人口和设施仅作暴露背景，未纳入研究参考金额"))
	}
	limitations := sortedUniqueStrings(append(cloneStrings(plan.input.analysis.ProjectionLimitations),
		requiredLimitations(plan.referenceOnly, plan.input.stale)...))
	assessment := lossdomain.Assessment{SnapshotID: plan.input.snapshot.ID, HazardType: string(plan.input.snapshot.HazardType),
		RegionCode: plan.input.regionCode, FormulaVersion: lossdomain.FormulaVersion,
		ScenarioMethod:      method,
		ConditionalLowCents: low, ConditionalMidCents: mid, ConditionalHighCents: high,
		ImpactAreaSquareM: plan.input.analysis.TotalAreaSquareMeters, AffectedPopulation: population,
		AffectedRoadMeters: roads, AffectedFacilities: facilities, InputReferences: lossdomain.EvidenceReferences(evidence),
		IncludedAssets: exposureAssets(plan.monetizedExposures), ExcludedLosses: excluded, Status: status,
		Confidence: confidence, ConfidenceBand: confidenceBand(confidence), Limitations: limitations,
		CalculatedAt: calculatedAt.UTC(), Evidence: evidence}
	return lossdomain.BindAssessmentIdentity(assessment)
}

func requiredLimitations(referenceOnly, stale bool) []string {
	if referenceOnly {
		result := []string{lossdomain.LimitationAdvisoryOnly, lossdomain.LimitationReferenceOnly,
			lossdomain.LimitationReferenceRoadOnly, lossdomain.LimitationReferenceTransfer}
		if stale {
			result = append(result, lossdomain.LimitationLastSuccessStale)
		}
		return result
	}
	return []string{lossdomain.LimitationDirectPhysicalLoss, lossdomain.LimitationAdvisoryOnly}
}

func calculateAmounts(plan calculationPlan) (int64, int64, int64, error) {
	costs := indexCosts(plan.costs)
	vulnerabilities := indexVulnerabilities(plan.vulnerabilities)
	var low, mid, high int64
	for _, exposure := range plan.monetizedExposures {
		cost := costs[exposure.AssetType]
		vulnerability := vulnerabilities[vulnerabilityMapKey(exposure.AssetType, exposure.IntensityBand)]
		parts, err := damageBand(exposure, cost, vulnerability)
		if err != nil {
			return 0, 0, 0, err
		}
		if low, err = addCents(low, parts[0]); err != nil {
			return 0, 0, 0, err
		}
		if mid, err = addCents(mid, parts[1]); err != nil {
			return 0, 0, 0, err
		}
		if high, err = addCents(high, parts[2]); err != nil {
			return 0, 0, 0, err
		}
	}
	return low, mid, high, nil
}

func damageBand(exposure lossdomain.Exposure, cost lossdomain.CostBaseline,
	vulnerability lossdomain.Vulnerability) ([3]int64, error) {
	inputs := [3]struct {
		unitCents int64
		impact    float64
		damage    float64
	}{{cost.LowCents, vulnerability.ImpactFractionLow, vulnerability.DamageRatioLow},
		{cost.CentralCents, vulnerability.ImpactFractionMid, vulnerability.DamageRatioMid},
		{cost.HighCents, vulnerability.ImpactFractionHigh, vulnerability.DamageRatioHigh}}
	var result [3]int64
	for index, input := range inputs {
		value, err := damageCents(exposure, input.unitCents, input.impact, input.damage)
		if err != nil {
			return [3]int64{}, err
		}
		result[index] = value
	}
	return result, nil
}

func damageCents(exposure lossdomain.Exposure, unitCents int64, impact, damage float64) (int64, error) {
	quantity, err := decimalRat(exposure.Quantity)
	if err != nil {
		return 0, err
	}
	coverage, err := decimalRat(exposure.CoverageRatio)
	if err != nil || coverage.Sign() <= 0 {
		return 0, fmt.Errorf("%w: 损失覆盖率无效", domain.ErrInvalidInput)
	}
	impactRatio, err := decimalRat(impact)
	if err != nil {
		return 0, err
	}
	damageRatio, err := decimalRat(damage)
	if err != nil {
		return 0, err
	}
	value := new(big.Rat).Mul(quantity, big.NewRat(unitCents, 1))
	value.Mul(value, impactRatio).Mul(value, damageRatio).Quo(value, coverage)
	return roundNonNegativeRat(value)
}

func decimalRat(value float64) (*big.Rat, error) {
	if !finite(value) || value < 0 {
		return nil, fmt.Errorf("%w: 损失计算比例不是有限非负数", domain.ErrInvalidInput)
	}
	result, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok {
		return nil, fmt.Errorf("%w: 损失计算比例无法转为定点数", domain.ErrInvalidInput)
	}
	return result, nil
}

func roundNonNegativeRat(value *big.Rat) (int64, error) {
	if value == nil || value.Sign() < 0 {
		return 0, fmt.Errorf("%w: 损失定点结果无效", domain.ErrInvalidInput)
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	doubled := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
	if doubled.Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() < 0 {
		return 0, fmt.Errorf("%w: 损失金额超出整数范围", domain.ErrInvalidInput)
	}
	return quotient.Int64(), nil
}

func addCents(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("%w: 损失金额累计溢出", domain.ErrInvalidInput)
	}
	return left + right, nil
}

func buildEvidence(plan calculationPlan) lossdomain.AssessmentEvidence {
	input := plan.input
	return lossdomain.AssessmentEvidence{Version: lossdomain.EvidenceVersion,
		Snapshot: lossdomain.SnapshotEvidence{ID: input.snapshot.ID, HazardType: string(input.snapshot.HazardType),
			ModelName: input.snapshot.ModelName, ModelVersion: input.snapshot.ModelVersion, Status: string(input.snapshot.Status),
			RunAt: input.snapshot.RunAt, ValidFrom: input.snapshot.ValidFrom, ValidTo: input.snapshot.ValidTo, Source: input.snapshot.Source},
		SpatialAnalysis: lossdomain.SpatialAnalysisEvidence{ID: input.analysis.ID, Version: input.analysis.Version,
			Digest: input.analysis.Digest, ProjectionID: input.analysis.ProjectionID,
			ProjectionVersion: input.analysis.ProjectionVersion, ProjectionDigest: input.analysis.ProjectionDigest,
			ProjectionCollectedAt:  input.analysis.ProjectionCollectedAt,
			ProjectionValidFrom:    input.analysis.ProjectionValidFrom,
			ProjectionValidTo:      input.analysis.ProjectionValidTo,
			ProjectionLimitations:  cloneStrings(input.analysis.ProjectionLimitations),
			SourceReferenceDigests: cloneStrings(input.analysis.SourceReferenceDigests),
			AdminBoundaryID:        input.analysis.AdminBoundaryID,
			AdminBoundaryDigest:    input.analysis.AdminBoundaryDigest,
			Status:                 string(input.analysis.Status), RegionCode: input.regionCode,
			TotalAreaSquareM: input.analysis.TotalAreaSquareMeters,
			CalculatedAt:     input.analysis.CalculatedAt, InputReferences: cloneStrings(input.analysis.InputReferences),
			DatasetReferences: cloneStrings(input.analysis.DatasetReferences)},
		BaselineSet: plan.baselineSet, IntensityBand: input.intensityBand, RiskZones: input.riskZones, Population: input.population,
		Exposures: input.exposures, Costs: plan.costs, Vulnerabilities: plan.vulnerabilities}
}

func contextMetrics(input authoritativeInput) (float64, float64, int) {
	population, roads, facilities := 0.0, 0.0, 0
	for _, value := range input.population {
		population += value.Quantity
	}
	for _, value := range input.exposures {
		if value.AssetType == lossdomain.AssetRoad {
			roads += value.Quantity
		} else if value.AssetType == lossdomain.AssetFacility {
			facilities += int(math.Round(value.Quantity))
		}
	}
	return population, roads, facilities
}

func exposureConfidence(values []lossdomain.Exposure) float64 {
	confidence := 1.0
	for _, value := range values {
		confidence *= value.CoverageRatio
	}
	return clamp(confidence)
}

func exposureAssets(values []lossdomain.Exposure) []lossdomain.AssetType {
	seen := make(map[lossdomain.AssetType]struct{}, len(values))
	for _, value := range values {
		seen[value.AssetType] = struct{}{}
	}
	result := make([]lossdomain.AssetType, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func exposureUnit(values []lossdomain.Exposure, asset lossdomain.AssetType) string {
	for _, value := range values {
		if value.AssetType == asset {
			return value.Unit
		}
	}
	return ""
}

func exposureKey(value lossdomain.Exposure) string {
	return string(value.AssetType) + "\x00" + value.FeatureID
}

func indexCosts(values []lossdomain.CostBaseline) map[lossdomain.AssetType]lossdomain.CostBaseline {
	result := make(map[lossdomain.AssetType]lossdomain.CostBaseline, len(values))
	for _, value := range values {
		result[value.AssetType] = value
	}
	return result
}

func indexVulnerabilities(values []lossdomain.Vulnerability) map[string]lossdomain.Vulnerability {
	result := make(map[string]lossdomain.Vulnerability, len(values))
	for _, value := range values {
		result[vulnerabilityMapKey(value.AssetType, value.IntensityBand)] = value
	}
	return result
}

type exposurePair struct {
	asset     lossdomain.AssetType
	intensity string
}

func exposurePairs(values []lossdomain.Exposure) []exposurePair {
	seen := make(map[string]exposurePair, len(values))
	for _, value := range values {
		key := vulnerabilityMapKey(value.AssetType, value.IntensityBand)
		seen[key] = exposurePair{asset: value.AssetType, intensity: value.IntensityBand}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]exposurePair, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func deriveBaselineRequirements(values []lossdomain.Exposure) (BaselineRequirements, error) {
	requirements := BaselineRequirements{Costs: make([]CostBaselineRequirement, 0, len(values)),
		Vulnerabilities: make([]VulnerabilityBaselineRequirement, 0, len(values))}
	for _, asset := range exposureAssets(values) {
		requirements.Costs = append(requirements.Costs,
			CostBaselineRequirement{AssetType: asset, Unit: exposureUnit(values, asset)})
	}
	for _, pair := range exposurePairs(values) {
		requirements.Vulnerabilities = append(requirements.Vulnerabilities,
			VulnerabilityBaselineRequirement{AssetType: pair.asset, IntensityBand: pair.intensity})
	}
	if err := requirements.Validate(); err != nil {
		return BaselineRequirements{}, insufficient("派生损失基线需求", err)
	}
	return requirements, nil
}

func validRequiredAsset(value lossdomain.AssetType) bool {
	return value == lossdomain.AssetBuilding || value == lossdomain.AssetRoad || value == lossdomain.AssetFacility
}

func validRequiredText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 64
}

func validRequiredIntensity(value string) bool {
	_, ok := riskLevelRank(hazarddomain.RiskLevel(value))
	return ok
}

func addRequirementKey(values map[string]struct{}, key string) bool {
	if _, exists := values[key]; exists {
		return false
	}
	values[key] = struct{}{}
	return true
}

func vulnerabilityMapKey(asset lossdomain.AssetType, intensity string) string {
	return string(asset) + "\x00" + intensity
}

func validateCurrentSource(value provenance.Provenance, now time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Stale || provenance.LatestAuthorityTime(value).After(now) || value.ValidFrom.IsZero() || value.ValidFrom.After(now) ||
		value.ValidTo.IsZero() || !value.ValidTo.After(now) {
		return domain.ErrInsufficientData
	}
	return nil
}

func calculationTime(plan calculationPlan) time.Time {
	result := plan.input.analysis.CalculatedAt
	for _, value := range []time.Time{plan.input.analysis.ProjectionCollectedAt,
		plan.input.snapshot.RunAt, provenance.LatestAuthorityTime(plan.input.snapshot.Source)} {
		if value.After(result) {
			result = value
		}
	}
	for _, value := range plan.costs {
		if sourceTime := provenance.LatestAuthorityTime(value.Source); sourceTime.After(result) {
			result = sourceTime
		}
		if value.PriceBaseDate.After(result) {
			result = value.PriceBaseDate
		}
	}
	for _, value := range plan.vulnerabilities {
		if sourceTime := provenance.LatestAuthorityTime(value.Source); sourceTime.After(result) {
			result = sourceTime
		}
	}
	return result.UTC().Truncate(time.Microsecond)
}

func supportedHazard(value hazarddomain.Type) bool {
	return value == hazarddomain.TypeLandslide || value == hazarddomain.TypeDebrisFlow
}

func projectionReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInsufficientData)) {
		return fmt.Errorf("读取单事务损失输入投影: %w", err)
	}
	return insufficient("读取单事务损失输入投影", err)
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validProjectionStrings(values []string, required bool) bool {
	if (required && len(values) == 0) || len(values) > maxLossReferences {
		return false
	}
	for index, value := range values {
		if value == "" || len(value) > maxLossReferenceBytes || strings.TrimSpace(value) != value || unsafeText(value) ||
			(index > 0 && value <= values[index-1]) {
			return false
		}
	}
	return true
}

func validProjectionIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !unsafeText(value)
}

func unsafeText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return true
		}
	}
	return false
}

func contains(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func cloneStrings(values []string) []string {
	return append([]string{}, values...)
}

func sortedStrings(values []string) []string {
	result := cloneStrings(values)
	sort.Strings(result)
	return result
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Errorf("%s: %w", label, errors.Join(domain.ErrInsufficientData, err))
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
