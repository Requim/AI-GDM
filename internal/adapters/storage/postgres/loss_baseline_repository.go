package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// LossBaselineRepository 使用 PostgreSQL 持久化版本化损失基线。
type LossBaselineRepository struct {
	pool      *pgxpool.Pool
	readStore baselineReadStore
}

var baselineReadOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

type baselineReadStore interface {
	BeginTx(context.Context, pgx.TxOptions) (baselineReadTransaction, error)
}

type baselineReadTransaction interface {
	QueryRow(context.Context, string, ...any) rowScanner
	Query(context.Context, string, ...any) (baselineRows, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type baselineRows interface {
	rowScanner
	Close()
	Next() bool
	Err() error
}

type pgxBaselineReadStore struct {
	pool *pgxpool.Pool
}

func (s pgxBaselineReadStore) BeginTx(ctx context.Context, options pgx.TxOptions) (baselineReadTransaction, error) {
	tx, err := s.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return pgxBaselineReadTransaction{tx: tx}, nil
}

type pgxBaselineReadTransaction struct {
	tx pgx.Tx
}

func (t pgxBaselineReadTransaction) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t pgxBaselineReadTransaction) Query(ctx context.Context, sql string, args ...any) (baselineRows, error) {
	return t.tx.Query(ctx, sql, args...)
}

func (t pgxBaselineReadTransaction) Commit(ctx context.Context) error {
	return t.tx.Commit(ctx)
}

func (t pgxBaselineReadTransaction) Rollback(ctx context.Context) error {
	return t.tx.Rollback(ctx)
}

var _ ports.LossBaselineReader = (*LossBaselineRepository)(nil)
var _ ports.LossBaselineWriter = (*LossBaselineRepository)(nil)

// NewLossBaselineRepository 创建损失基线仓储适配器。
func NewLossBaselineRepository(pool *pgxpool.Pool) *LossBaselineRepository {
	repository := &LossBaselineRepository{pool: pool}
	if pool != nil {
		repository.readStore = pgxBaselineReadStore{pool: pool}
	}
	return repository
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
	rows, err := r.query(ctx, exposureBaselinesSQL, regionCode, kind, baselineReadTime())
	if err != nil {
		return nil, fmt.Errorf("查询 %s 区域 %s 暴露基线: %w", regionCode, kind, err)
	}
	values, err := scanExposureBaselines(rows)
	if err != nil {
		return nil, err
	}
	return selectExposureRegion(values, regionCode, "暴露基线")
}

// CostBaselines 返回某区域最新有效的单位成本基线。
func (r *LossBaselineRepository) CostBaselines(ctx context.Context, regionCode string) ([]loss.CostBaseline, error) {
	if err := validateBaselineLookup(regionCode); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, costBaselinesSQL, regionCode, baselineReadTime())
	if err != nil {
		return nil, fmt.Errorf("查询 %s 成本基线: %w", regionCode, err)
	}
	values, err := scanCostBaselines(rows)
	if err != nil {
		return nil, err
	}
	return selectCostRegions(values, regionCode)
}

// Vulnerabilities 返回某灾种最新有效的脆弱性基线。
func (r *LossBaselineRepository) Vulnerabilities(ctx context.Context, hazardType string) ([]loss.Vulnerability, error) {
	if err := validateBaselineLookup(hazardType); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, vulnerabilitiesSQL, hazardType, baselineReadTime())
	if err != nil {
		return nil, fmt.Errorf("查询 %s 脆弱性基线: %w", hazardType, err)
	}
	values, err := scanVulnerabilities(rows)
	if err != nil {
		return nil, err
	}
	return requireBaselineRows(values, "脆弱性基线")
}

