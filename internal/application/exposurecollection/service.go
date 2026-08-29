package exposurecollection

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

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Collector 组合真实供应商结果、PostGIS 空间投影和追加写入。
type Collector struct {
	geometries     GeometryInputReader
	boundaries     AdministrativeBoundaryProvider
	population     PopulationProvider
	infrastructure InfrastructureProvider
	administrator  AdministrativeProjector
	projector      InfrastructureProjector
	writer         ProjectionWriter
	clock          ports.Clock
}

// New 创建真实暴露采集服务。
func New(geometries GeometryInputReader, boundaries AdministrativeBoundaryProvider,
	population PopulationProvider, infrastructure InfrastructureProvider,
	administrator AdministrativeProjector, projector InfrastructureProjector,
	writer ProjectionWriter, clock ports.Clock,
) (*Collector, error) {
	if geometries == nil || boundaries == nil || population == nil || infrastructure == nil ||
		administrator == nil || projector == nil || writer == nil || clock == nil {
		return nil, fmt.Errorf("%w: 暴露采集依赖为空", domain.ErrInvalidInput)
	}
	return &Collector{geometries: geometries, boundaries: boundaries, population: population,
		infrastructure: infrastructure, administrator: administrator,
		projector: projector, writer: writer, clock: clock}, nil
}

// Collect 采集并原子保存一次真实、内容寻址的暴露投影。
func (c *Collector) Collect(ctx context.Context, snapshotID, analysisID string) (ExposureProjection, error) {
	if err := validateCollectionIdentity(snapshotID, analysisID); err != nil {
		return ExposureProjection{}, err
	}
	input, err := c.geometries.ReadExposureGeometry(ctx, snapshotID, analysisID)
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("读取暴露采集空间输入: %w", err)
	}
	canonicalizeGeometryInput(&input)
	if err = validateGeometryInput(input, snapshotID, analysisID); err != nil {
		return ExposureProjection{}, err
	}
	boundary, err := c.boundaries.Boundary(ctx)
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("采集 geoBoundaries 行政边界: %w", err)
	}
	boundary.CollectedAt = canonicalTime(boundary.CollectedAt)
	providerNow, err := collectionNow(c.clock)
	if err != nil {
		return ExposureProjection{}, err
	}
	if !utcTime(boundary.CollectedAt) || boundary.CollectedAt.After(providerNow) {
		return ExposureProjection{}, fmt.Errorf("%w: geoBoundaries 采集时间晚于当前 UTC 时间", domain.ErrInsufficientData)
	}
	administration, err := c.administrator.ProjectAdministration(ctx, input, boundary,
		geometryProjectionLimits())
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("裁剪中国行政边界: %w", err)
	}
	year := providerNow.Year()
	population, err := c.population.Population(ctx, PopulationQuery{Geometry: administration.UnionGeometry,
		ExpectedAreaSquareMeter: administration.TotalAreaSquareMeters, Year: year})
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("采集 WorldPop 人口: %w", err)
	}
	canonicalizePopulation(&population)
	infrastructure, err := c.infrastructure.Infrastructure(ctx, InfrastructureQuery{Bounds: administration.Bounds})
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("采集 OSM 道路和设施: %w", err)
	}
	canonicalizeInfrastructure(&infrastructure)
	now, err := collectionNow(c.clock)
	if err != nil {
		return ExposureProjection{}, err
	}
	if now.Before(providerNow) {
		return ExposureProjection{}, fmt.Errorf("%w: 暴露采集时钟发生回退", domain.ErrInvalidInput)
	}
	value, err := c.compose(ctx, input, administration, boundary, population, infrastructure, now)
	if err != nil {
		return ExposureProjection{}, err
	}
	if err = c.writer.SaveExposureProjection(ctx, value); err != nil {
		return ExposureProjection{}, fmt.Errorf("保存真实暴露投影: %w", err)
	}
	return value, nil
}

