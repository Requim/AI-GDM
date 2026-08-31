package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const maxExposureZoneBindings = 10_000

var _ exposurecollection.ProjectionWriter = (*HazardRepository)(nil)

type storedExposureHeader struct {
	ID, AnalysisID, Version, Digest                           string
	Status                                                    string
	RegionCode, BoundaryID, BoundaryDigest, BoundaryReference string
	CollectedAt, ValidFrom, ValidTo                           time.Time
	UnionArea                                                 float64
	Complete                                                  bool
	ZoneCount, FeatureCount                                   int
	InputReferences, DatasetReferences                        []string
	SourceDigests                                             []string
	Limitations                                               []string
}

// SaveExposureProjection 原子追加完整投影；已完成同内容仅幂等返回。
func (r *HazardRepository) SaveExposureProjection(ctx context.Context,
	value exposurecollection.ExposureProjection,
) error {
	applicationloss.CanonicalizeRiskProjectionTimes(&value.Input)
	value.ValidFrom = value.ValidFrom.UTC().Truncate(time.Microsecond)
	value.ValidTo = value.ValidTo.UTC().Truncate(time.Microsecond)
	if err := validateExposureProjection(r, value); err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = r.saveExposureProjectionTx(ctx, value)
		if last == nil || !serializationFailure(last) {
			return last
		}
	}
	return fmt.Errorf("保存暴露投影并发重试耗尽: %w", last)
}