// BaselineSet 在单个可重复读事务中返回覆盖权威语义键的完整损失基线集合。
func (r *LossBaselineRepository) BaselineSet(ctx context.Context,
	query applicationloss.BaselineQuery,
) (set loss.BaselineSet, err error) {
	if err := validateBaselineLookup(query.RegionCode); err != nil {
		return loss.BaselineSet{}, err
	}
	if err := validateBaselineLookup(query.HazardType); err != nil {
		return loss.BaselineSet{}, err
	}
	if err := query.Requirements.Validate(); err != nil {
		return loss.BaselineSet{}, err
	}
	query.At, err = normalizeBaselineReadTime(query.At)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if r == nil || r.readStore == nil {
		return loss.BaselineSet{}, fmt.Errorf("%w: 损失基线数据库连接为空", domain.ErrInvalidInput)
	}
	tx, err := r.readStore.BeginTx(ctx, baselineReadOptions)
	if err != nil {
		return loss.BaselineSet{}, fmt.Errorf("开始读取完整损失基线事务: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = joinBaselineRollbackError(err, tx.Rollback(ctx))
		}
	}()
	set, err = readBaselineSet(ctx, tx, query)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return loss.BaselineSet{}, fmt.Errorf("提交完整损失基线读取事务: %w", err)
	}
	committed = true
	return set, nil
}

func readBaselineSet(ctx context.Context, tx baselineReadTransaction,
	query applicationloss.BaselineQuery,
) (loss.BaselineSet, error) {
	costAssets, costUnits, vulnerabilityAssets, intensities := baselineRequirementColumns(query.Requirements)
	var version string
	err := tx.QueryRow(ctx, baselineSetVersionSQL, query.RegionCode, query.HazardType, query.At,
		costAssets, costUnits, vulnerabilityAssets, intensities).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return loss.BaselineSet{}, fmt.Errorf("完整损失基线不存在: %w", domain.ErrNotFound)
	}
	if err != nil {
		return loss.BaselineSet{}, fmt.Errorf("选择完整损失基线版本: %w", err)
	}
	if err = validateBaselineLookup(version); err != nil {
		return loss.BaselineSet{}, fmt.Errorf("校验完整损失基线版本: %w", err)
	}
	population, err := queryExposureSet(ctx, tx, version, query.RegionCode, loss.ExposurePopulation, query.At)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if _, err = requireBaselineRows(population, "人口暴露基线"); err != nil {
		return loss.BaselineSet{}, err
	}
	roads, err := queryExposureSet(ctx, tx, version, query.RegionCode, loss.ExposureRoad, query.At)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if _, err = requireBaselineRows(roads, "道路暴露基线"); err != nil {
		return loss.BaselineSet{}, err
	}
	costs, err := queryCostSet(ctx, tx, version, query.RegionCode, query.At, query.Requirements.Costs)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if _, err = requireBaselineRows(costs, "成本基线"); err != nil {
		return loss.BaselineSet{}, err
	}
	vulnerabilities, err := queryVulnerabilitySet(ctx, tx, version, query.HazardType,
		query.RegionCode, query.At, query.Requirements.Vulnerabilities)
	if err != nil {
		return loss.BaselineSet{}, err
	}
	if _, err = requireBaselineRows(vulnerabilities, "脆弱性基线"); err != nil {
		return loss.BaselineSet{}, err
	}
	set := loss.BaselineSet{Version: version, Population: population, Roads: roads,
		Costs: costs, Vulnerabilities: vulnerabilities}
	if err = set.Validate(); err != nil {
		return loss.BaselineSet{}, fmt.Errorf("校验完整损失基线 %s: %w", version, err)
	}
	return set, nil
}

func joinBaselineRollbackError(primary, rollback error) error {
	if rollback == nil || errors.Is(rollback, pgx.ErrTxClosed) {
		return primary
	}
	rollback = fmt.Errorf("回滚完整损失基线读取事务: %w", rollback)
	if primary == nil {
		return rollback
	}
	return errors.Join(primary, rollback)
}

func queryExposureSet(ctx context.Context, tx baselineReadTransaction, version, regionCode string,
	kind loss.ExposureKind, now time.Time,
) ([]loss.ExposureBaseline, error) {
	rows, err := tx.Query(ctx, exposureBaselinesByVersionSQL, version, regionCode, kind, now)
	if err != nil {
		return nil, fmt.Errorf("查询版本 %s 的 %s 暴露基线: %w", version, kind, err)
	}
	values, err := scanExposureBaselines(rows)
	if err != nil {
		return nil, err
	}
	return selectExposureRegion(values, regionCode, string(kind)+" 暴露基线")
}

