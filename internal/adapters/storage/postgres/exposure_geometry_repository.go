package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

var (
	_ exposurecollection.GeometryInputReader     = (*HazardRepository)(nil)
	_ exposurecollection.AdministrativeProjector = (*HazardRepository)(nil)
	_ exposurecollection.InfrastructureProjector = (*HazardRepository)(nil)
)

const (
	maxInfrastructurePayloadBytes = 8 << 20
	maxGeoJSONBytesPerPoint       = 64
	maxGeoJSONStructureBytes      = 256
)

type exposureGeometryBudget struct {
	analysisID, snapshotID, version, status string
	zoneCount, declaredZones                int64
	maxPoints, totalPoints, maxBytes        int64
	totalArea                               float64
	calculatedAt                            time.Time
	inputs, datasets                        []byte
}

type exposureUnionBudget struct {
	points, memoryBytes int64
	valid               bool
}

// ReadExposureGeometry 在物化联合 GeoJSON 前检查风险区数量和复杂度。
func (r *HazardRepository) ReadExposureGeometry(ctx context.Context,
	snapshotID, analysisID string,
) (exposurecollection.GeometryInput, error) {
	if r == nil || r.pool == nil || !validExposureIdentifier(snapshotID) ||
		!validExposureIdentifier(analysisID) {
		return exposurecollection.GeometryInput{}, fmt.Errorf("%w: 暴露空间读取参数无效", domain.ErrInvalidInput)
	}
	tx, err := r.pool.BeginTx(ctx, exposureReadOptions)
	if err != nil {
		return exposurecollection.GeometryInput{}, fmt.Errorf("开始读取暴露空间事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	budget, err := readExposureGeometryBudget(ctx, tx, snapshotID, analysisID)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	value, err := materializeExposureGeometry(ctx, tx, snapshotID, budget)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return exposurecollection.GeometryInput{}, fmt.Errorf("提交暴露空间读取事务: %w", err)
	}
	return value, nil
}

func readExposureGeometryBudget(ctx context.Context, tx pgx.Tx,
	snapshotID, analysisID string,
) (exposureGeometryBudget, error) {
	var value exposureGeometryBudget
	err := tx.QueryRow(ctx, exposureGeometryBudgetSQL, snapshotID, analysisID).Scan(&value.analysisID,
		&value.snapshotID, &value.version, &value.status, &value.declaredZones,
		&value.totalArea, &value.calculatedAt, &value.inputs, &value.datasets,
		&value.zoneCount, &value.maxPoints, &value.totalPoints, &value.maxBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, domain.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("预检暴露空间输入: %w", err)
	}
	if value.zoneCount <= 0 || value.zoneCount > exposurecollection.MaxRiskZones ||
		value.zoneCount != value.declaredZones || value.maxPoints <= 0 ||
		value.maxPoints > hardMaxLossGeometryPointsPerZone || value.totalPoints < value.maxPoints ||
		value.totalPoints > hardMaxLossTotalGeometryPoints || value.maxBytes > hardMaxLossGeometryBytesPerZone ||
		conservativeUnionBytes(value.totalPoints, value.zoneCount) > exposurecollection.MaxUnionGeometryBytes ||
		!finitePositive(value.totalArea) {
		return value, fmt.Errorf("%w: 暴露空间输入超过安全预算", domain.ErrInsufficientData)
	}
	if value.snapshotID != snapshotID || value.analysisID != analysisID ||
		len(spatialDigest(value.analysisID)) != 64 ||
		!validSpatialAnalysisStatus(value.status) || value.version == "" || value.calculatedAt.IsZero() {
		return value, fmt.Errorf("%w: 暴露空间分析身份或状态无效", domain.ErrInsufficientData)
	}
	union, err := readExposureUnionBudget(ctx, tx, value.analysisID)
	if err != nil {
		return value, err
	}
	if !validExposureUnionBudget(union) {
		return value, fmt.Errorf("%w: 风险区联合几何超过预算", domain.ErrInsufficientData)
	}
	return value, nil
}

func conservativeUnionBytes(points, zones int64) int64 {
	return maxGeoJSONStructureBytes + points*maxGeoJSONBytesPerPoint + zones*64
}

func readExposureUnionBudget(ctx context.Context, tx pgx.Tx,
	analysisID string,
) (exposureUnionBudget, error) {
	var value exposureUnionBudget
	if err := tx.QueryRow(ctx, exposureUnionBudgetSQL, analysisID).
		Scan(&value.points, &value.memoryBytes, &value.valid); err != nil {
		return value, fmt.Errorf("预检风险区联合几何: %w", err)
	}
	return value, nil
}

func validExposureUnionBudget(value exposureUnionBudget) bool {
	return value.valid && value.points > 0 && value.points <= hardMaxLossTotalGeometryPoints &&
		value.memoryBytes > 0 && value.memoryBytes <= hardMaxLossTotalGeometryBytes &&
		conservativeUnionBytes(value.points, 1) <= exposurecollection.MaxUnionGeometryBytes
}

func materializeExposureGeometry(ctx context.Context, tx pgx.Tx, snapshotID string,
	budget exposureGeometryBudget,
) (exposurecollection.GeometryInput, error) {
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, lossProjectionSnapshotSQL, snapshotID))
	if err != nil {
		return exposurecollection.GeometryInput{}, fmt.Errorf("读取暴露风险快照: %w", err)
	}
	zones, err := readExposureBaseZones(ctx, tx, budget.analysisID)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	geometry, bounds, err := readExposureUnion(ctx, tx, budget.analysisID)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	inputs, err := decodeExposureStrings(budget.inputs)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	datasets, err := decodeExposureStrings(budget.datasets)
	if err != nil {
		return exposurecollection.GeometryInput{}, err
	}
	analysis := applicationloss.LossSpatialProjection{ID: budget.analysisID,
		Version: budget.version, Digest: spatialDigest(budget.analysisID), SnapshotID: snapshotID,
		Status: spatialanalysis.AnalysisStatus(budget.status), TotalAreaSquareMeters: budget.totalArea,
		CalculatedAt: budget.calculatedAt.UTC(), InputReferences: inputs, DatasetReferences: datasets}
	return exposurecollection.GeometryInput{Snapshot: snapshot, Zones: zones, Analysis: analysis,
		UnionGeometry: geometry, Bounds: bounds, Stats: exposurecollection.GeometryStats{
			ZoneCount: len(zones), UnionGeometryBytes: int64(len(geometry)),
			MaxZonePoints: budget.maxPoints, TotalZonePoints: budget.totalPoints}}, nil
}

