package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

const (
	hardMaxLossZones                          = 500
	hardMaxLossGeometryPointsPerZone          = 10_000
	hardMaxLossGeometryBytesPerZone           = 1 << 20
	hardMaxLossTotalGeometryPoints            = 500_000
	hardMaxLossTotalGeometryBytes             = 32 << 20
	hardMaxLossSpatialJSONBytes               = 16 << 20
	hardMaxLossFeatures                       = 1_000
	hardMaxLossReferences                     = 2_000
	hardMaxLossUniqueReferences               = 1_000
	hardMaxLossProjectionBytes                = 1 << 20
	hardMaxLossProjectionLimitations          = 100
	hardMaxLossProjectionLimitationBytes      = 4096
	hardMaxLossProjectionLimitationTotalBytes = 64 << 10
)

var _ loss.LossInputProjectionReader = (*HazardRepository)(nil)

type lossProjectionBudget struct {
	analysisID, version, digest, snapshotID, status, regionCode  string
	projectionID, projectionVersion, projectionDigest            string
	adminBoundaryID, adminBoundaryDigest, adminBoundaryReference string
	totalArea                                                    float64
	calculatedAt, projectionCollectedAt                          time.Time
	projectionValidFrom, projectionValidTo                       time.Time
	analysisZones, declaredZones, zones, zoneResults             int64
	validAdminZones                                              int64
	maxPoints, maxGeometryBytes, totalPoints, totalGeometryBytes int64
	spatialJSONBytes, declaredFeatures, features, featureKinds   int64
	availableFeatureKinds, invalidFeatures, orphanFeatures       int64
	declaredSourceDigests                                        int64
	references, uniqueReferences, projectionBytes                int64
	projectionLimitations, maxProjectionLimitationBytes          int64
	projectionLimitationBytes                                    int64
}

// ReadLossInput 在单个只读可重复读事务中返回损失评估所需的有界权威投影。
func (r *HazardRepository) ReadLossInput(ctx context.Context, snapshotID string, now time.Time,
	limits loss.RiskProjectionLimits,
) (loss.LossInputProjection, error) {
	return r.readLossInput(ctx, snapshotID, "", now, limits)
}

func (r *HazardRepository) readLossInput(ctx context.Context, snapshotID, analysisID string, now time.Time,
	limits loss.RiskProjectionLimits,
) (loss.LossInputProjection, error) {
	now = now.UTC().Truncate(time.Microsecond)
	if err := validateLossProjectionRequest(r, snapshotID, analysisID, now, limits); err != nil {
		return loss.LossInputProjection{}, err
	}
	tx, err := r.pool.BeginTx(ctx, lossProjectionReadOptions)
	if err != nil {
		return loss.LossInputProjection{}, fmt.Errorf("开始读取损失输入投影事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	budget, err := preflightLossProjection(ctx, tx, snapshotID, analysisID, now, limits)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	value, err := materializeLossProjection(ctx, tx, snapshotID, budget)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	loss.CanonicalizeRiskProjectionTimes(&value)
	if err = validateMaterializedLossProjection(value); err != nil {
		return loss.LossInputProjection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return loss.LossInputProjection{}, fmt.Errorf("提交损失输入投影事务: %w", err)
	}
	return value, nil
}

func validateLossProjectionRequest(r *HazardRepository, snapshotID, analysisID string, now time.Time,
	limits loss.RiskProjectionLimits,
) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("%w: 损失输入投影数据库为空", domain.ErrInvalidInput)
	}
	if snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) || len(snapshotID) > 128 {
		return fmt.Errorf("%w: 损失输入投影快照标识无效", domain.ErrInvalidInput)
	}
	if analysisID != "" && !validExposureIdentifier(analysisID) {
		return fmt.Errorf("%w: 损失输入投影空间分析标识无效", domain.ErrInvalidInput)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: 损失输入投影读取时间为空", domain.ErrInvalidInput)
	}
	if limits.MaxZones <= 0 || limits.MaxGeometryPointsPerZone <= 0 ||
		limits.MaxGeometryBytesPerZone <= 0 || limits.MaxTotalGeometryPoints < limits.MaxGeometryPointsPerZone ||
		limits.MaxTotalGeometryBytes < limits.MaxGeometryBytesPerZone || limits.MaxSpatialJSONBytes <= 0 ||
		limits.MaxFeatures <= 0 || limits.MaxReferences <= 0 || limits.MaxUniqueReferences <= 0 ||
		limits.MaxUniqueReferences > limits.MaxReferences || limits.MaxProjectionBytes <= 0 ||
		limits.MaxProjectionLimitations <= 0 || limits.MaxProjectionLimitationBytes <= 0 ||
		limits.MaxProjectionLimitationTotalBytes < limits.MaxProjectionLimitationBytes {
		return fmt.Errorf("%w: 损失输入投影限制无效", domain.ErrInvalidInput)
	}
	if exceedsHardLossLimits(limits) {
		return fmt.Errorf("%w: 损失输入投影限制超过生产安全上限", domain.ErrInvalidInput)
	}
	return nil
}