func queryCostSet(ctx context.Context, tx baselineReadTransaction, version,
	regionCode string, now time.Time, requirements []applicationloss.CostBaselineRequirement,
) ([]loss.CostBaseline, error) {
	assets, units := costRequirementColumns(requirements)
	rows, err := tx.Query(ctx, costBaselinesByVersionSQL, version, regionCode, now, assets, units)
	if err != nil {
		return nil, fmt.Errorf("查询版本 %s 的成本基线: %w", version, err)
	}
	values, err := scanCostBaselines(rows)
	if err != nil {
		return nil, err
	}
	selected, err := selectCostRegions(values, regionCode)
	if err != nil {
		return nil, err
	}
	return requireCostCoverage(selected, requirements)
}

func queryVulnerabilitySet(ctx context.Context, tx baselineReadTransaction, version,
	hazardType, regionCode string, now time.Time,
	requirements []applicationloss.VulnerabilityBaselineRequirement,
) ([]loss.Vulnerability, error) {
	assets, intensities := vulnerabilityRequirementColumns(requirements)
	rows, err := tx.Query(ctx, vulnerabilitiesByVersionSQL, version, hazardType, regionCode, now,
		assets, intensities)
	if err != nil {
		return nil, fmt.Errorf("查询版本 %s 的脆弱性基线: %w", version, err)
	}
	values, err := scanVulnerabilities(rows)
	if err != nil {
		return nil, err
	}
	selected, err := selectVulnerabilityRegions(values, regionCode)
	if err != nil {
		return nil, err
	}
	return requireVulnerabilityCoverage(selected, requirements)
}

func baselineRequirementColumns(requirements applicationloss.BaselineRequirements) (
	[]string, []string, []string, []string,
) {
	costAssets, costUnits := costRequirementColumns(requirements.Costs)
	vulnerabilityAssets, intensities := vulnerabilityRequirementColumns(requirements.Vulnerabilities)
	return costAssets, costUnits, vulnerabilityAssets, intensities
}

func costRequirementColumns(values []applicationloss.CostBaselineRequirement) ([]string, []string) {
	assets, units := make([]string, len(values)), make([]string, len(values))
	for index, value := range values {
		assets[index], units[index] = string(value.AssetType), value.Unit
	}
	return assets, units
}

func vulnerabilityRequirementColumns(values []applicationloss.VulnerabilityBaselineRequirement) (
	[]string, []string,
) {
	assets, intensities := make([]string, len(values)), make([]string, len(values))
	for index, value := range values {
		assets[index], intensities[index] = string(value.AssetType), value.IntensityBand
	}
	return assets, intensities
}

func (r *LossBaselineRepository) query(ctx context.Context, sql string, args ...any) (baselineRows, error) {
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

func normalizeBaselineReadTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("%w: 基线读取时间为空", domain.ErrInvalidInput)
	}
	return value.UTC().Truncate(time.Microsecond), nil
}

func baselineReadTime() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func selectExposureRegion(values []loss.ExposureBaseline, regionCode,
	label string,
) ([]loss.ExposureBaseline, error) {
	exact, national := make([]loss.ExposureBaseline, 0, 1), make([]loss.ExposureBaseline, 0, 1)
	for _, value := range values {
		if value.RegionCode == regionCode {
			exact = append(exact, value)
		} else if value.RegionCode == "CN" {
			national = append(national, value)
		}
	}
	selected := national
	if len(exact) > 0 {
		selected = exact
	}
	if len(selected) == 0 {
		return requireBaselineRows(selected, label)
	}
	if len(selected) > 1 {
		return nil, invalidBaselineCandidates(label, len(selected))
	}
	return selected, nil
}