func (r *HazardRepository) saveExposureProjectionTx(ctx context.Context,
	value exposurecollection.ExposureProjection,
) error {
	tx, err := r.pool.BeginTx(ctx, exposureWriteOptions)
	if err != nil {
		return fmt.Errorf("开始保存暴露投影事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := insertExposureHeader(ctx, tx, value)
	if err != nil {
		return err
	}
	if !inserted {
		complete, prepareErr := prepareExistingExposure(ctx, tx, value)
		if prepareErr != nil {
			return prepareErr
		}
		if complete {
			return commitExposureTransaction(ctx, tx, "提交暴露投影幂等事务")
		}
		if inserted, err = insertExposureHeader(ctx, tx, value); err != nil || !inserted {
			return fmt.Errorf("重建未完成暴露投影头: %w",
				errors.Join(domain.ErrInvalidInput, err))
		}
	}
	if err = insertExposureZones(ctx, tx, value); err != nil {
		return err
	}
	if err = insertExposureFeatures(ctx, tx, value); err != nil {
		return err
	}
	if err = completeExposureProjection(ctx, tx, value); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交暴露投影事务: %w", err)
	}
	return nil
}

func prepareExistingExposure(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) (bool, error) {
	header, err := readStoredExposureHeaderForUpdate(ctx, tx, value.Input.Analysis.ProjectionID)
	if err != nil {
		return false, err
	}
	if !sameExposureHeaderContent(header, value) {
		return false, fmt.Errorf("%w: 已存在暴露投影头内容冲突", domain.ErrInvalidInput)
	}
	if header.Complete {
		matches, matchErr := existingExposureChildrenMatch(ctx, tx, value)
		if matchErr != nil || !matches {
			return false, errors.Join(matchErr,
				fmt.Errorf("%w: 已存在完整暴露投影内容冲突", domain.ErrInvalidInput))
		}
		return true, nil
	}
	result, err := tx.Exec(ctx, deleteIncompleteExposureSQL, header.ID)
	if err != nil || result.RowsAffected() != 1 {
		return false, fmt.Errorf("清理未完成暴露投影: %w",
			errors.Join(domain.ErrInvalidInput, err))
	}
	return false, nil
}

func commitExposureTransaction(ctx context.Context, tx pgx.Tx, operation string) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func serializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func validateExposureProjection(r *HazardRepository,
	value exposurecollection.ExposureProjection,
) error {
	input, analysis := value.Input, value.Input.Analysis
	if r == nil || r.pool == nil || !value.ValidFrom.Equal(analysis.ProjectionValidFrom) ||
		!value.ValidTo.Equal(analysis.ProjectionValidTo) || value.ValidFrom.IsZero() ||
		!value.ValidTo.After(value.ValidFrom) || analysis.ProjectionCollectedAt.Before(value.ValidFrom) ||
		!analysis.ProjectionCollectedAt.Before(value.ValidTo) {
		return fmt.Errorf("%w: 暴露投影窗口或仓储无效", domain.ErrInvalidInput)
	}
	if analysis.RegionCode != "CN" || !strings.HasPrefix(analysis.AdminBoundaryID, "CHN-ADM0-") ||
		analysis.AdminBoundaryDigest == "" || analysis.AdminBoundaryReference == "" ||
		!finitePositive(analysis.TotalAreaSquareMeters) || len(input.Zones) == 0 ||
		len(input.Zones) > exposurecollection.MaxRiskZones || len(analysis.Features) == 0 ||
		len(analysis.Features) > exposurecollection.MaxInfrastructure+2 {
		return fmt.Errorf("%w: 暴露投影行政或数量契约无效", domain.ErrInvalidInput)
	}
	if len(analysis.InputReferences) == 0 || len(analysis.InputReferences) > 1000 ||
		len(analysis.DatasetReferences) == 0 || len(analysis.DatasetReferences) > 1000 ||
		!strictExposureStrings(analysis.InputReferences) || !strictExposureStrings(analysis.DatasetReferences) {
		return fmt.Errorf("%w: 暴露投影来源引用无效", domain.ErrInvalidInput)
	}
	if err := validateExposureProjectionRows(input); err != nil {
		return err
	}
	if err := applicationloss.ValidateRiskProjectionIdentity(input); err != nil {
		return fmt.Errorf("暴露投影身份无效: %w", errors.Join(domain.ErrInvalidInput, err))
	}
	return nil
}

func validateExposureProjectionRows(value applicationloss.LossInputProjection) error {
	zoneSet := make(map[string]struct{}, len(value.Zones))
	zoneArea, largestZone := 0.0, 0.0
	for index, zone := range value.Zones {
		if !validExposureIdentifier(zone.ID) || !zone.AreaCalculated || !finitePositive(zone.AreaSquareM) ||
			len(zone.AdminCodes) != 1 || zone.AdminCodes[0] != "CN" ||
			zone.SnapshotID != value.Snapshot.ID || !validExposureRiskLevel(zone.Level) ||
			(index > 0 && zone.ID <= value.Zones[index-1].ID) {
			return fmt.Errorf("%w: 暴露投影风险区无效", domain.ErrInvalidInput)
		}
		zoneSet[zone.ID] = struct{}{}
		zoneArea += zone.AreaSquareM
		largestZone = math.Max(largestZone, zone.AreaSquareM)
	}
	tolerance := math.Max(1e-6, zoneArea*1e-9)
	if value.Analysis.TotalAreaSquareMeters > zoneArea+tolerance ||
		value.Analysis.TotalAreaSquareMeters < largestZone-tolerance {
		return fmt.Errorf("%w: 暴露投影联合面积不在合法区间", domain.ErrInvalidInput)
	}
	bindings, previous := 0, ""
	seen := make(map[string]struct{}, len(value.Analysis.Features))
	for _, feature := range value.Analysis.Features {
		key := string(feature.Kind) + "\x00" + feature.FeatureID
		if key <= previous || !validExposureFeatureRow(feature, zoneSet) {
			return fmt.Errorf("%w: 暴露投影 feature 无效", domain.ErrInvalidInput)
		}
		if _, exists := seen[feature.FeatureID]; exists {
			return fmt.Errorf("%w: 暴露投影 featureId 未全局唯一", domain.ErrInvalidInput)
		}
		seen[feature.FeatureID] = struct{}{}
		bindings, previous = bindings+len(feature.ZoneIDs), key
	}
	if bindings <= 0 || bindings > maxExposureZoneBindings {
		return fmt.Errorf("%w: 暴露投影 zone 绑定超过预算", domain.ErrInvalidInput)
	}
	return nil
}

func validExposureFeatureRow(value applicationloss.LossExposureFeature,
	zones map[string]struct{},
) bool {
	if !validExposureIdentifier(value.FeatureID) || !value.Provided || value.Status != "available" ||
		len(value.ZoneIDs) == 0 || len(value.InputReferences) == 0 ||
		len(value.InputReferences) > exposurecollection.MaxProviderReferences ||
		math.IsNaN(value.Quantity) || math.IsInf(value.Quantity, 0) || value.Quantity < 0 ||
		math.IsNaN(value.CoverageRatio) || math.IsInf(value.CoverageRatio, 0) ||
		value.CoverageRatio <= 0 || value.CoverageRatio > 1 || !validExposureUnit(value) ||
		!strictExposureStrings(value.InputReferences) {
		return false
	}
	if value.Kind == applicationloss.LossFeatureFacility && value.Quantity != math.Trunc(value.Quantity) {
		return false
	}
	for index, zoneID := range value.ZoneIDs {
		if _, exists := zones[zoneID]; !exists || (index > 0 && zoneID <= value.ZoneIDs[index-1]) {
			return false
		}
	}
	return true
}

func validExposureUnit(value applicationloss.LossExposureFeature) bool {
	return (value.Kind == applicationloss.LossFeaturePopulation && value.Unit == "people") ||
		(value.Kind == applicationloss.LossFeatureRoad && value.Unit == "meters") ||
		(value.Kind == applicationloss.LossFeatureFacility && value.Unit == "count")
}

func strictExposureStrings(values []string) bool {
	for index, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 4096 ||
			unsafeExposureText(value) || (index > 0 && value <= values[index-1]) {
			return false
		}
	}
	return true
}