func (c *Collector) compose(ctx context.Context, input GeometryInput,
	administration AdministrativeProjection, boundary AdministrativeBoundary,
	population PopulationResult, infrastructure InfrastructureResult, now time.Time,
) (ExposureProjection, error) {
	if err := validateAdministration(administration, boundary, input); err != nil {
		return ExposureProjection{}, err
	}
	if err := validatePopulation(population, administration.TotalAreaSquareMeters); err != nil {
		return ExposureProjection{}, err
	}
	if err := validateInfrastructure(infrastructure); err != nil {
		return ExposureProjection{}, err
	}
	rawFeatures := withProviderReferences(infrastructure.Features, infrastructure.InputReferences)
	features, err := c.projector.ProjectInfrastructure(ctx, administration,
		rawFeatures, geometryProjectionLimits())
	if err != nil {
		return ExposureProjection{}, fmt.Errorf("投影 OSM 道路和设施: %w", err)
	}
	features = append(features, populationFeature(population, administration.Zones))
	if err = normalizeFeatures(features); err != nil {
		return ExposureProjection{}, err
	}
	analysis := input.Analysis
	analysis.Features = features
	analysis.Status = spatialanalysis.AnalysisAvailable
	analysis.RegionCode = administration.RegionCode
	analysis.TotalAreaSquareMeters = administration.TotalAreaSquareMeters
	analysis.AdminBoundaryID = administration.BoundaryID
	analysis.AdminBoundaryDigest = administration.BoundaryDigest
	analysis.AdminBoundaryReference = administration.BoundaryReference
	analysis.ProjectionLimitations = sortedUnique(append(
		append([]string(nil), population.Limitations...), infrastructure.Limitations...))
	analysis.InputReferences = mergeStrings(analysis.InputReferences,
		population.InputReferences, infrastructure.InputReferences)
	analysis.DatasetReferences = mergeStrings(analysis.DatasetReferences, boundary.InputReferences,
		[]string{population.DatasetIdentity, population.DataSource}, infrastructure.InputReferences)
	analysis.ProjectionCollectedAt = latestTime(boundary.CollectedAt, population.CollectedAt,
		infrastructure.CollectedAt)
	if analysis.ProjectionCollectedAt.After(now) {
		return ExposureProjection{}, fmt.Errorf("%w: 暴露投影采集时间晚于当前 UTC 时间", domain.ErrInsufficientData)
	}
	projection := ExposureProjection{Input: applicationloss.LossInputProjection{Snapshot: input.Snapshot,
		Zones: administration.Zones, Analysis: analysis}, ValidFrom: latestTime(input.Snapshot.ValidFrom,
		population.ValidFrom, infrastructure.ValidFrom), ValidTo: earliestTime(input.Snapshot.ValidTo,
		population.ValidTo, infrastructure.ValidTo)}
	projection.Input.Analysis.ProjectionValidFrom = projection.ValidFrom
	projection.Input.Analysis.ProjectionValidTo = projection.ValidTo
	if err = validateProjectionWindow(projection); err != nil {
		return ExposureProjection{}, err
	}
	if err = applicationloss.BindRiskProjectionIdentity(&projection.Input); err != nil {
		return ExposureProjection{}, fmt.Errorf("绑定真实暴露投影身份: %w", err)
	}
	return projection, nil
}

func canonicalizeGeometryInput(value *GeometryInput) {
	value.Snapshot.RunAt = canonicalTime(value.Snapshot.RunAt)
	value.Snapshot.ValidFrom = canonicalTime(value.Snapshot.ValidFrom)
	value.Snapshot.ValidTo = canonicalTime(value.Snapshot.ValidTo)
	canonicalizeProvenance(&value.Snapshot.Source)
	value.Analysis.CalculatedAt = canonicalTime(value.Analysis.CalculatedAt)
}

func canonicalizeProvenance(value *provenance.Provenance) {
	value.ObservedAt = canonicalTime(value.ObservedAt)
	value.PublishedAt = canonicalTime(value.PublishedAt)
	value.RevisionFirstSeenAt = canonicalTime(value.RevisionFirstSeenAt)
	value.FetchedAt = canonicalTime(value.FetchedAt)
	value.ValidFrom = canonicalTime(value.ValidFrom)
	value.ValidTo = canonicalTime(value.ValidTo)
}

func canonicalizePopulation(value *PopulationResult) {
	value.CollectedAt = canonicalTime(value.CollectedAt)
	value.ValidFrom = canonicalTime(value.ValidFrom)
	value.ValidTo = canonicalTime(value.ValidTo)
}