func selectCostRegions(values []loss.CostBaseline, regionCode string) ([]loss.CostBaseline, error) {
	groups, keys := make(map[string]*costRegionCandidates), make([]string, 0)
	for _, value := range values {
		if value.Status != loss.BaselineApproved {
			return nil, invalidBaselineCandidates("成本基线状态", 0)
		}
		key := costRequirementKey(value.AssetType, value.Unit)
		group := groups[key]
		if group == nil {
			group, keys = &costRegionCandidates{}, append(keys, key)
			groups[key] = group
		}
		group.add(value, regionCode)
	}
	sort.Strings(keys)
	selected := make([]loss.CostBaseline, 0, len(keys))
	for _, key := range keys {
		value, err := groups[key].selected("成本基线 " + key)
		if err != nil {
			return nil, err
		}
		selected = append(selected, value)
	}
	return requireBaselineRows(selected, "成本基线")
}

func requireCostCoverage(values []loss.CostBaseline,
	requirements []applicationloss.CostBaselineRequirement,
) ([]loss.CostBaseline, error) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[costRequirementKey(value.AssetType, value.Unit)] = struct{}{}
	}
	for _, requirement := range requirements {
		if _, exists := seen[costRequirementKey(requirement.AssetType, requirement.Unit)]; !exists {
			return nil, fmt.Errorf("成本基线未覆盖 %s/%s: %w", requirement.AssetType,
				requirement.Unit, domain.ErrNotFound)
		}
	}
	return values, nil
}

func costRequirementKey(asset loss.AssetType, unit string) string {
	return string(asset) + "\x00" + unit
}

type costRegionCandidates struct {
	exact    []loss.CostBaseline
	national []loss.CostBaseline
}

func (c *costRegionCandidates) add(value loss.CostBaseline, regionCode string) {
	if value.RegionCode == regionCode {
		c.exact = append(c.exact, value)
	} else if value.RegionCode == "CN" {
		c.national = append(c.national, value)
	}
}

func (c costRegionCandidates) selected(label string) (loss.CostBaseline, error) {
	values := c.national
	if len(c.exact) > 0 {
		values = c.exact
	}
	if len(values) != 1 {
		return loss.CostBaseline{}, invalidBaselineCandidates(label, len(values))
	}
	return values[0], nil
}

func selectVulnerabilityRegions(values []loss.Vulnerability,
	regionCode string,
) ([]loss.Vulnerability, error) {
	groups, keys := make(map[string]*vulnerabilityRegionCandidates), make([]string, 0)
	for _, value := range values {
		if value.Status != loss.BaselineApproved {
			return nil, invalidBaselineCandidates("脆弱性基线状态", 0)
		}
		key := string(value.AssetType) + "\x00" + value.IntensityBand
		group := groups[key]
		if group == nil {
			group, keys = &vulnerabilityRegionCandidates{}, append(keys, key)
			groups[key] = group
		}
		group.add(value, regionCode)
	}
	sort.Strings(keys)
	selected := make([]loss.Vulnerability, 0, len(keys))
	for _, key := range keys {
		value, err := groups[key].selected("脆弱性基线 " + key)
		if err != nil {
			return nil, err
		}
		selected = append(selected, value)
	}
	return requireBaselineRows(selected, "脆弱性基线")
}

func requireVulnerabilityCoverage(values []loss.Vulnerability,
	requirements []applicationloss.VulnerabilityBaselineRequirement,
) ([]loss.Vulnerability, error) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[vulnerabilityRequirementKey(value.AssetType, value.IntensityBand)] = struct{}{}
	}
	for _, requirement := range requirements {
		key := vulnerabilityRequirementKey(requirement.AssetType, requirement.IntensityBand)
		if _, exists := seen[key]; !exists {
			return nil, fmt.Errorf("脆弱性基线未覆盖 %s/%s: %w", requirement.AssetType,
				requirement.IntensityBand, domain.ErrNotFound)
		}
	}
	return values, nil
}

func vulnerabilityRequirementKey(asset loss.AssetType, intensity string) string {
	return string(asset) + "\x00" + intensity
}

type vulnerabilityRegionCandidates struct {
	exact    []loss.Vulnerability
	national []loss.Vulnerability
}

func (c *vulnerabilityRegionCandidates) add(value loss.Vulnerability, regionCode string) {
	if value.CalibrationRegion == regionCode {
		c.exact = append(c.exact, value)
	} else if value.CalibrationRegion == "CN" {
		c.national = append(c.national, value)
	}
}