func exceedsHardLossLimits(value loss.RiskProjectionLimits) bool {
	return value.MaxZones > hardMaxLossZones ||
		value.MaxGeometryPointsPerZone > hardMaxLossGeometryPointsPerZone ||
		value.MaxGeometryBytesPerZone > hardMaxLossGeometryBytesPerZone ||
		value.MaxTotalGeometryPoints > hardMaxLossTotalGeometryPoints ||
		value.MaxTotalGeometryBytes > hardMaxLossTotalGeometryBytes ||
		value.MaxSpatialJSONBytes > hardMaxLossSpatialJSONBytes || value.MaxFeatures > hardMaxLossFeatures ||
		value.MaxReferences > hardMaxLossReferences || value.MaxUniqueReferences > hardMaxLossUniqueReferences ||
		value.MaxProjectionBytes > hardMaxLossProjectionBytes ||
		value.MaxProjectionLimitations > hardMaxLossProjectionLimitations ||
		value.MaxProjectionLimitationBytes > hardMaxLossProjectionLimitationBytes ||
		value.MaxProjectionLimitationTotalBytes > hardMaxLossProjectionLimitationTotalBytes
}

func preflightLossProjection(ctx context.Context, tx pgx.Tx, snapshotID, analysisID string, now time.Time,
	limits loss.RiskProjectionLimits,
) (lossProjectionBudget, error) {
	var exists bool
	if err := tx.QueryRow(ctx, lossProjectionSnapshotExistsSQL, snapshotID).Scan(&exists); err != nil {
		return lossProjectionBudget{}, fmt.Errorf("检查损失风险快照: %w", err)
	}
	if !exists {
		return lossProjectionBudget{}, domain.ErrNotFound
	}
	value, err := scanLossProjectionBudget(tx.QueryRow(ctx, lossProjectionBudgetSQL,
		snapshotID, analysisID, now, now.Add(-loss.MaxReferenceProjectionStaleness)))
	if errors.Is(err, pgx.ErrNoRows) {
		return lossProjectionBudget{}, lossProjectionMissing("没有可用空间分析")
	}
	if err != nil {
		return lossProjectionBudget{}, fmt.Errorf("统计损失输入投影预算: %w", err)
	}
	if err = validateLossProjectionBudget(value, limits); err != nil {
		return lossProjectionBudget{}, err
	}
	return value, nil
}

func scanLossProjectionBudget(row pgx.Row) (lossProjectionBudget, error) {
	var value lossProjectionBudget
	err := row.Scan(&value.analysisID, &value.version, &value.digest, &value.snapshotID,
		&value.status, &value.regionCode, &value.totalArea, &value.calculatedAt,
		&value.projectionID, &value.projectionVersion, &value.projectionDigest,
		&value.projectionCollectedAt, &value.projectionValidFrom, &value.projectionValidTo,
		&value.adminBoundaryID, &value.adminBoundaryDigest, &value.adminBoundaryReference,
		&value.analysisZones, &value.declaredZones, &value.zones, &value.zoneResults,
		&value.validAdminZones,
		&value.maxPoints, &value.maxGeometryBytes, &value.totalPoints, &value.totalGeometryBytes,
		&value.spatialJSONBytes, &value.declaredFeatures, &value.features,
		&value.featureKinds, &value.availableFeatureKinds, &value.invalidFeatures, &value.orphanFeatures,
		&value.declaredSourceDigests,
		&value.references, &value.uniqueReferences, &value.projectionBytes,
		&value.projectionLimitations, &value.maxProjectionLimitationBytes,
		&value.projectionLimitationBytes)
	return value, err
}

