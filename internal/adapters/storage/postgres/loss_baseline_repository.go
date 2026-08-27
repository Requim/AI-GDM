package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// LossBaselineRepository 使用 PostgreSQL 持久化版本化损失基线。
type LossBaselineRepository struct {
	pool *pgxpool.Pool
}

var _ ports.LossBaselineReader = (*LossBaselineRepository)(nil)
var _ ports.LossBaselineWriter = (*LossBaselineRepository)(nil)

// NewLossBaselineRepository 创建损失基线仓储适配器。
func NewLossBaselineRepository(pool *pgxpool.Pool) *LossBaselineRepository {
	return &LossBaselineRepository{pool: pool}
}

// ReplaceBaselineSet 原子替换同一数据集版本的全部基线记录。
func (r *LossBaselineRepository) ReplaceBaselineSet(ctx context.Context, set loss.BaselineSet) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("%w: 损失基线数据库连接为空", domain.ErrInvalidInput)
	}
	if err := set.Validate(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始替换损失基线事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = deleteBaselineVersion(ctx, tx, set.Version); err != nil {
		return err
	}
	if err = insertBaselineSet(ctx, tx, set); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交损失基线事务: %w", err)
	}
	return nil
}

func deleteBaselineVersion(ctx context.Context, tx pgx.Tx, version string) error {
	for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE dataset_version=$1`, version); err != nil {
			return fmt.Errorf("清理损失基线版本 %s 的 %s 记录: %w", version, table, err)
		}
	}
	return nil
}

func insertBaselineSet(ctx context.Context, tx pgx.Tx, set loss.BaselineSet) error {
	for _, value := range set.Population {
		if err := insertExposureBaseline(ctx, tx, value, set.Version); err != nil {
			return err
		}
	}
	for _, value := range set.Roads {
		if err := insertExposureBaseline(ctx, tx, value, set.Version); err != nil {
			return err
		}
	}
	for _, value := range set.Costs {
		if err := insertCostBaseline(ctx, tx, value, set.Version); err != nil {
			return err
		}
	}
	for _, value := range set.Vulnerabilities {
		if err := insertVulnerability(ctx, tx, value, set.Version); err != nil {
			return err
		}
	}
	return nil
}

// ExposureBaselines 返回某区域指定类别的最新有效暴露基线。
func (r *LossBaselineRepository) ExposureBaselines(ctx context.Context, regionCode string, kind loss.ExposureKind) ([]loss.ExposureBaseline, error) {
	if err := validateBaselineLookup(regionCode); err != nil {
		return nil, err
	}
	if kind != loss.ExposurePopulation && kind != loss.ExposureRoad {
		return nil, fmt.Errorf("%w: 暴露基线类别无效", domain.ErrInvalidInput)
	}
	rows, err := r.query(ctx, exposureBaselinesSQL, regionCode, kind)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 区域 %s 暴露基线: %w", regionCode, kind, err)
	}
	values, err := scanExposureBaselines(rows)
	if err != nil {
		return nil, err
	}
	return requireBaselineRows(values, "暴露基线")
}

// CostBaselines 返回某区域最新有效的单位成本基线。
func (r *LossBaselineRepository) CostBaselines(ctx context.Context, regionCode string) ([]loss.CostBaseline, error) {
	if err := validateBaselineLookup(regionCode); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, costBaselinesSQL, regionCode)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 成本基线: %w", regionCode, err)
	}
	values, err := scanCostBaselines(rows)
	if err != nil {
		return nil, err
	}
	return requireBaselineRows(values, "成本基线")
}

// Vulnerabilities 返回某灾种最新有效的脆弱性基线。
func (r *LossBaselineRepository) Vulnerabilities(ctx context.Context, hazardType string) ([]loss.Vulnerability, error) {
	if err := validateBaselineLookup(hazardType); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, vulnerabilitiesSQL, hazardType)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 脆弱性基线: %w", hazardType, err)
	}
	values, err := scanVulnerabilities(rows)
	if err != nil {
		return nil, err
	}
	return requireBaselineRows(values, "脆弱性基线")
}

func (r *LossBaselineRepository) query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("%w: 损失基线数据库连接为空", domain.ErrInvalidInput)
	}
	return r.pool.Query(ctx, sql, args...)
}

func validateBaselineLookup(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 {
		return fmt.Errorf("%w: 基线查询条件无效", domain.ErrInvalidInput)
	}
	return nil
}

func requireBaselineRows[T any](values []T, label string) ([]T, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s不存在: %w", label, domain.ErrNotFound)
	}
	return values, nil
}

func insertExposureBaseline(ctx context.Context, tx pgx.Tx, value loss.ExposureBaseline, version string) error {
	source, err := json.Marshal(value.Source)
	if err != nil {
		return fmt.Errorf("编码暴露基线 %s 来源: %w", value.ID, err)
	}
	_, err = tx.Exec(ctx, insertExposureBaselineSQL, version, value.ID, value.RegionCode, value.Kind,
		value.Quantity, value.Unit, value.DataYear, value.CoverageRatio, value.Source.ValidFrom,
		nullTime(value.Source.ValidTo), source)
	if err != nil {
		return fmt.Errorf("保存暴露基线 %s: %w", value.ID, err)
	}
	return nil
}

func insertCostBaseline(ctx context.Context, tx pgx.Tx, value loss.CostBaseline, version string) error {
	source, err := json.Marshal(value.Source)
	if err != nil {
		return fmt.Errorf("编码成本基线 %s 来源: %w", value.ID, err)
	}
	_, err = tx.Exec(ctx, insertCostBaselineSQL, version, value.ID, value.AssetType, value.RegionCode, value.Unit,
		value.LowCents, value.CentralCents, value.HighCents, value.Currency, value.PriceBaseDate, value.Status,
		value.ApprovedBy, value.Source.ValidFrom, nullTime(value.Source.ValidTo), source)
	if err != nil {
		return fmt.Errorf("保存成本基线 %s: %w", value.ID, err)
	}
	return nil
}

func insertVulnerability(ctx context.Context, tx pgx.Tx, value loss.Vulnerability, version string) error {
	source, err := json.Marshal(value.Source)
	if err != nil {
		return fmt.Errorf("编码脆弱性基线 %s 来源: %w", value.ID, err)
	}
	_, err = tx.Exec(ctx, insertVulnerabilitySQL, version, value.ID, value.AssetType, value.HazardType,
		value.IntensityBand, value.ImpactFractionLow, value.ImpactFractionMid, value.ImpactFractionHigh,
		value.DamageRatioLow, value.DamageRatioMid, value.DamageRatioHigh, value.CalibrationRegion, value.Status,
		value.ApprovedBy, value.Source.ValidFrom, nullTime(value.Source.ValidTo), source)
	if err != nil {
		return fmt.Errorf("保存脆弱性基线 %s: %w", value.ID, err)
	}
	return nil
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func scanExposureBaselines(rows pgx.Rows) ([]loss.ExposureBaseline, error) {
	defer rows.Close()
	values := make([]loss.ExposureBaseline, 0)
	for rows.Next() {
		value, err := scanExposureBaseline(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历暴露基线: %w", err)
	}
	return values, nil
}

func scanExposureBaseline(row rowScanner) (loss.ExposureBaseline, error) {
	var value loss.ExposureBaseline
	var version string
	var validFrom time.Time
	var validTo *time.Time
	var source []byte
	if err := row.Scan(&version, &value.ID, &value.RegionCode, &value.Kind, &value.Quantity,
		&value.Unit, &value.DataYear, &value.CoverageRatio, &validFrom, &validTo, &source); err != nil {
		return loss.ExposureBaseline{}, fmt.Errorf("扫描暴露基线: %w", err)
	}
	if err := decodeBaselineSource(&value.Source, source, version, validFrom, validTo); err != nil {
		return loss.ExposureBaseline{}, fmt.Errorf("解码暴露基线 %s 来源: %w", value.ID, err)
	}
	if err := value.Validate(); err != nil {
		return loss.ExposureBaseline{}, fmt.Errorf("校验暴露基线 %s: %w", value.ID, err)
	}
	return value, nil
}

func scanCostBaselines(rows pgx.Rows) ([]loss.CostBaseline, error) {
	defer rows.Close()
	values := make([]loss.CostBaseline, 0)
	for rows.Next() {
		value, err := scanCostBaseline(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历成本基线: %w", err)
	}
	return values, nil
}

func scanCostBaseline(row rowScanner) (loss.CostBaseline, error) {
	var value loss.CostBaseline
	var version string
	var validFrom time.Time
	var validTo *time.Time
	var source []byte
	if err := row.Scan(&version, &value.ID, &value.AssetType, &value.RegionCode, &value.Unit,
		&value.LowCents, &value.CentralCents, &value.HighCents, &value.Currency, &value.PriceBaseDate,
		&value.Status, &value.ApprovedBy, &validFrom, &validTo, &source); err != nil {
		return loss.CostBaseline{}, fmt.Errorf("扫描成本基线: %w", err)
	}
	if err := decodeBaselineSource(&value.Source, source, version, validFrom, validTo); err != nil {
		return loss.CostBaseline{}, fmt.Errorf("解码成本基线 %s 来源: %w", value.ID, err)
	}
	if err := value.Validate(); err != nil {
		return loss.CostBaseline{}, fmt.Errorf("校验成本基线 %s: %w", value.ID, err)
	}
	return value, nil
}

func scanVulnerabilities(rows pgx.Rows) ([]loss.Vulnerability, error) {
	defer rows.Close()
	values := make([]loss.Vulnerability, 0)
	for rows.Next() {
		value, err := scanVulnerability(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历脆弱性基线: %w", err)
	}
	return values, nil
}

func scanVulnerability(row rowScanner) (loss.Vulnerability, error) {
	var value loss.Vulnerability
	var version string
	var validFrom time.Time
	var validTo *time.Time
	var source []byte
	if err := row.Scan(&version, &value.ID, &value.AssetType, &value.HazardType, &value.IntensityBand,
		&value.ImpactFractionLow, &value.ImpactFractionMid, &value.ImpactFractionHigh, &value.DamageRatioLow,
		&value.DamageRatioMid, &value.DamageRatioHigh, &value.CalibrationRegion, &value.Status,
		&value.ApprovedBy, &validFrom, &validTo, &source); err != nil {
		return loss.Vulnerability{}, fmt.Errorf("扫描脆弱性基线: %w", err)
	}
	if err := decodeBaselineSource(&value.Source, source, version, validFrom, validTo); err != nil {
		return loss.Vulnerability{}, fmt.Errorf("解码脆弱性基线 %s 来源: %w", value.ID, err)
	}
	if err := value.Validate(); err != nil {
		return loss.Vulnerability{}, fmt.Errorf("校验脆弱性基线 %s: %w", value.ID, err)
	}
	return value, nil
}

func decodeBaselineSource(destination *provenance.Provenance, source []byte, version string, validFrom time.Time, validTo *time.Time) error {
	if err := json.Unmarshal(source, destination); err != nil {
		return fmt.Errorf("解码来源 JSON: %w", err)
	}
	if destination.DatasetVersion != version {
		return fmt.Errorf("%w: 数据集版本与来源不一致", domain.ErrInvalidInput)
	}
	if destination.ValidFrom.IsZero() {
		destination.ValidFrom = validFrom
	}
	if !sameBaselineTime(destination.ValidFrom, validFrom) {
		return fmt.Errorf("%w: 来源有效期开始时间不一致", domain.ErrInvalidInput)
	}
	destination.ValidFrom = validFrom
	if validTo == nil {
		if !destination.ValidTo.IsZero() {
			return fmt.Errorf("%w: 来源有效期结束时间不一致", domain.ErrInvalidInput)
		}
	} else if destination.ValidTo.IsZero() {
		destination.ValidTo = *validTo
	} else if !sameBaselineTime(destination.ValidTo, *validTo) {
		return fmt.Errorf("%w: 来源有效期结束时间不一致", domain.ErrInvalidInput)
	}
	if validTo != nil {
		destination.ValidTo = *validTo
	}
	return nil
}

func sameBaselineTime(left, right time.Time) bool {
	return left.Truncate(time.Microsecond).Equal(right.Truncate(time.Microsecond))
}

const exposureBaselinesSQL = `WITH latest AS (
    SELECT dataset_version
    FROM loss_exposure_baselines
    WHERE region_code=$1 AND exposure_kind=$2 AND valid_from <= NOW()
      AND (valid_to IS NULL OR valid_to > NOW())
    ORDER BY valid_from DESC, created_at DESC, dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.region_code,baseline.exposure_kind,baseline.quantity,baseline.unit,baseline.data_year,baseline.coverage_ratio,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_exposure_baselines baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.region_code=$1 AND baseline.exposure_kind=$2
  AND baseline.valid_from <= NOW()
  AND (baseline.valid_to IS NULL OR baseline.valid_to > NOW())
ORDER BY baseline.id`

const costBaselinesSQL = `WITH latest AS (
    SELECT dataset_version
    FROM loss_cost_baselines
    WHERE region_code=$1 AND valid_from <= NOW()
      AND (valid_to IS NULL OR valid_to > NOW())
    ORDER BY valid_from DESC, created_at DESC, dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.region_code,baseline.unit,baseline.low_cents,baseline.central_cents,baseline.high_cents,baseline.currency,baseline.price_base_date,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_cost_baselines baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.region_code=$1 AND baseline.valid_from <= NOW()
  AND (baseline.valid_to IS NULL OR baseline.valid_to > NOW())
ORDER BY baseline.id`

const vulnerabilitiesSQL = `WITH latest AS (
    SELECT dataset_version
    FROM loss_vulnerabilities
    WHERE hazard_type=$1 AND valid_from <= NOW()
      AND (valid_to IS NULL OR valid_to > NOW())
    ORDER BY valid_from DESC, created_at DESC, dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.hazard_type,baseline.intensity_band,baseline.impact_fraction_low,baseline.impact_fraction_mid,baseline.impact_fraction_high,baseline.damage_ratio_low,baseline.damage_ratio_mid,baseline.damage_ratio_high,baseline.calibration_region,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_vulnerabilities baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.hazard_type=$1 AND baseline.valid_from <= NOW()
  AND (baseline.valid_to IS NULL OR baseline.valid_to > NOW())
ORDER BY baseline.id`

const insertExposureBaselineSQL = `INSERT INTO loss_exposure_baselines (dataset_version,id,region_code,exposure_kind,quantity,unit,data_year,coverage_ratio,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

const insertCostBaselineSQL = `INSERT INTO loss_cost_baselines (dataset_version,id,asset_type,region_code,unit,low_cents,central_cents,high_cents,currency,price_base_date,status,approved_by,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`

const insertVulnerabilitySQL = `INSERT INTO loss_vulnerabilities (dataset_version,id,asset_type,hazard_type,intensity_band,impact_fraction_low,impact_fraction_mid,impact_fraction_high,damage_ratio_low,damage_ratio_mid,damage_ratio_high,calibration_region,status,approved_by,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`