func (c vulnerabilityRegionCandidates) selected(label string) (loss.Vulnerability, error) {
	values := c.national
	if len(c.exact) > 0 {
		values = c.exact
	}
	if len(values) != 1 {
		return loss.Vulnerability{}, invalidBaselineCandidates(label, len(values))
	}
	return values[0], nil
}

func invalidBaselineCandidates(label string, count int) error {
	return fmt.Errorf("%w: %s候选数量为 %d", domain.ErrInvalidInput, label, count)
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

func scanExposureBaselines(rows baselineRows) ([]loss.ExposureBaseline, error) {
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

func scanCostBaselines(rows baselineRows) ([]loss.CostBaseline, error) {
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
	value.PriceBaseDate = normalizeBaselineTime(value.PriceBaseDate)
	if err := decodeBaselineSource(&value.Source, source, version, validFrom, validTo); err != nil {
		return loss.CostBaseline{}, fmt.Errorf("解码成本基线 %s 来源: %w", value.ID, err)
	}
	if err := value.Validate(); err != nil {
		return loss.CostBaseline{}, fmt.Errorf("校验成本基线 %s: %w", value.ID, err)
	}
	return value, nil
}

func scanVulnerabilities(rows baselineRows) ([]loss.Vulnerability, error) {
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
	validFrom = normalizeBaselineTime(validFrom)
	if validTo != nil {
		normalized := normalizeBaselineTime(*validTo)
		validTo = &normalized
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

func normalizeBaselineTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

const exposureBaselinesSQL = `WITH latest AS (
    SELECT dataset_version,BOOL_OR(region_code=$1) AS exact_region,MAX(valid_from) AS latest_valid_from
    FROM loss_exposure_baselines
    WHERE region_code IN ($1,'CN') AND exposure_kind=$2 AND valid_from <= $3
      AND (valid_to IS NULL OR valid_to > $3)
    GROUP BY dataset_version
    ORDER BY exact_region DESC,latest_valid_from DESC,dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.region_code,baseline.exposure_kind,baseline.quantity,baseline.unit,baseline.data_year,baseline.coverage_ratio,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_exposure_baselines baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.region_code IN ($1,'CN') AND baseline.exposure_kind=$2
  AND baseline.valid_from <= $3
  AND (baseline.valid_to IS NULL OR baseline.valid_to > $3)
ORDER BY CASE WHEN baseline.region_code=$1 THEN 0 ELSE 1 END,baseline.id`

const costBaselinesSQL = `WITH latest AS (
    SELECT dataset_version,BOOL_OR(region_code=$1) AS exact_region,MAX(valid_from) AS latest_valid_from
    FROM loss_cost_baselines
    WHERE region_code IN ($1,'CN') AND valid_from <= $2
      AND (valid_to IS NULL OR valid_to > $2)
    GROUP BY dataset_version
    HAVING BOOL_AND(status='approved')
    ORDER BY exact_region DESC,latest_valid_from DESC,dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.region_code,baseline.unit,baseline.low_cents,baseline.central_cents,baseline.high_cents,baseline.currency,baseline.price_base_date,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_cost_baselines baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.region_code IN ($1,'CN') AND baseline.status='approved' AND baseline.valid_from <= $2
  AND (baseline.valid_to IS NULL OR baseline.valid_to > $2)
ORDER BY CASE WHEN baseline.region_code=$1 THEN 0 ELSE 1 END,baseline.asset_type,baseline.id`

const vulnerabilitiesSQL = `WITH latest AS (
    SELECT dataset_version,MAX(valid_from) AS latest_valid_from
    FROM loss_vulnerabilities
    WHERE hazard_type=$1 AND valid_from <= $2
      AND (valid_to IS NULL OR valid_to > $2)
    GROUP BY dataset_version
    HAVING BOOL_AND(status='approved')
    ORDER BY latest_valid_from DESC,dataset_version DESC
    LIMIT 1
)
SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.hazard_type,baseline.intensity_band,baseline.impact_fraction_low,baseline.impact_fraction_mid,baseline.impact_fraction_high,baseline.damage_ratio_low,baseline.damage_ratio_mid,baseline.damage_ratio_high,baseline.calibration_region,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_vulnerabilities baseline
JOIN latest ON latest.dataset_version=baseline.dataset_version
WHERE baseline.hazard_type=$1 AND baseline.status='approved' AND baseline.valid_from <= $2
  AND (baseline.valid_to IS NULL OR baseline.valid_to > $2)
ORDER BY baseline.asset_type,baseline.intensity_band,baseline.calibration_region,baseline.id`

const baselineSetVersionSQL = `WITH required_costs(asset_type,unit) AS (
    SELECT * FROM UNNEST($4::TEXT[],$5::TEXT[])
), required_vulnerabilities(asset_type,intensity_band) AS (
    SELECT * FROM UNNEST($6::TEXT[],$7::TEXT[])
), cost_versions AS (
    SELECT cost.dataset_version,BOOL_OR(cost.region_code=$1) AS exact_region,MAX(cost.valid_from) AS latest_valid_from
    FROM loss_cost_baselines cost
    JOIN required_costs required ON required.asset_type=cost.asset_type AND required.unit=cost.unit
    WHERE cost.region_code IN ($1,'CN') AND cost.valid_from <= $3
      AND (cost.valid_to IS NULL OR cost.valid_to > $3)
    GROUP BY cost.dataset_version
    HAVING BOOL_AND(cost.status='approved')
      AND COUNT(DISTINCT (cost.asset_type,cost.unit))=(SELECT COUNT(*) FROM required_costs)
      AND NOT EXISTS (SELECT 1 FROM loss_cost_baselines mixed WHERE mixed.dataset_version=cost.dataset_version
        AND mixed.region_code IN ($1,'CN') AND mixed.valid_from <= $3
        AND (mixed.valid_to IS NULL OR mixed.valid_to > $3) AND mixed.status<>'approved')
), vulnerability_versions AS (
    SELECT vulnerability.dataset_version,BOOL_OR(vulnerability.calibration_region=$1) AS exact_region,
      MAX(vulnerability.valid_from) AS latest_valid_from
    FROM loss_vulnerabilities vulnerability
    JOIN required_vulnerabilities required ON required.asset_type=vulnerability.asset_type
      AND required.intensity_band=vulnerability.intensity_band
    WHERE vulnerability.hazard_type=$2 AND vulnerability.calibration_region IN ($1,'CN')
      AND vulnerability.valid_from <= $3 AND (vulnerability.valid_to IS NULL OR vulnerability.valid_to > $3)
    GROUP BY vulnerability.dataset_version
    HAVING BOOL_AND(vulnerability.status='approved')
      AND COUNT(DISTINCT (vulnerability.asset_type,vulnerability.intensity_band))=
        (SELECT COUNT(*) FROM required_vulnerabilities)
      AND NOT EXISTS (SELECT 1 FROM loss_vulnerabilities mixed
        WHERE mixed.dataset_version=vulnerability.dataset_version AND mixed.hazard_type=$2
          AND mixed.calibration_region IN ($1,'CN') AND mixed.valid_from <= $3
          AND (mixed.valid_to IS NULL OR mixed.valid_to > $3) AND mixed.status<>'approved')
), population_versions AS (
    SELECT population.dataset_version,BOOL_OR(population.region_code=$1) AS exact_region,
      MAX(population.valid_from) AS latest_valid_from
    FROM loss_exposure_baselines population
    WHERE population.region_code IN ($1,'CN') AND population.exposure_kind='population'
      AND population.valid_from <= $3 AND (population.valid_to IS NULL OR population.valid_to > $3)
    GROUP BY population.dataset_version
), road_versions AS (
    SELECT road.dataset_version,BOOL_OR(road.region_code=$1) AS exact_region,MAX(road.valid_from) AS latest_valid_from
    FROM loss_exposure_baselines road
    WHERE road.region_code IN ($1,'CN') AND road.exposure_kind='road'
      AND road.valid_from <= $3 AND (road.valid_to IS NULL OR road.valid_to > $3)
    GROUP BY road.dataset_version
)
SELECT cost.dataset_version
FROM cost_versions cost
JOIN vulnerability_versions vulnerability USING (dataset_version)
JOIN population_versions population USING (dataset_version)
JOIN road_versions road USING (dataset_version)
ORDER BY (cost.exact_region::INT+vulnerability.exact_region::INT+population.exact_region::INT+road.exact_region::INT) DESC,
  GREATEST(cost.latest_valid_from,vulnerability.latest_valid_from,population.latest_valid_from,road.latest_valid_from) DESC,
  cost.dataset_version DESC
LIMIT 1`

const exposureBaselinesByVersionSQL = `SELECT baseline.dataset_version,baseline.id,baseline.region_code,baseline.exposure_kind,baseline.quantity,baseline.unit,baseline.data_year,baseline.coverage_ratio,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_exposure_baselines baseline
WHERE baseline.dataset_version=$1 AND baseline.region_code IN ($2,'CN') AND baseline.exposure_kind=$3
  AND baseline.valid_from <= $4 AND (baseline.valid_to IS NULL OR baseline.valid_to > $4)
ORDER BY CASE WHEN baseline.region_code=$2 THEN 0 ELSE 1 END,baseline.id`

const costBaselinesByVersionSQL = `WITH required(asset_type,unit) AS (
    SELECT * FROM UNNEST($4::TEXT[],$5::TEXT[])
) SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.region_code,baseline.unit,baseline.low_cents,baseline.central_cents,baseline.high_cents,baseline.currency,baseline.price_base_date,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_cost_baselines baseline
JOIN required ON required.asset_type=baseline.asset_type AND required.unit=baseline.unit
WHERE baseline.dataset_version=$1 AND baseline.region_code IN ($2,'CN') AND baseline.status='approved'
  AND baseline.valid_from <= $3 AND (baseline.valid_to IS NULL OR baseline.valid_to > $3)
ORDER BY CASE WHEN baseline.region_code=$2 THEN 0 ELSE 1 END,baseline.asset_type,baseline.id`

const vulnerabilitiesByVersionSQL = `WITH required(asset_type,intensity_band) AS (
    SELECT * FROM UNNEST($5::TEXT[],$6::TEXT[])
) SELECT baseline.dataset_version,baseline.id,baseline.asset_type,baseline.hazard_type,baseline.intensity_band,baseline.impact_fraction_low,baseline.impact_fraction_mid,baseline.impact_fraction_high,baseline.damage_ratio_low,baseline.damage_ratio_mid,baseline.damage_ratio_high,baseline.calibration_region,baseline.status,baseline.approved_by,baseline.valid_from,baseline.valid_to,baseline.source
FROM loss_vulnerabilities baseline
JOIN required ON required.asset_type=baseline.asset_type AND required.intensity_band=baseline.intensity_band
WHERE baseline.dataset_version=$1 AND baseline.hazard_type=$2 AND baseline.calibration_region IN ($3,'CN')
  AND baseline.status='approved' AND baseline.valid_from <= $4 AND (baseline.valid_to IS NULL OR baseline.valid_to > $4)
ORDER BY CASE WHEN baseline.calibration_region=$3 THEN 0 ELSE 1 END,baseline.asset_type,baseline.intensity_band,baseline.id`

const insertExposureBaselineSQL = `INSERT INTO loss_exposure_baselines (dataset_version,id,region_code,exposure_kind,quantity,unit,data_year,coverage_ratio,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

const insertCostBaselineSQL = `INSERT INTO loss_cost_baselines (dataset_version,id,asset_type,region_code,unit,low_cents,central_cents,high_cents,currency,price_base_date,status,approved_by,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`

const insertVulnerabilitySQL = `INSERT INTO loss_vulnerabilities (dataset_version,id,asset_type,hazard_type,intensity_band,impact_fraction_low,impact_fraction_mid,impact_fraction_high,damage_ratio_low,damage_ratio_mid,damage_ratio_high,calibration_region,status,approved_by,valid_from,valid_to,source)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`