func validateLossProjectionBudget(value lossProjectionBudget, limits loss.RiskProjectionLimits) error {
	if value.analysisID == "" || value.digest == "" || value.snapshotID == "" ||
		value.status != string(spatialanalysis.AnalysisAvailable) || value.regionCode != "CN" ||
		value.projectionID == "" || value.projectionVersion == "" || value.projectionDigest == "" ||
		value.projectionCollectedAt.IsZero() || value.projectionValidFrom.IsZero() ||
		value.projectionValidTo.IsZero() || !value.projectionValidTo.After(value.projectionValidFrom) ||
		value.projectionCollectedAt.Before(value.projectionValidFrom) ||
		!value.projectionCollectedAt.Before(value.projectionValidTo) ||
		!strings.HasPrefix(value.adminBoundaryID, "CHN-ADM0-") ||
		len(value.adminBoundaryDigest) != 64 || value.adminBoundaryReference == "" {
		return lossProjectionInsufficient("空间分析身份、摘要或区域无效")
	}
	if value.zones <= 0 || value.zones > int64(limits.MaxZones) || value.analysisZones < value.zones ||
		value.declaredZones != value.zones || value.zoneResults != value.zones ||
		value.validAdminZones != value.zones {
		return lossProjectionInsufficient("风险区或空间结果数量不完整")
	}
	if value.maxPoints <= 0 || value.maxPoints > limits.MaxGeometryPointsPerZone ||
		value.maxGeometryBytes <= 0 || value.maxGeometryBytes > limits.MaxGeometryBytesPerZone ||
		value.totalPoints < value.maxPoints || value.totalPoints > limits.MaxTotalGeometryPoints ||
		value.totalGeometryBytes < value.maxGeometryBytes || value.totalGeometryBytes > limits.MaxTotalGeometryBytes {
		return lossProjectionInsufficient("风险区几何超过损失投影预算")
	}
	if value.spatialJSONBytes <= 0 || value.spatialJSONBytes > limits.MaxSpatialJSONBytes ||
		value.features <= 0 || value.features > int64(limits.MaxFeatures) ||
		value.declaredFeatures != value.features || value.featureKinds != 3 ||
		value.availableFeatureKinds != 3 || value.invalidFeatures != 0 || value.orphanFeatures != 0 {
		return lossProjectionInsufficient("空间 JSON 或真实暴露 feature 不完整")
	}
	if value.declaredSourceDigests != value.uniqueReferences || value.references <= 0 ||
		value.references > int64(limits.MaxReferences) ||
		value.uniqueReferences <= 0 || value.uniqueReferences > int64(limits.MaxUniqueReferences) ||
		value.uniqueReferences > value.references || value.projectionBytes <= 0 ||
		value.projectionBytes > limits.MaxProjectionBytes {
		return lossProjectionInsufficient("来源引用或投影字节超过预算")
	}
	if value.projectionLimitations > int64(limits.MaxProjectionLimitations) ||
		value.maxProjectionLimitationBytes > limits.MaxProjectionLimitationBytes ||
		value.projectionLimitationBytes > limits.MaxProjectionLimitationTotalBytes {
		return lossProjectionInsufficient("空间投影限制超过预算")
	}
	return nil
}

func materializeLossProjection(ctx context.Context, tx pgx.Tx, snapshotID string,
	budget lossProjectionBudget,
) (loss.LossInputProjection, error) {
	snapshot, err := readLossProjectionSnapshot(ctx, tx, snapshotID)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	zones, err := readLossProjectionZones(ctx, tx, budget.projectionID)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	analysis, err := readLossProjectionAnalysis(ctx, tx, budget)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	analysis.Features, err = readLossProjectionFeatures(ctx, tx, budget.projectionID)
	if err != nil {
		return loss.LossInputProjection{}, err
	}
	return loss.LossInputProjection{Snapshot: snapshot, Zones: zones, Analysis: analysis,
		Stats: budget.lossStats()}, nil
}

func readLossProjectionSnapshot(ctx context.Context, tx pgx.Tx, snapshotID string) (hazard.Snapshot, error) {
	value, err := scanSnapshot(tx.QueryRow(ctx, lossProjectionSnapshotSQL, snapshotID))
	if err != nil {
		return hazard.Snapshot{}, invalidStoredLossProjection("读取损失风险快照", err)
	}
	return value, nil
}

func readLossProjectionZones(ctx context.Context, tx pgx.Tx,
	projectionID string,
) ([]loss.LossRiskZone, error) {
	rows, err := tx.Query(ctx, lossProjectionZonesSQL, projectionID)
	if err != nil {
		return nil, fmt.Errorf("查询无几何损失风险区: %w", err)
	}
	defer rows.Close()
	values := make([]loss.LossRiskZone, 0)
	for rows.Next() {
		var value loss.LossRiskZone
		var level string
		var adminCodes []byte
		if err = rows.Scan(&value.ID, &value.SnapshotID, &level, &value.AreaSquareM,
			&value.AreaCalculated, &adminCodes); err != nil {
			return nil, invalidStoredLossProjection("扫描无几何损失风险区", err)
		}
		value.Level = hazard.RiskLevel(level)
		if value.AdminCodes, err = decodeLossStringArray("风险区行政代码", adminCodes); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历无几何损失风险区: %w", err)
	}
	return values, nil
}