func readExposureBaseZones(ctx context.Context, tx pgx.Tx,
	analysisID string,
) ([]applicationloss.LossRiskZone, error) {
	rows, err := tx.Query(ctx, exposureBaseZonesSQL, analysisID)
	if err != nil {
		return nil, fmt.Errorf("查询暴露基础风险区: %w", err)
	}
	defer rows.Close()
	values := make([]applicationloss.LossRiskZone, 0)
	for rows.Next() {
		var value applicationloss.LossRiskZone
		var level string
		if err = rows.Scan(&value.ID, &value.SnapshotID, &level, &value.AreaSquareM,
			&value.AreaCalculated); err != nil {
			return nil, fmt.Errorf("扫描暴露基础风险区: %w", err)
		}
		value.Level = hazard.RiskLevel(level)
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历暴露基础风险区: %w", err)
	}
	return values, nil
}

func readExposureUnion(ctx context.Context, tx pgx.Tx,
	analysisID string,
) (json.RawMessage, exposurecollection.Bounds, error) {
	var payload []byte
	var bounds exposurecollection.Bounds
	err := tx.QueryRow(ctx, exposureUnionSQL, analysisID).Scan(&payload, &bounds.West,
		&bounds.South, &bounds.East, &bounds.North)
	if err != nil {
		return nil, bounds, fmt.Errorf("读取风险区联合几何: %w", err)
	}
	if len(payload) == 0 || len(payload) > exposurecollection.MaxUnionGeometryBytes {
		return nil, bounds, fmt.Errorf("%w: 风险区联合 GeoJSON 超过预算", domain.ErrInsufficientData)
	}
	return append(json.RawMessage(nil), payload...), bounds, nil
}