func canonicalizeInfrastructure(value *InfrastructureResult) {
	value.OSMBaseTimestamp = canonicalTime(value.OSMBaseTimestamp)
	value.CollectedAt = canonicalTime(value.CollectedAt)
	value.ValidFrom = canonicalTime(value.ValidFrom)
	value.ValidTo = canonicalTime(value.ValidTo)
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func collectionNow(clock ports.Clock) (time.Time, error) {
	value := clock.Now()
	if !utcTime(value) {
		return time.Time{}, fmt.Errorf("%w: 暴露采集时钟必须是非零 UTC", domain.ErrInvalidInput)
	}
	return canonicalTime(value), nil
}

func validateCollectionIdentity(snapshotID, analysisID string) error {
	if !validCollectionID(snapshotID) || !validCollectionID(analysisID) {
		return fmt.Errorf("%w: 暴露采集快照或空间分析标识无效", domain.ErrInvalidInput)
	}
	return nil
}

func validCollectionID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128
}

func validateGeometryInput(value GeometryInput, snapshotID, analysisID string) error {
	if value.Snapshot.ID != snapshotID || value.Analysis.SnapshotID != snapshotID ||
		value.Analysis.ID != analysisID || len(value.Zones) == 0 || len(value.Zones) > MaxRiskZones {
		return fmt.Errorf("%w: 暴露采集空间输入身份无效", domain.ErrInsufficientData)
	}
	if len(value.UnionGeometry) == 0 || len(value.UnionGeometry) > MaxUnionGeometryBytes ||
		value.Stats.ZoneCount != len(value.Zones) || value.Stats.UnionGeometryBytes != int64(len(value.UnionGeometry)) {
		return fmt.Errorf("%w: 暴露采集联合几何超过预算", domain.ErrInsufficientData)
	}
	if !validBounds(value.Bounds) || !finitePositive(value.Analysis.TotalAreaSquareMeters) {
		return fmt.Errorf("%w: 暴露采集空间范围无效", domain.ErrInsufficientData)
	}
	return nil
}

func validateAdministration(value AdministrativeProjection, boundary AdministrativeBoundary,
	input GeometryInput,
) error {
	if value.AnalysisID != input.Analysis.ID || value.SnapshotID != input.Snapshot.ID ||
		value.RegionCode != "CN" || value.BoundaryID != boundary.BoundaryID ||
		value.BoundaryDigest != boundary.Digest || value.BoundaryReference != boundary.Reference ||
		len(value.Zones) == 0 || len(value.UnionGeometry) == 0 || !validBounds(value.Bounds) ||
		!finitePositive(value.TotalAreaSquareMeters) {
		return fmt.Errorf("%w: 行政边界投影不完整", domain.ErrInsufficientData)
	}
	for _, zone := range value.Zones {
		if len(zone.AdminCodes) != 1 || zone.AdminCodes[0] != "CN" || !zone.AreaCalculated {
			return fmt.Errorf("%w: 行政边界风险区绑定无效", domain.ErrInsufficientData)
		}
	}
	return nil
}

func validatePopulation(value PopulationResult, expectedAreaSquareMeters float64) error {
	if value.TaskID == "" || !finiteNonNegative(value.Total) || !finitePositive(value.AreaKM2) ||
		value.DataYear <= 0 || value.DataSource == "" || value.DatasetIdentity == "" || !utcTime(value.CollectedAt) ||
		!validTimeWindow(value.ValidFrom, value.ValidTo) || len(value.InputReferences) == 0 ||
		len(value.Limitations) > MaxProviderReferences {
		return fmt.Errorf("%w: WorldPop 结果不完整", domain.ErrProviderUnavailable)
	}
	expected := expectedAreaSquareMeters / 1_000_000
	tolerance := math.Max(0.01, expected*0.02)
	if math.Abs(value.AreaKM2-expected) > tolerance {
		return fmt.Errorf("%w: WorldPop 面积与风险区联合面积不一致", domain.ErrProviderUnavailable)
	}
	return nil
}

func validateInfrastructure(value InfrastructureResult) error {
	if !utcTime(value.OSMBaseTimestamp) || !utcTime(value.CollectedAt) ||
		value.OSMBaseTimestamp.After(value.CollectedAt) || !validTimeWindow(value.ValidFrom, value.ValidTo) ||
		len(value.InputReferences) == 0 || len(value.InputReferences) > MaxProviderReferences ||
		len(value.Limitations) > MaxProviderReferences || len(value.Features) == 0 ||
		len(value.Features) > MaxInfrastructure {
		return fmt.Errorf("%w: OSM 暴露结果不完整", domain.ErrProviderUnavailable)
	}
	return nil
}