func readLossProjectionAnalysis(ctx context.Context, tx pgx.Tx,
	budget lossProjectionBudget,
) (loss.LossSpatialProjection, error) {
	var inputs, datasets, sourceDigests, limitations []byte
	if err := tx.QueryRow(ctx, lossProjectionAnalysisSQL, budget.analysisID, budget.projectionID).
		Scan(&inputs, &datasets, &sourceDigests, &limitations); err != nil {
		return loss.LossSpatialProjection{}, invalidStoredLossProjection("读取损失空间分析引用", err)
	}
	inputReferences, err := decodeLossStringArray("空间分析输入引用", inputs)
	if err != nil {
		return loss.LossSpatialProjection{}, err
	}
	datasetReferences, err := decodeLossStringArray("空间分析数据集引用", datasets)
	if err != nil {
		return loss.LossSpatialProjection{}, err
	}
	redactedSources, err := decodeLossStringArray("空间投影脱敏来源摘要", sourceDigests)
	if err != nil {
		return loss.LossSpatialProjection{}, err
	}
	projectionLimitations, err := decodeLossStringArray("空间投影限制", limitations)
	if err != nil {
		return loss.LossSpatialProjection{}, err
	}
	return loss.LossSpatialProjection{ID: budget.analysisID, Version: budget.version,
		Digest: budget.digest, ProjectionID: budget.projectionID,
		ProjectionVersion: budget.projectionVersion, ProjectionDigest: budget.projectionDigest,
		ProjectionCollectedAt: budget.projectionCollectedAt, SourceReferenceDigests: redactedSources,
		ProjectionValidFrom: budget.projectionValidFrom, ProjectionValidTo: budget.projectionValidTo,
		ProjectionLimitations: projectionLimitations,
		AdminBoundaryID:       budget.adminBoundaryID, AdminBoundaryDigest: budget.adminBoundaryDigest,
		AdminBoundaryReference: budget.adminBoundaryReference,
		SnapshotID:             budget.snapshotID,
		Status:                 spatialanalysis.AnalysisStatus(budget.status), RegionCode: budget.regionCode,
		TotalAreaSquareMeters: budget.totalArea, CalculatedAt: budget.calculatedAt,
		InputReferences: inputReferences, DatasetReferences: datasetReferences}, nil
}

func readLossProjectionFeatures(ctx context.Context, tx pgx.Tx,
	analysisID string,
) ([]loss.LossExposureFeature, error) {
	rows, err := tx.Query(ctx, lossProjectionFeaturesSQL, analysisID)
	if err != nil {
		return nil, fmt.Errorf("查询全局去重损失暴露 feature: %w", err)
	}
	defer rows.Close()
	values := make([]loss.LossExposureFeature, 0)
	for rows.Next() {
		value, scanErr := scanLossProjectionFeature(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历全局去重损失暴露 feature: %w", err)
	}
	return values, nil
}

func scanLossProjectionFeature(row pgx.Row) (loss.LossExposureFeature, error) {
	var value loss.LossExposureFeature
	var kind, status string
	var zoneIDs, references []byte
	err := row.Scan(&value.FeatureID, &kind, &zoneIDs, &value.Quantity, &value.Unit,
		&value.CoverageRatio, &status, &value.Provided, &references)
	if err != nil {
		return loss.LossExposureFeature{}, invalidStoredLossProjection("扫描损失暴露 feature", err)
	}
	value.Kind = loss.LossFeatureKind(kind)
	value.Status = spatialanalysis.MetricStatus(status)
	if value.ZoneIDs, err = decodeLossStringArray("损失暴露风险区标识", zoneIDs); err != nil {
		return loss.LossExposureFeature{}, err
	}
	if value.InputReferences, err = decodeLossStringArray("损失暴露输入引用", references); err != nil {
		return loss.LossExposureFeature{}, err
	}
	return value, nil
}

func decodeLossStringArray(label string, payload []byte) ([]string, error) {
	var values []string
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, invalidStoredLossProjection("解码"+label, err)
	}
	return values, nil
}