func unsafeExposureText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return true
		}
	}
	return false
}

func insertExposureHeader(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) (bool, error) {
	analysis := value.Input.Analysis
	inputs, err := json.Marshal(analysis.InputReferences)
	if err != nil {
		return false, fmt.Errorf("编码暴露投影输入引用: %w", err)
	}
	datasets, err := json.Marshal(analysis.DatasetReferences)
	if err != nil {
		return false, fmt.Errorf("编码暴露投影数据集引用: %w", err)
	}
	sources, err := json.Marshal(analysis.SourceReferenceDigests)
	if err != nil {
		return false, fmt.Errorf("编码暴露投影来源摘要: %w", err)
	}
	limitations, err := json.Marshal(analysis.ProjectionLimitations)
	if err != nil {
		return false, fmt.Errorf("编码暴露投影限制: %w", err)
	}
	result, err := tx.Exec(ctx, insertExposureHeaderSQL, analysis.ProjectionID, analysis.ID,
		analysis.ProjectionVersion, analysis.ProjectionDigest, analysis.Status,
		analysis.ProjectionCollectedAt, value.ValidFrom, value.ValidTo, analysis.RegionCode, analysis.TotalAreaSquareMeters,
		analysis.AdminBoundaryID, analysis.AdminBoundaryDigest, analysis.AdminBoundaryReference,
		len(value.Input.Zones), len(analysis.Features), inputs, datasets, sources, limitations)
	if err != nil {
		return false, fmt.Errorf("插入暴露投影头: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func insertExposureZones(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) error {
	analysis := value.Input.Analysis
	for _, zone := range value.Input.Zones {
		codes, err := json.Marshal(zone.AdminCodes)
		if err != nil {
			return fmt.Errorf("编码暴露投影行政代码: %w", err)
		}
		if _, err = tx.Exec(ctx, insertExposureZoneSQL, analysis.ProjectionID, analysis.ID,
			zone.ID, zone.AreaSquareM, codes); err != nil {
			return fmt.Errorf("插入暴露投影风险区 %s: %w", zone.ID, err)
		}
	}
	return nil
}

func insertExposureFeatures(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) error {
	analysis := value.Input.Analysis
	for _, feature := range analysis.Features {
		references, err := json.Marshal(feature.InputReferences)
		if err != nil {
			return fmt.Errorf("编码暴露 feature 来源: %w", err)
		}
		if _, err = tx.Exec(ctx, insertExposureFeatureSQL, analysis.ProjectionID, analysis.ID,
			feature.FeatureID, feature.Kind, feature.Quantity, feature.Unit, feature.CoverageRatio,
			feature.Status, feature.Provided, references); err != nil {
			return fmt.Errorf("插入暴露 feature %s: %w", feature.FeatureID, err)
		}
		if err = insertExposureBindings(ctx, tx, analysis, feature); err != nil {
			return err
		}
	}
	return nil
}

func insertExposureBindings(ctx context.Context, tx pgx.Tx,
	analysis applicationloss.LossSpatialProjection,
	feature applicationloss.LossExposureFeature,
) error {
	for _, zoneID := range feature.ZoneIDs {
		if _, err := tx.Exec(ctx, insertExposureBindingSQL, analysis.ProjectionID, analysis.ID,
			feature.FeatureID, zoneID); err != nil {
			return fmt.Errorf("插入暴露 feature %s 风险区绑定: %w", feature.FeatureID, err)
		}
	}
	return nil
}

func completeExposureProjection(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) error {
	analysis := value.Input.Analysis
	result, err := tx.Exec(ctx, completeExposureProjectionSQL, analysis.ProjectionID,
		len(value.Input.Zones), len(analysis.Features), exposureBindingCount(analysis.Features),
		analysis.ProjectionDigest)
	if err != nil {
		return fmt.Errorf("完成暴露投影: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: 暴露投影子行计数不完整", domain.ErrInvalidInput)
	}
	return nil
}

func existingExposureChildrenMatch(ctx context.Context, tx pgx.Tx,
	value exposurecollection.ExposureProjection,
) (bool, error) {
	zones, err := readStoredExposureZones(ctx, tx, value.Input.Analysis.ProjectionID)
	if err != nil {
		return false, err
	}
	features, err := readLossProjectionFeatures(ctx, tx, value.Input.Analysis.ProjectionID)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(zones, value.Input.Zones) &&
		reflect.DeepEqual(features, value.Input.Analysis.Features), nil
}

func readStoredExposureHeaderForUpdate(ctx context.Context, tx pgx.Tx,
	projectionID string,
) (storedExposureHeader, error) {
	return scanStoredExposureHeader(tx.QueryRow(ctx, selectExposureHeaderForUpdateSQL, projectionID))
}

func readStoredExposureHeader(ctx context.Context, tx pgx.Tx,
	projectionID string,
) (storedExposureHeader, error) {
	return scanStoredExposureHeader(tx.QueryRow(ctx, selectExposureHeaderSQL, projectionID))
}

func scanStoredExposureHeader(row pgx.Row) (storedExposureHeader, error) {
	var value storedExposureHeader
	var inputs, datasets, sources, limitations []byte
	err := row.Scan(&value.ID, &value.AnalysisID,
		&value.Version, &value.Digest, &value.Status, &value.CollectedAt, &value.ValidFrom, &value.ValidTo,
		&value.RegionCode, &value.UnionArea, &value.BoundaryID, &value.BoundaryDigest,
		&value.BoundaryReference, &value.Complete, &value.ZoneCount, &value.FeatureCount,
		&inputs, &datasets, &sources, &limitations)
	if err != nil {
		return value, fmt.Errorf("读取已存在暴露投影头: %w", err)
	}
	if value.InputReferences, err = decodeExposureStrings(inputs); err != nil {
		return value, err
	}
	if value.DatasetReferences, err = decodeExposureStrings(datasets); err != nil {
		return value, err
	}
	if value.SourceDigests, err = decodeExposureStrings(sources); err != nil {
		return value, err
	}
	if value.Limitations, err = decodeExposureStrings(limitations); err != nil {
		return value, err
	}
	return value, nil
}

func readStoredExposureZones(ctx context.Context, tx pgx.Tx,
	projectionID string,
) ([]applicationloss.LossRiskZone, error) {
	rows, err := tx.Query(ctx, selectExposureZonesSQL, projectionID)
	if err != nil {
		return nil, fmt.Errorf("读取已存在暴露投影风险区: %w", err)
	}
	defer rows.Close()
	values := make([]applicationloss.LossRiskZone, 0)
	for rows.Next() {
		var value applicationloss.LossRiskZone
		var level string
		var codes []byte
		if err = rows.Scan(&value.ID, &value.SnapshotID, &level, &value.AreaSquareM, &codes); err != nil {
			return nil, fmt.Errorf("扫描已存在暴露投影风险区: %w", err)
		}
		value.Level, value.AreaCalculated = hazardLevel(level), true
		if value.AdminCodes, err = decodeExposureStrings(codes); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func sameExposureHeaderContent(stored storedExposureHeader,
	value exposurecollection.ExposureProjection,
) bool {
	analysis := value.Input.Analysis
	return stored.ID == analysis.ProjectionID && stored.AnalysisID == analysis.ID &&
		stored.Version == analysis.ProjectionVersion && stored.Digest == analysis.ProjectionDigest &&
		stored.Status == string(analysis.Status) &&
		stored.CollectedAt.Equal(analysis.ProjectionCollectedAt) && stored.ValidFrom.Equal(value.ValidFrom) &&
		stored.ValidTo.Equal(value.ValidTo) && stored.RegionCode == analysis.RegionCode &&
		stored.UnionArea == analysis.TotalAreaSquareMeters && stored.BoundaryID == analysis.AdminBoundaryID &&
		stored.BoundaryDigest == analysis.AdminBoundaryDigest &&
		stored.BoundaryReference == analysis.AdminBoundaryReference && stored.ZoneCount == len(value.Input.Zones) &&
		stored.FeatureCount == len(analysis.Features) &&
		reflect.DeepEqual(stored.InputReferences, analysis.InputReferences) &&
		reflect.DeepEqual(stored.DatasetReferences, analysis.DatasetReferences) &&
		reflect.DeepEqual(stored.SourceDigests, analysis.SourceReferenceDigests) &&
		reflect.DeepEqual(stored.Limitations, analysis.ProjectionLimitations)
}

func exposureBindingCount(values []applicationloss.LossExposureFeature) int {
	total := 0
	for _, value := range values {
		total += len(value.ZoneIDs)
	}
	return total
}

func hazardLevel(value string) hazard.RiskLevel { return hazard.RiskLevel(value) }

func validExposureRiskLevel(value hazard.RiskLevel) bool {
	return value == hazard.RiskLow || value == hazard.RiskModerate ||
		value == hazard.RiskHigh || value == hazard.RiskVeryHigh
}

var exposureWriteOptions = pgx.TxOptions{IsoLevel: pgx.Serializable}

const insertExposureHeaderSQL = `INSERT INTO spatial_exposure_projections(
    id,analysis_id,projection_version,projection_digest,projection_status,collected_at,valid_from,valid_to,
    region_code,union_area_square_meters,admin_boundary_id,admin_boundary_digest,
    admin_boundary_reference,complete,zone_count,feature_count,input_references,dataset_references,
    source_reference_digests,limitations
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,FALSE,$14,$15,$16,$17,$18,$19)
ON CONFLICT (id) DO NOTHING`

const insertExposureZoneSQL = `INSERT INTO spatial_exposure_projection_zones(
    projection_id,analysis_id,zone_id,area_square_meters,admin_codes
) VALUES($1,$2,$3,$4,$5)`

const insertExposureFeatureSQL = `INSERT INTO spatial_exposure_features(
    projection_id,analysis_id,feature_id,feature_kind,quantity,unit,coverage_ratio,
    status,provided,input_references
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`

const insertExposureBindingSQL = `INSERT INTO spatial_exposure_feature_zones(
    projection_id,analysis_id,feature_id,zone_id
) VALUES($1,$2,$3,$4)`

const completeExposureProjectionSQL = `UPDATE spatial_exposure_projections p SET complete=TRUE
WHERE p.id=$1 AND p.complete=FALSE AND p.zone_count=$2 AND p.feature_count=$3
AND (SELECT COUNT(*) FROM spatial_exposure_projection_zones z WHERE z.projection_id=p.id)=$2
AND (SELECT COUNT(*) FROM spatial_exposure_features f WHERE f.projection_id=p.id)=$3
AND (SELECT COUNT(*) FROM spatial_exposure_feature_zones b WHERE b.projection_id=p.id)=$4
AND p.projection_digest=$5 AND p.id='exposure-' || $5`

const selectExposureHeaderSQL = `SELECT id,analysis_id,projection_version,projection_digest,
    projection_status,collected_at,valid_from,valid_to,region_code,union_area_square_meters,admin_boundary_id,
	admin_boundary_digest,admin_boundary_reference,complete,zone_count,feature_count,
	input_references,dataset_references,source_reference_digests,limitations
	FROM spatial_exposure_projections WHERE id=$1`

const selectExposureHeaderForUpdateSQL = selectExposureHeaderSQL + ` FOR UPDATE`

const deleteIncompleteExposureSQL = `DELETE FROM spatial_exposure_projections
WHERE id=$1 AND complete=FALSE`

const selectExposureZonesSQL = `SELECT pz.zone_id,rz.snapshot_id,rz.risk_level,
    pz.area_square_meters,pz.admin_codes FROM spatial_exposure_projection_zones pz
    JOIN risk_zones rz ON rz.id=pz.zone_id
    WHERE pz.projection_id=$1 ORDER BY pz.zone_id`