// ProjectAdministration 将风险区精确裁剪到版本化 CHN ADM0 边界。
func (r *HazardRepository) ProjectAdministration(ctx context.Context,
	input exposurecollection.GeometryInput, boundary exposurecollection.AdministrativeBoundary,
	limits exposurecollection.GeometryProjectionLimits,
) (exposurecollection.AdministrativeProjection, error) {
	if err := validateAdministrativeRequest(r, input, boundary, limits); err != nil {
		return exposurecollection.AdministrativeProjection{}, err
	}
	tx, err := r.pool.BeginTx(ctx, exposureReadOptions)
	if err != nil {
		return exposurecollection.AdministrativeProjection{}, fmt.Errorf("开始行政边界投影事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = preflightAdministrativeProjection(ctx, tx, input, boundary, limits); err != nil {
		return exposurecollection.AdministrativeProjection{}, err
	}
	value, err := materializeAdministrativeProjection(ctx, tx, input, boundary)
	if err != nil {
		return exposurecollection.AdministrativeProjection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return exposurecollection.AdministrativeProjection{}, fmt.Errorf("提交行政边界投影事务: %w", err)
	}
	return value, nil
}

func validateAdministrativeRequest(r *HazardRepository, input exposurecollection.GeometryInput,
	boundary exposurecollection.AdministrativeBoundary, limits exposurecollection.GeometryProjectionLimits,
) error {
	if r == nil || r.pool == nil || input.Analysis.ID == "" || input.Snapshot.ID == "" ||
		boundary.RegionCode != "CN" || boundary.BoundaryType != "ADM0" || boundary.Digest == "" ||
		len(boundary.Geometry) == 0 || limits.MaxPointsPerItem <= 0 || limits.MaxTotalPoints <= 0 {
		return fmt.Errorf("%w: 行政边界投影参数无效", domain.ErrInvalidInput)
	}
	return nil
}

func preflightAdministrativeProjection(ctx context.Context, tx pgx.Tx,
	input exposurecollection.GeometryInput, boundary exposurecollection.AdministrativeBoundary,
	limits exposurecollection.GeometryProjectionLimits,
) error {
	var boundaryValid, unionValid bool
	var zones, maxPoints, totalPoints, unionPoints, unionMemoryBytes int64
	err := tx.QueryRow(ctx, administrativeProjectionBudgetSQL, input.Analysis.ID,
		string(boundary.Geometry)).Scan(&boundaryValid, &zones, &maxPoints, &totalPoints,
		&unionPoints, &unionMemoryBytes, &unionValid)
	if err != nil {
		return fmt.Errorf("预检行政边界投影: %w", err)
	}
	if !boundaryValid || zones <= 0 || zones > exposurecollection.MaxRiskZones ||
		maxPoints <= 0 || maxPoints > limits.MaxPointsPerItem || totalPoints < maxPoints ||
		totalPoints > limits.MaxTotalPoints || !validAdministrativeUnionBudget(exposureUnionBudget{
		points: unionPoints, memoryBytes: unionMemoryBytes, valid: unionValid,
	}, limits) {
		return fmt.Errorf("%w: 行政边界裁剪结果超过预算或为空", domain.ErrInsufficientData)
	}
	return nil
}

func validAdministrativeUnionBudget(value exposureUnionBudget,
	limits exposurecollection.GeometryProjectionLimits,
) bool {
	return validExposureUnionBudget(value) && value.points <= limits.MaxTotalPoints
}

func materializeAdministrativeProjection(ctx context.Context, tx pgx.Tx,
	input exposurecollection.GeometryInput, boundary exposurecollection.AdministrativeBoundary,
) (exposurecollection.AdministrativeProjection, error) {
	zones, err := readAdministrativeZones(ctx, tx, input, boundary)
	if err != nil {
		return exposurecollection.AdministrativeProjection{}, err
	}
	geometry, bounds, area, err := readAdministrativeUnion(ctx, tx, input.Analysis.ID, boundary)
	if err != nil {
		return exposurecollection.AdministrativeProjection{}, err
	}
	return exposurecollection.AdministrativeProjection{AnalysisID: input.Analysis.ID,
		SnapshotID: input.Snapshot.ID, RegionCode: "CN", BoundaryID: boundary.BoundaryID,
		BoundaryDigest: boundary.Digest, BoundaryReference: boundary.Reference,
		BoundaryGeometry: append(json.RawMessage(nil), boundary.Geometry...), UnionGeometry: geometry,
		Bounds: bounds, TotalAreaSquareMeters: area, Zones: zones}, nil
}

func readAdministrativeZones(ctx context.Context, tx pgx.Tx, input exposurecollection.GeometryInput,
	boundary exposurecollection.AdministrativeBoundary,
) ([]applicationloss.LossRiskZone, error) {
	levels := make(map[string]hazard.RiskLevel, len(input.Zones))
	for _, zone := range input.Zones {
		levels[zone.ID] = zone.Level
	}
	rows, err := tx.Query(ctx, administrativeZonesSQL, input.Analysis.ID, string(boundary.Geometry))
	if err != nil {
		return nil, fmt.Errorf("查询行政裁剪风险区: %w", err)
	}
	defer rows.Close()
	values := make([]applicationloss.LossRiskZone, 0)
	for rows.Next() {
		var value applicationloss.LossRiskZone
		if err = rows.Scan(&value.ID, &value.SnapshotID, &value.AreaSquareM); err != nil {
			return nil, fmt.Errorf("扫描行政裁剪风险区: %w", err)
		}
		value.Level, value.AreaCalculated, value.AdminCodes = levels[value.ID], true, []string{"CN"}
		if value.Level == "" {
			return nil, fmt.Errorf("%w: 行政裁剪返回未知风险区", domain.ErrInsufficientData)
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历行政裁剪风险区: %w", err)
	}
	return values, nil
}

func readAdministrativeUnion(ctx context.Context, tx pgx.Tx, analysisID string,
	boundary exposurecollection.AdministrativeBoundary,
) (json.RawMessage, exposurecollection.Bounds, float64, error) {
	var payload []byte
	var bounds exposurecollection.Bounds
	var area float64
	err := tx.QueryRow(ctx, administrativeUnionSQL, analysisID, string(boundary.Geometry)).Scan(
		&payload, &bounds.West, &bounds.South, &bounds.East, &bounds.North, &area)
	if err != nil {
		return nil, bounds, 0, fmt.Errorf("读取行政裁剪联合几何: %w", err)
	}
	if len(payload) == 0 || len(payload) > exposurecollection.MaxUnionGeometryBytes || !finitePositive(area) {
		return nil, bounds, 0, fmt.Errorf("%w: 行政裁剪联合几何无效", domain.ErrInsufficientData)
	}
	return append(json.RawMessage(nil), payload...), bounds, area, nil
}

type infrastructurePayload struct {
	FeatureID       string          `json:"feature_id"`
	Kind            string          `json:"kind"`
	Geometry        json.RawMessage `json:"geometry"`
	InputReferences []string        `json:"input_references"`
}

// ProjectInfrastructure 将 OSM 几何与行政裁剪风险区精确相交。
func (r *HazardRepository) ProjectInfrastructure(ctx context.Context,
	administration exposurecollection.AdministrativeProjection,
	features []exposurecollection.RawInfrastructureFeature,
	limits exposurecollection.GeometryProjectionLimits,
) ([]applicationloss.LossExposureFeature, error) {
	payload, err := encodeInfrastructure(features, limits)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.BeginTx(ctx, exposureReadOptions)
	if err != nil {
		return nil, fmt.Errorf("开始 OSM 空间投影事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	zoneIDs := administrativeZoneIDs(administration.Zones)
	if err = preflightInfrastructure(ctx, tx, administration, zoneIDs, payload, len(features), limits); err != nil {
		return nil, err
	}
	values, err := readProjectedInfrastructure(ctx, tx, administration, zoneIDs, payload)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交 OSM 空间投影事务: %w", err)
	}
	return values, nil
}

func encodeInfrastructure(values []exposurecollection.RawInfrastructureFeature,
	limits exposurecollection.GeometryProjectionLimits,
) ([]byte, error) {
	if len(values) == 0 || len(values) > limits.MaxFeatures {
		return nil, fmt.Errorf("%w: OSM feature 数量无效", domain.ErrInvalidInput)
	}
	payload := make([]infrastructurePayload, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if !validExposureIdentifier(value.FeatureID) || len(value.Geometry) == 0 ||
			int64(len(value.Geometry)) > limits.MaxGeometryBytes || len(value.InputReferences) == 0 {
			return nil, fmt.Errorf("%w: OSM feature 内容超过预算", domain.ErrInvalidInput)
		}
		if _, exists := seen[value.FeatureID]; exists {
			return nil, fmt.Errorf("%w: OSM feature 标识重复", domain.ErrInvalidInput)
		}
		seen[value.FeatureID] = struct{}{}
		payload[index] = infrastructurePayload{FeatureID: value.FeatureID, Kind: string(value.Kind),
			Geometry: value.Geometry, InputReferences: sortedExposureStrings(value.InputReferences)}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码 OSM feature 投影载荷: %w", err)
	}
	if len(encoded) > maxInfrastructurePayloadBytes {
		return nil, fmt.Errorf("%w: OSM feature 总载荷超过预算", domain.ErrInvalidInput)
	}
	return encoded, nil
}

func preflightInfrastructure(ctx context.Context, tx pgx.Tx,
	administration exposurecollection.AdministrativeProjection, zoneIDs []string, payload []byte,
	expectedFeatures int, limits exposurecollection.GeometryProjectionLimits,
) error {
	var features, invalid, maxPoints, totalPoints, maxBytes int64
	err := tx.QueryRow(ctx, infrastructureBudgetSQL, administration.AnalysisID,
		string(administration.BoundaryGeometry), zoneIDs, payload).Scan(&features, &invalid,
		&maxPoints, &totalPoints, &maxBytes)
	if err != nil {
		return fmt.Errorf("预检 OSM feature 几何: %w", err)
	}
	if features != int64(expectedFeatures) || invalid != 0 || maxPoints <= 0 ||
		maxPoints > limits.MaxPointsPerItem || totalPoints < maxPoints || totalPoints > limits.MaxTotalPoints ||
		maxBytes <= 0 || maxBytes > limits.MaxGeometryBytes {
		return fmt.Errorf("%w: OSM feature 几何超过投影预算", domain.ErrInsufficientData)
	}
	var bindings int64
	if err = tx.QueryRow(ctx, infrastructureBindingBudgetSQL, administration.AnalysisID,
		string(administration.BoundaryGeometry), zoneIDs, payload).Scan(&bindings); err != nil {
		return fmt.Errorf("预检 OSM feature 风险区绑定: %w", err)
	}
	if !validInfrastructureBindingBudget(bindings, int64(len(zoneIDs))) {
		return fmt.Errorf("%w: OSM feature 风险区绑定超过预算或为空", domain.ErrInsufficientData)
	}
	return nil
}

func validInfrastructureBindingBudget(infrastructureBindings, projectedZones int64) bool {
	return infrastructureBindings > 0 && projectedZones > 0 &&
		infrastructureBindings+projectedZones <= maxExposureZoneBindings
}

func readProjectedInfrastructure(ctx context.Context, tx pgx.Tx,
	administration exposurecollection.AdministrativeProjection, zoneIDs []string,
	payload []byte,
) ([]applicationloss.LossExposureFeature, error) {
	rows, err := tx.Query(ctx, infrastructureProjectionSQL, administration.AnalysisID,
		string(administration.BoundaryGeometry), zoneIDs, payload)
	if err != nil {
		return nil, fmt.Errorf("查询 OSM 精确空间投影: %w", err)
	}
	defer rows.Close()
	values := make([]applicationloss.LossExposureFeature, 0)
	for rows.Next() {
		value, scanErr := scanProjectedInfrastructure(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 OSM 精确空间投影: %w", err)
	}
	return values, nil
}

func scanProjectedInfrastructure(row pgx.Row) (applicationloss.LossExposureFeature, error) {
	var value applicationloss.LossExposureFeature
	var kind string
	var zones, references []byte
	if err := row.Scan(&value.FeatureID, &kind, &value.Quantity, &zones, &references); err != nil {
		return value, fmt.Errorf("扫描 OSM 精确空间投影: %w", err)
	}
	value.Kind = applicationloss.LossFeatureKind(kind)
	var err error
	if value.ZoneIDs, err = decodeExposureStrings(zones); err != nil {
		return value, err
	}
	if value.InputReferences, err = decodeExposureStrings(references); err != nil {
		return value, err
	}
	value.CoverageRatio, value.Status, value.Provided = 1, spatialanalysis.MetricAvailable, true
	if value.Kind == applicationloss.LossFeatureRoad {
		value.Unit = "meters"
	} else {
		value.Unit = "count"
		if value.Quantity != math.Trunc(value.Quantity) {
			return value, fmt.Errorf("%w: 设施数量不是整数", domain.ErrInsufficientData)
		}
	}
	return value, nil
}

func administrativeZoneIDs(values []applicationloss.LossRiskZone) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.ID
	}
	sort.Strings(result)
	return result
}

func decodeExposureStrings(payload []byte) ([]string, error) {
	var values []string
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, fmt.Errorf("解码暴露来源列表: %w", err)
	}
	return values, nil
}

func sortedExposureStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func spatialDigest(analysisID string) string {
	return strings.TrimPrefix(analysisID, "spatial-")
}

func validSpatialAnalysisStatus(value string) bool {
	return value == string(spatialanalysis.AnalysisAreaOnly) ||
		value == string(spatialanalysis.AnalysisPartial) || value == string(spatialanalysis.AnalysisAvailable)
}

func validExposureIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

var exposureReadOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

const exposureGeometryBudgetSQL = `WITH selected AS (
    SELECT id,snapshot_id,algorithm_version,status,zone_count,merged_area_square_meters,
        calculated_at,input_references,dataset_references
    FROM spatial_analyses sa WHERE snapshot_id=$1 AND id=$2 AND EXISTS (
        SELECT 1 FROM hazard_snapshots hs WHERE hs.id=sa.snapshot_id AND hs.analysis_complete=TRUE
    )
), zone_stats AS (
    SELECT COUNT(*)::BIGINT AS zones,COALESCE(MAX(ST_NPoints(rz.geometry)),0)::BIGINT AS max_points,
        COALESCE(SUM(ST_NPoints(rz.geometry)),0)::BIGINT AS total_points,
        COALESCE(MAX(ST_MemSize(rz.geometry)),0)::BIGINT AS max_bytes
    FROM selected s JOIN risk_zones rz ON rz.snapshot_id=s.snapshot_id
    JOIN spatial_zone_results szr ON szr.analysis_id=s.id AND szr.zone_id=rz.id
)
SELECT s.id,s.snapshot_id,s.algorithm_version,s.status,s.zone_count,s.merged_area_square_meters,
    s.calculated_at,s.input_references,s.dataset_references,z.zones,z.max_points,z.total_points,z.max_bytes
FROM selected s CROSS JOIN zone_stats z`

const exposureBaseZonesSQL = `SELECT rz.id,rz.snapshot_id,rz.risk_level,
    szr.area_square_meters,rz.area_calculated FROM spatial_zone_results szr
    JOIN risk_zones rz ON rz.id=szr.zone_id AND rz.snapshot_id=szr.snapshot_id
    WHERE szr.analysis_id=$1 ORDER BY rz.id`

const exposureSelectedZonesCTE = `WITH selected_zones AS (
    SELECT rz.geometry FROM spatial_zone_results szr JOIN risk_zones rz
        ON rz.id=szr.zone_id AND rz.snapshot_id=szr.snapshot_id WHERE szr.analysis_id=$1
), merged AS (
    SELECT ST_UnaryUnion(ST_Collect(geometry)) AS geom FROM selected_zones
)
`

const exposureUnionBudgetSQL = exposureSelectedZonesCTE + `SELECT
    COALESCE(ST_NPoints(geom),0)::BIGINT,COALESCE(ST_MemSize(geom),0)::BIGINT,
    COALESCE(NOT ST_IsEmpty(geom) AND ST_IsValid(geom)
        AND ST_GeometryType(geom) IN ('ST_Polygon','ST_MultiPolygon'),FALSE)
    FROM merged`

const exposureUnionSQL = exposureSelectedZonesCTE + `SELECT ST_AsGeoJSON(geom,9,0)::JSONB,
    ST_XMin(Box2D(geom)),ST_YMin(Box2D(geom)),ST_XMax(Box2D(geom)),ST_YMax(Box2D(geom)) FROM merged`

const administrativeGeometryCTE = `WITH boundary AS (
    SELECT ST_SetSRID(ST_GeomFromGeoJSON($2),4326) AS geom
), clipped AS (
    SELECT rz.id AS zone_id,rz.snapshot_id,
        ST_CollectionExtract(ST_MakeValid(ST_Intersection(rz.geometry,b.geom)),3) AS geom
    FROM spatial_zone_results szr JOIN risk_zones rz
        ON rz.id=szr.zone_id AND rz.snapshot_id=szr.snapshot_id
    CROSS JOIN boundary b WHERE szr.analysis_id=$1
), included AS (
    SELECT * FROM clipped WHERE NOT ST_IsEmpty(geom) AND ST_Area(geom::geography)>0
), merged AS (
    SELECT ST_UnaryUnion(ST_Collect(geom)) AS geom FROM included
) `

const administrativeProjectionBudgetSQL = administrativeGeometryCTE + `SELECT
    (SELECT ST_IsValid(geom) AND NOT ST_IsEmpty(geom)
        AND ST_GeometryType(geom) IN ('ST_Polygon','ST_MultiPolygon') FROM boundary),
	COUNT(i.zone_id)::BIGINT,COALESCE(MAX(ST_NPoints(i.geom)),0)::BIGINT,
	COALESCE(SUM(ST_NPoints(i.geom)),0)::BIGINT,
	COALESCE((SELECT ST_NPoints(geom) FROM merged),0)::BIGINT,
	COALESCE((SELECT ST_MemSize(geom) FROM merged),0)::BIGINT,
	COALESCE((SELECT NOT ST_IsEmpty(geom) AND ST_IsValid(geom)
		AND ST_GeometryType(geom) IN ('ST_Polygon','ST_MultiPolygon') FROM merged),FALSE)
	FROM included i`

const administrativeZonesSQL = administrativeGeometryCTE + `SELECT zone_id,snapshot_id,
    ST_Area(geom::geography) FROM included ORDER BY zone_id`

const administrativeUnionSQL = administrativeGeometryCTE + `SELECT ST_AsGeoJSON(geom,9,0)::JSONB,
    ST_XMin(Box2D(geom)),ST_YMin(Box2D(geom)),ST_XMax(Box2D(geom)),ST_YMax(Box2D(geom)),
    ST_Area(geom::geography) FROM merged`

const infrastructureGeometryCTE = `WITH boundary AS (
    SELECT ST_SetSRID(ST_GeomFromGeoJSON($2),4326) AS geom
), projected_zones AS (
    SELECT rz.id AS zone_id,
        ST_CollectionExtract(ST_MakeValid(ST_Intersection(rz.geometry,b.geom)),3) AS geom
    FROM spatial_zone_results szr JOIN risk_zones rz
        ON rz.id=szr.zone_id AND rz.snapshot_id=szr.snapshot_id
    CROSS JOIN boundary b WHERE szr.analysis_id=$1 AND rz.id=ANY($3::TEXT[])
), input AS (
    SELECT feature_id,kind,geometry,input_references FROM JSONB_TO_RECORDSET($4::JSONB)
        AS f(feature_id TEXT,kind TEXT,geometry JSONB,input_references JSONB)
), features AS (
    SELECT feature_id,kind,input_references,
        ST_SetSRID(ST_GeomFromGeoJSON(geometry::TEXT),4326) AS geom FROM input
), merged AS (
    SELECT ST_UnaryUnion(ST_Collect(geom)) AS geom FROM projected_zones
) `

const infrastructureBudgetSQL = infrastructureGeometryCTE + `SELECT COUNT(*)::BIGINT,
    COUNT(*) FILTER (WHERE geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom)
        OR (kind='road' AND ST_Dimension(geom)<>1)
        OR (kind='facility' AND ST_Dimension(geom) NOT IN (0,2))
        OR kind NOT IN ('road','facility'))::BIGINT,
    COALESCE(MAX(ST_NPoints(geom)),0)::BIGINT,COALESCE(SUM(ST_NPoints(geom)),0)::BIGINT,
    COALESCE(MAX(ST_MemSize(geom)),0)::BIGINT FROM features`

const infrastructureBindingBudgetSQL = infrastructureGeometryCTE + `SELECT COUNT(*)::BIGINT
FROM features f JOIN projected_zones z ON ST_Intersects(f.geom,z.geom)
WHERE f.kind='facility' OR ST_Length(ST_Intersection(f.geom,z.geom)::geography)>0`

const infrastructureProjectionSQL = infrastructureGeometryCTE + `, projected AS (
    SELECT f.feature_id,f.kind,f.input_references,f.geom,
        CASE WHEN f.kind='road' THEN ST_Length(ST_Intersection(f.geom,m.geom)::geography)
            ELSE 1::DOUBLE PRECISION END AS quantity
    FROM features f CROSS JOIN merged m WHERE ST_Intersects(f.geom,m.geom)
), bindings AS (
    SELECT p.feature_id,p.kind,p.input_references,p.quantity,z.zone_id
    FROM projected p JOIN projected_zones z ON ST_Intersects(p.geom,z.geom)
    WHERE (p.kind='facility' OR ST_Length(ST_Intersection(p.geom,z.geom)::geography)>0)
)
SELECT feature_id,kind,MAX(quantity),JSONB_AGG(DISTINCT zone_id ORDER BY zone_id),
    input_references FROM bindings WHERE quantity>0
GROUP BY feature_id,kind,input_references ORDER BY kind,feature_id`