func validateMaterializedLossProjection(value loss.LossInputProjection) error {
	stats := value.Stats
	if len(value.Zones) != stats.ZoneCount || len(value.Analysis.Features) != stats.FeatureCount ||
		value.Analysis.ID != stats.AnalysisID || value.Analysis.Digest != stats.AnalysisDigest ||
		value.Analysis.ProjectionID != stats.ProjectionID ||
		value.Analysis.ProjectionVersion != stats.ProjectionVersion ||
		value.Analysis.ProjectionDigest != stats.ProjectionDigest ||
		!value.Analysis.ProjectionCollectedAt.Equal(stats.ProjectionCollectedAt) ||
		!value.Analysis.ProjectionValidFrom.Equal(stats.ProjectionValidFrom) ||
		!value.Analysis.ProjectionValidTo.Equal(stats.ProjectionValidTo) {
		return lossProjectionInsufficient("损失输入投影计数或分析绑定发生变化")
	}
	references, unique := lossProjectionReferenceCounts(value)
	if references != stats.ReferenceCount || unique != stats.UniqueReferenceCount {
		return lossProjectionInsufficient("损失输入投影来源计数发生变化")
	}
	count, maximum, total := lossProjectionLimitationStats(value.Analysis.ProjectionLimitations)
	if count != stats.ProjectionLimitationCount || maximum != stats.MaxProjectionLimitationBytes ||
		total != stats.ProjectionLimitationBytes {
		return lossProjectionInsufficient("损失输入投影限制统计发生变化")
	}
	previousZone := ""
	for _, zone := range value.Zones {
		if zone.ID <= previousZone {
			return lossProjectionInsufficient("无几何风险区未按标识严格排序")
		}
		previousZone = zone.ID
	}
	if err := validateMaterializedLossFeatures(value.Analysis.Features); err != nil {
		return err
	}
	if err := loss.ValidateRiskProjectionIdentity(value); err != nil {
		return invalidStoredLossProjection("校验内容寻址损失投影", err)
	}
	return nil
}

func validateMaterializedLossFeatures(values []loss.LossExposureFeature) error {
	previous := ""
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := string(value.Kind) + "\x00" + value.FeatureID
		if key <= previous || !strictlySortedLossStrings(value.ZoneIDs) {
			return lossProjectionInsufficient("损失暴露 feature 或风险区绑定未规范排序")
		}
		if _, exists := seen[value.FeatureID]; exists {
			return lossProjectionInsufficient("损失暴露 featureId 未全局去重")
		}
		seen[value.FeatureID], previous = struct{}{}, key
	}
	return nil
}

func strictlySortedLossStrings(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		if value == "" || (index > 0 && value <= values[index-1]) {
			return false
		}
	}
	return true
}

func lossProjectionLimitationStats(values []string) (int, int64, int64) {
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

func lossProjectionReferenceCounts(value loss.LossInputProjection) (int, int) {
	references := []string{value.Snapshot.RasterReference, value.Snapshot.Source.SourceURI,
		value.Analysis.AdminBoundaryReference}
	for _, part := range value.Snapshot.Source.SourceParts {
		references = append(references, part.Reference)
	}
	references = append(references, value.Analysis.InputReferences...)
	references = append(references, value.Analysis.DatasetReferences...)
	for _, feature := range value.Analysis.Features {
		references = append(references, feature.InputReferences...)
	}
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		seen[reference] = struct{}{}
	}
	return len(references), len(seen)
}

func (value lossProjectionBudget) lossStats() loss.RiskProjectionStats {
	return loss.RiskProjectionStats{ZoneCount: int(value.zones), MaxGeometryPoints: value.maxPoints,
		MaxGeometryBytes: value.maxGeometryBytes, TotalGeometryPoints: value.totalPoints,
		TotalGeometryBytes: value.totalGeometryBytes, SpatialJSONBytes: value.spatialJSONBytes,
		FeatureCount: int(value.features), ReferenceCount: int(value.references),
		UniqueReferenceCount: int(value.uniqueReferences), ProjectionBytes: value.projectionBytes,
		AnalysisID: value.analysisID, AnalysisDigest: value.digest,
		ProjectionID: value.projectionID, ProjectionVersion: value.projectionVersion,
		ProjectionDigest: value.projectionDigest, ProjectionCollectedAt: value.projectionCollectedAt,
		ProjectionValidFrom: value.projectionValidFrom, ProjectionValidTo: value.projectionValidTo,
		ProjectionLimitationCount:    int(value.projectionLimitations),
		MaxProjectionLimitationBytes: value.maxProjectionLimitationBytes,
		ProjectionLimitationBytes:    value.projectionLimitationBytes}
}

func lossProjectionInsufficient(reason string) error {
	return fmt.Errorf("%w: %s", domain.ErrInsufficientData, reason)
}

func lossProjectionMissing(reason string) error {
	return fmt.Errorf("%s: %w", reason, errors.Join(domain.ErrInsufficientData, domain.ErrNotFound))
}

func invalidStoredLossProjection(label string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Errorf("%s: %w", label, errors.Join(domain.ErrInsufficientData, err))
}