func populationFeature(value PopulationResult,
	zones []applicationloss.LossRiskZone,
) applicationloss.LossExposureFeature {
	zoneIDs := make([]string, len(zones))
	for index, zone := range zones {
		zoneIDs[index] = zone.ID
	}
	sort.Strings(zoneIDs)
	return applicationloss.LossExposureFeature{FeatureID: "worldpop-task-" + value.TaskID,
		Kind: applicationloss.LossFeaturePopulation, ZoneIDs: zoneIDs, Quantity: value.Total,
		Unit: "people", CoverageRatio: 1, Status: spatialanalysis.MetricAvailable,
		Provided: true, InputReferences: sortedUnique(value.InputReferences)}
}

func normalizeFeatures(values []applicationloss.LossExposureFeature) error {
	if len(values) == 0 || len(values) > MaxInfrastructure+1 {
		return fmt.Errorf("%w: 暴露 feature 数量无效", domain.ErrInsufficientData)
	}
	sort.Slice(values, func(left, right int) bool {
		return featureKey(values[left]) < featureKey(values[right])
	})
	seen, kinds := make(map[string]struct{}, len(values)), make(map[applicationloss.LossFeatureKind]struct{}, 3)
	for _, value := range values {
		if value.FeatureID == "" || len(value.ZoneIDs) == 0 || len(value.InputReferences) == 0 ||
			!finiteNonNegative(value.Quantity) || value.Status != spatialanalysis.MetricAvailable || !value.Provided {
			return fmt.Errorf("%w: 暴露 feature 内容无效", domain.ErrInsufficientData)
		}
		if _, exists := seen[value.FeatureID]; exists {
			return fmt.Errorf("%w: 暴露 feature 标识重复", domain.ErrInsufficientData)
		}
		seen[value.FeatureID], kinds[value.Kind] = struct{}{}, struct{}{}
	}
	if len(kinds) != 3 {
		return fmt.Errorf("%w: 人口、道路或设施真实暴露缺失", domain.ErrInsufficientData)
	}
	return nil
}

func validateProjectionWindow(value ExposureProjection) error {
	if !validTimeWindow(value.ValidFrom, value.ValidTo) || value.Input.Analysis.ProjectionCollectedAt.IsZero() ||
		value.Input.Analysis.ProjectionCollectedAt.After(value.ValidTo) {
		return fmt.Errorf("%w: 暴露投影共同有效窗口无效", domain.ErrInsufficientData)
	}
	return nil
}

func geometryProjectionLimits() GeometryProjectionLimits {
	return GeometryProjectionLimits{MaxFeatures: MaxInfrastructure, MaxGeometryBytes: MaxFeatureGeometry,
		MaxPointsPerItem: MaxFeaturePoints, MaxTotalPoints: MaxTotalFeaturePoints}
}

func featureKey(value applicationloss.LossExposureFeature) string {
	return string(value.Kind) + "\x00" + value.FeatureID
}

func mergeStrings(groups ...[]string) []string {
	values := make([]string, 0)
	for _, group := range groups {
		values = append(values, group...)
	}
	return sortedUnique(values)
}

func withProviderReferences(values []RawInfrastructureFeature,
	references []string,
) []RawInfrastructureFeature {
	result := make([]RawInfrastructureFeature, len(values))
	for index, value := range values {
		value.Geometry = append([]byte(nil), value.Geometry...)
		value.InputReferences = mergeStrings(value.InputReferences, references)
		result[index] = value
	}
	return result
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validBounds(value Bounds) bool {
	return finite(value.South) && finite(value.West) && finite(value.North) && finite(value.East) &&
		value.South >= -90 && value.North <= 90 && value.West >= -180 && value.East <= 180 &&
		value.South < value.North && value.West < value.East
}

func validTimeWindow(from, to time.Time) bool {
	return utcTime(from) && utcTime(to) && to.After(from)
}

func utcTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func latestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.After(result) {
			result = value
		}
	}
	return result.UTC()
}

func earliestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if result.IsZero() || value.Before(result) {
			result = value
		}
	}
	return result.UTC()
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func finite(value float64) bool            { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finitePositive(value float64) bool    { return finite(value) && value > 0 }
func finiteNonNegative(value float64) bool { return finite(value) && value >= 0 }

func providerFailure(label string, err error) error {
	if err == nil {
		err = domain.ErrProviderUnavailable
	}
	return fmt.Errorf("%s: %w", label, errors.Join(domain.ErrProviderUnavailable, err))
}