var lossProjectionReadOptions = pgx.TxOptions{
	IsoLevel:   pgx.RepeatableRead,
	AccessMode: pgx.ReadOnly,
}

const lossProjectionSnapshotExistsSQL = `SELECT EXISTS(
    SELECT 1 FROM hazard_snapshots WHERE id=$1
)`

const lossProjectionSnapshotSQL = selectSnapshotSQL + ` WHERE id=$1`

const lossProjectionAnalysisSQL = `SELECT input_references,dataset_references,
    source_reference_digests,limitations FROM spatial_exposure_projections
    WHERE analysis_id=$1 AND id=$2`

const lossProjectionZonesSQL = `SELECT rz.id,rz.snapshot_id,rz.risk_level,
    pz.area_square_meters,TRUE,pz.admin_codes
    FROM spatial_exposure_projection_zones pz
    JOIN spatial_analyses sa ON sa.id=pz.analysis_id
    JOIN risk_zones rz ON rz.id=pz.zone_id AND rz.snapshot_id=sa.snapshot_id
    WHERE pz.projection_id=$1 ORDER BY rz.id`

const lossProjectionFeaturesSQL = `SELECT f.feature_id,f.feature_kind,
    COALESCE((SELECT JSONB_AGG(fz.zone_id ORDER BY fz.zone_id)
        FROM spatial_exposure_feature_zones fz
        WHERE fz.projection_id=f.projection_id AND fz.feature_id=f.feature_id),'[]'::JSONB),
    f.quantity,f.unit,f.coverage_ratio,f.status,f.provided,f.input_references
    FROM spatial_exposure_features f WHERE f.projection_id=$1
    ORDER BY f.feature_kind,f.feature_id`

const lossProjectionBudgetSQL = `WITH target_analysis AS (
    SELECT sa.* FROM spatial_analyses sa WHERE sa.snapshot_id=$1
        AND ($2='' OR sa.id=$2)
    ORDER BY sa.calculated_at DESC,sa.id DESC LIMIT 1
), selected AS (
    SELECT sa.id,sa.snapshot_id,sa.algorithm_version,ep.projection_status AS status,
        sa.zone_count AS analysis_zone_count,
        sa.calculated_at,ep.input_references,ep.dataset_references,
        ep.id AS projection_id,ep.projection_version,ep.projection_digest,
        ep.collected_at AS projection_collected_at,ep.valid_from AS projection_valid_from,
        ep.valid_to AS projection_valid_to,ep.region_code,ep.union_area_square_meters,
        ep.admin_boundary_id,ep.admin_boundary_digest,ep.admin_boundary_reference,
        ep.zone_count AS projection_zone_count,ep.feature_count AS projection_feature_count,
		ep.source_reference_digests,ep.limitations AS projection_limitations
    FROM target_analysis sa JOIN spatial_exposure_projections ep ON ep.analysis_id=sa.id
    WHERE sa.status IN ('area_only','partial','available')
		AND ep.complete=TRUE AND ep.collected_at<=$3
		AND ep.valid_from<=$3 AND ep.valid_to>=$4
	ORDER BY (ep.valid_to>$3) DESC,sa.calculated_at DESC,ep.collected_at DESC,ep.id DESC LIMIT 1
), zone_stats AS (
    SELECT COUNT(pz.zone_id)::BIGINT AS zone_count,
        COUNT(pz.zone_id) FILTER (WHERE JSONB_TYPEOF(pz.admin_codes)='array'
            AND JSONB_ARRAY_LENGTH(pz.admin_codes)=1 AND pz.admin_codes ? s.region_code)::BIGINT AS valid_admins,
        COALESCE(MAX(ST_NPoints(rz.geometry)),0)::BIGINT AS max_points,
        COALESCE(MAX(ST_MemSize(rz.geometry)),0)::BIGINT AS max_geometry_bytes,
        COALESCE(SUM(ST_NPoints(rz.geometry)),0)::BIGINT AS total_points,
        COALESCE(SUM(ST_MemSize(rz.geometry)),0)::BIGINT AS total_geometry_bytes,
        COALESCE(SUM(OCTET_LENGTH(pz.admin_codes::TEXT)),0)::BIGINT AS json_bytes,
        COALESCE(SUM(OCTET_LENGTH(pz.zone_id)+OCTET_LENGTH(pz.area_square_meters::TEXT)+
            OCTET_LENGTH(pz.admin_codes::TEXT)+OCTET_LENGTH(rz.risk_level)+96),0)::BIGINT AS projection_bytes
    FROM selected s LEFT JOIN spatial_exposure_projection_zones pz ON pz.projection_id=s.projection_id
    LEFT JOIN risk_zones rz ON rz.id=pz.zone_id AND rz.snapshot_id=s.snapshot_id
    GROUP BY s.region_code
), result_stats AS (
    SELECT COUNT(szr.zone_id)::BIGINT AS result_count,
        COALESCE(SUM(OCTET_LENGTH(szr.admin_matches::TEXT)+OCTET_LENGTH(szr.exposures::TEXT)+
            OCTET_LENGTH(szr.input_references::TEXT)+OCTET_LENGTH(szr.limitations::TEXT)),0)::BIGINT AS json_bytes
    FROM selected s LEFT JOIN spatial_exposure_projection_zones pz ON pz.projection_id=s.projection_id
    LEFT JOIN spatial_zone_results szr ON szr.analysis_id=s.id AND szr.zone_id=pz.zone_id
), feature_stats AS (
    SELECT COUNT(f.feature_id)::BIGINT AS feature_count,
        COUNT(DISTINCT f.feature_kind)::BIGINT AS feature_kind_count,
        COUNT(DISTINCT f.feature_kind) FILTER (
            WHERE f.status='available' AND f.provided=TRUE)::BIGINT AS available_feature_kind_count,
        COUNT(f.feature_id) FILTER (
            WHERE f.status<>'available' OR f.provided<>TRUE)::BIGINT AS invalid_feature_count,
        COUNT(f.feature_id) FILTER (WHERE f.feature_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM spatial_exposure_feature_zones orphan_zone
            WHERE orphan_zone.projection_id=f.projection_id AND orphan_zone.feature_id=f.feature_id
        ))::BIGINT AS orphan_feature_count,
        COALESCE(SUM(OCTET_LENGTH(f.input_references::TEXT)),0)::BIGINT AS json_bytes,
        COALESCE(SUM(OCTET_LENGTH(f.feature_id)+OCTET_LENGTH(f.feature_kind)+
            OCTET_LENGTH(f.quantity::TEXT)+OCTET_LENGTH(f.unit)+OCTET_LENGTH(f.coverage_ratio::TEXT)+
            OCTET_LENGTH(f.status)+OCTET_LENGTH(f.provided::TEXT)+OCTET_LENGTH(f.input_references::TEXT)+
            COALESCE((SELECT SUM(OCTET_LENGTH(fz.zone_id)+4) FROM spatial_exposure_feature_zones fz
                WHERE fz.projection_id=f.projection_id AND fz.feature_id=f.feature_id),0)+128),0)::BIGINT AS projection_bytes
    FROM selected s LEFT JOIN spatial_exposure_features f ON f.projection_id=s.projection_id
), reference_values AS (
    SELECT hs.raster_reference AS reference FROM selected s
        JOIN hazard_snapshots hs ON hs.id=s.snapshot_id
    UNION ALL
    SELECT hs.source->>'sourceUri' AS reference FROM selected s
        JOIN hazard_snapshots hs ON hs.id=s.snapshot_id WHERE hs.source->>'sourceUri'<>''
    UNION ALL
    SELECT parts.value->>'reference' AS reference FROM selected s
        JOIN hazard_snapshots hs ON hs.id=s.snapshot_id
        CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS(
            COALESCE(hs.source->'sourceParts','[]'::JSONB)) AS parts(value)
        WHERE parts.value->>'reference'<>''
    UNION ALL
    SELECT s.admin_boundary_reference AS reference FROM selected s
    UNION ALL
    SELECT refs.value AS reference FROM selected s
        CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS_TEXT(s.input_references) AS refs(value)
    UNION ALL
    SELECT refs.value AS reference FROM selected s
        CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS_TEXT(s.dataset_references) AS refs(value)
    UNION ALL
    SELECT refs.value AS reference FROM selected s
        JOIN spatial_exposure_features f ON f.projection_id=s.projection_id
        CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS_TEXT(f.input_references) AS refs(value)
), reference_stats AS (
	SELECT COUNT(*)::BIGINT AS reference_count,
		COUNT(DISTINCT reference)::BIGINT AS unique_reference_count FROM reference_values
), limitation_stats AS (
	SELECT COUNT(limitation.value)::BIGINT AS limitation_count,
		COALESCE(MAX(OCTET_LENGTH(limitation.value)),0)::BIGINT AS max_limitation_bytes,
		COALESCE(SUM(OCTET_LENGTH(limitation.value)),0)::BIGINT AS limitation_bytes
	FROM selected s LEFT JOIN LATERAL JSONB_ARRAY_ELEMENTS_TEXT(s.projection_limitations)
		AS limitation(value) ON TRUE
), header_stats AS (
	SELECT (OCTET_LENGTH(s.input_references::TEXT)+OCTET_LENGTH(s.dataset_references::TEXT)+
		OCTET_LENGTH(s.source_reference_digests::TEXT)+OCTET_LENGTH(s.projection_limitations::TEXT))::BIGINT AS json_bytes,
        (OCTET_LENGTH(s.id)+OCTET_LENGTH(s.snapshot_id)+OCTET_LENGTH(s.algorithm_version)+
        OCTET_LENGTH(s.status)+OCTET_LENGTH(s.union_area_square_meters::TEXT)+
		OCTET_LENGTH(s.calculated_at::TEXT)+OCTET_LENGTH(s.input_references::TEXT)+
		OCTET_LENGTH(s.dataset_references::TEXT)+OCTET_LENGTH(s.projection_id)+
        OCTET_LENGTH(s.projection_version)+OCTET_LENGTH(s.projection_digest)+
        OCTET_LENGTH(s.projection_collected_at::TEXT)+OCTET_LENGTH(s.projection_valid_from::TEXT)+
        OCTET_LENGTH(s.projection_valid_to::TEXT)+OCTET_LENGTH(s.region_code)+
        OCTET_LENGTH(s.admin_boundary_id)+OCTET_LENGTH(s.admin_boundary_digest)+
		OCTET_LENGTH(s.admin_boundary_reference)+OCTET_LENGTH(s.source_reference_digests::TEXT)+
		OCTET_LENGTH(s.projection_limitations::TEXT)+512)::BIGINT
        AS projection_bytes FROM selected s
), snapshot_stats AS (
    SELECT (OCTET_LENGTH(hs.thresholds::TEXT)+OCTET_LENGTH(hs.source::TEXT)+
        OCTET_LENGTH(hs.limitations::TEXT))::BIGINT AS json_bytes,
        (OCTET_LENGTH(hs.id)+OCTET_LENGTH(hs.hazard_type)+OCTET_LENGTH(hs.model_name)+
        OCTET_LENGTH(hs.model_version)+OCTET_LENGTH(hs.run_at::TEXT)+OCTET_LENGTH(hs.valid_from::TEXT)+
        OCTET_LENGTH(hs.valid_to::TEXT)+OCTET_LENGTH(hs.raster_reference)+
        OCTET_LENGTH(hs.probability_semantics)+OCTET_LENGTH(hs.thresholds::TEXT)+
        OCTET_LENGTH(hs.status)+OCTET_LENGTH(hs.source::TEXT)+OCTET_LENGTH(hs.limitations::TEXT)+512)::BIGINT
        AS projection_bytes FROM selected s JOIN hazard_snapshots hs ON hs.id=s.snapshot_id
)
SELECT s.id,s.algorithm_version,
    COALESCE(SUBSTRING(s.id FROM '^spatial-([0-9a-f]{64})$'),''),s.snapshot_id,s.status,
    s.region_code,s.union_area_square_meters,s.calculated_at,
    s.projection_id,s.projection_version,s.projection_digest,s.projection_collected_at,
    s.projection_valid_from,s.projection_valid_to,s.admin_boundary_id,s.admin_boundary_digest,
    s.admin_boundary_reference,s.analysis_zone_count::BIGINT,s.projection_zone_count::BIGINT,
    zone_stats.zone_count,result_stats.result_count,zone_stats.valid_admins,
    zone_stats.max_points,zone_stats.max_geometry_bytes,zone_stats.total_points,zone_stats.total_geometry_bytes,
    (snapshot_stats.json_bytes+header_stats.json_bytes+zone_stats.json_bytes+
        result_stats.json_bytes+feature_stats.json_bytes)::BIGINT,
    s.projection_feature_count::BIGINT,feature_stats.feature_count,feature_stats.feature_kind_count,
    feature_stats.available_feature_kind_count,feature_stats.invalid_feature_count,
    feature_stats.orphan_feature_count,JSONB_ARRAY_LENGTH(s.source_reference_digests)::BIGINT,
	reference_stats.reference_count,reference_stats.unique_reference_count,
	(snapshot_stats.projection_bytes+header_stats.projection_bytes+
		zone_stats.projection_bytes+feature_stats.projection_bytes)::BIGINT,
	limitation_stats.limitation_count,limitation_stats.max_limitation_bytes,
	limitation_stats.limitation_bytes
FROM selected s CROSS JOIN zone_stats CROSS JOIN result_stats CROSS JOIN feature_stats
	CROSS JOIN reference_stats CROSS JOIN limitation_stats CROSS JOIN header_stats CROSS JOIN snapshot_stats`
