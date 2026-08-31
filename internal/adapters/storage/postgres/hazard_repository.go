package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/ports"
)

// HazardRepository 使用 PostGIS 持久化灾害快照和风险区。
type HazardRepository struct {
	pool *pgxpool.Pool
}

// NewHazardRepository 创建灾害仓储适配器。
func NewHazardRepository(pool *pgxpool.Pool) *HazardRepository {
	return &HazardRepository{pool: pool}
}

// SaveSnapshot 保存或更新快照元数据。
func (r *HazardRepository) SaveSnapshot(ctx context.Context, value hazard.Snapshot) error {
	return saveSnapshot(ctx, r.pool, value, false)
}

// SaveAnalysis 在同一事务保存快照和全部风险区。
func (r *HazardRepository) SaveAnalysis(ctx context.Context, snapshot hazard.Snapshot,
	zones []hazard.RiskZone,
) error {
	if err := validateCompleteAnalysis(snapshot, zones); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始保存灾害分析事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = saveSnapshot(ctx, tx, snapshot, true); err != nil {
		return err
	}
	if err = replaceZones(ctx, tx, snapshot.ID, zones); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交灾害分析事务: %w", err)
	}
	return nil
}

// ReconcileAnalysisCoverage 恢复匹配新边界的历史分析，并使其他旧范围退出最新结果选择。
func (r *HazardRepository) ReconcileAnalysisCoverage(ctx context.Context,
	selector hazard.AnalysisSelector, replacement hazard.Coverage, observedAt time.Time,
) error {
	if err := validateAnalysisSelector(selector); err != nil {
		return err
	}
	if err := replacement.Validate(); err != nil {
		return fmt.Errorf("校验替代风险覆盖范围: %w", err)
	}
	if observedAt.IsZero() {
		return fmt.Errorf("%w: 风险覆盖范围失效时间无效", domain.ErrInvalidInput)
	}
	if _, offset := observedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 风险覆盖范围失效时间必须使用 UTC", domain.ErrInvalidInput)
	}
	replacement.CollectedAt = postgresTime(replacement.CollectedAt)
	observedAt = postgresTime(observedAt)
	if observedAt.Before(replacement.CollectedAt) {
		return fmt.Errorf("%w: 风险覆盖范围失效时间早于替代边界采集时间", domain.ErrInvalidInput)
	}
	payload, err := json.Marshal(replacement)
	if err != nil {
		return fmt.Errorf("编码替代风险覆盖范围: %w", err)
	}
	var affected int64
	err = r.pool.QueryRow(ctx, reconcileAnalysisCoverageSQL,
		selector.HazardType, selector.ModelName, selector.TransformVersion, selector.Provider,
		selector.Dataset, payload, observedAt).Scan(&affected)
	if err != nil {
		return fmt.Errorf("协调灾害分析覆盖范围: %w", err)
	}
	return nil
}

func saveSnapshot(ctx context.Context, executor sqlExecutor, value hazard.Snapshot,
	complete bool,
) error {
	if value.Coverage != nil {
		if err := value.Coverage.Validate(); err != nil {
			return fmt.Errorf("校验灾害快照覆盖范围: %w", err)
		}
	}
	value = normalizeSnapshotForStorage(value)
	thresholds, source, coverage, limitations, err := snapshotJSON(value)
	if err != nil {
		return err
	}
	result, err := executor.Exec(ctx, saveSnapshotSQL,
		value.ID, value.HazardType, value.ModelName, value.ModelVersion, value.RunAt,
		value.ValidFrom, value.ValidTo, value.RasterReference, value.ProbabilitySemantics,
		thresholds, value.Status, source, coverage, limitations, complete)
	if err != nil {
		return fmt.Errorf("保存灾害快照 %s: %w", value.ID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: 灾害快照 %s 内容冲突或已绑定不可变风险评估",
			domain.ErrInvalidInput, value.ID)
	}
	return nil
}

func normalizeSnapshotForStorage(value hazard.Snapshot) hazard.Snapshot {
	value.RunAt = postgresTime(value.RunAt)
	value.ValidFrom = postgresTime(value.ValidFrom)
	value.ValidTo = postgresTime(value.ValidTo)
	value.Source.ObservedAt = postgresTime(value.Source.ObservedAt)
	value.Source.PublishedAt = postgresTime(value.Source.PublishedAt)
	value.Source.RevisionFirstSeenAt = postgresTime(value.Source.RevisionFirstSeenAt)
	value.Source.FetchedAt = postgresTime(value.Source.FetchedAt)
	value.Source.ValidFrom = postgresTime(value.Source.ValidFrom)
	value.Source.ValidTo = postgresTime(value.Source.ValidTo)
	if value.Coverage != nil {
		coverage := *value.Coverage
		coverage.CollectedAt = postgresTime(coverage.CollectedAt)
		value.Coverage = &coverage
	}
	return value
}

func postgresTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

// Latest 返回指定灾种最新的可用快照。
func (r *HazardRepository) Latest(ctx context.Context, hazardType hazard.Type) (hazard.Snapshot, error) {
	row := r.pool.QueryRow(ctx, selectSnapshotSQL+latestSnapshotWhere, hazardType)
	return scanSnapshot(row)
}

// LatestRisk 返回指定灾种最新的完整风险分析。
func (r *HazardRepository) LatestRisk(ctx context.Context,
	hazardType hazard.Type,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if err := validateRiskHazardType(hazardType); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	return r.readRisk(ctx, latestSnapshotWhere, hazardType)
}

// LatestMapRisk 先在数据库计数，再有界读取地图用例所需的完整风险区。
func (r *HazardRepository) LatestMapRisk(ctx context.Context, hazardType hazard.Type,
	maxZones int,
) (ports.MapRiskRead, error) {
	if err := validateRiskHazardType(hazardType); err != nil {
		return ports.MapRiskRead{}, err
	}
	if maxZones <= 0 {
		return ports.MapRiskRead{}, fmt.Errorf("%w: 地图风险区上限无效", domain.ErrInvalidInput)
	}
	tx, err := r.pool.BeginTx(ctx, completeRiskReadOptions)
	if err != nil {
		return ports.MapRiskRead{}, fmt.Errorf("开始读取地图风险事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, selectSnapshotSQL+latestSnapshotWhere, hazardType))
	if err != nil {
		return ports.MapRiskRead{}, fmt.Errorf("读取地图风险快照: %w", err)
	}
	zones, total, err := boundedZonesBySnapshot(ctx, tx, snapshot.ID, maxZones)
	if err != nil {
		return ports.MapRiskRead{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.MapRiskRead{}, fmt.Errorf("提交地图风险读取事务: %w", err)
	}
	return ports.MapRiskRead{Snapshot: snapshot, Zones: zones, TotalZoneCount: total}, nil
}

// RiskDetail 返回指定快照的完整风险分析。
func (r *HazardRepository) RiskDetail(ctx context.Context,
	snapshotID string,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if err := validateRiskSnapshotID(snapshotID); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	return r.readRisk(ctx, riskDetailWhere, snapshotID)
}

// RiskDetailBounded 先统计指定快照风险区数量，超过上限时不加载几何明细。
func (r *HazardRepository) RiskDetailBounded(ctx context.Context, snapshotID string,
	maxZones int,
) (hazard.Snapshot, []hazard.RiskZone, int, error) {
	if err := validateRiskSnapshotID(snapshotID); err != nil {
		return hazard.Snapshot{}, nil, 0, err
	}
	if maxZones <= 0 {
		return hazard.Snapshot{}, nil, 0, fmt.Errorf("%w: 风险详情读取上限无效", domain.ErrInvalidInput)
	}
	tx, err := r.pool.BeginTx(ctx, completeRiskReadOptions)
	if err != nil {
		return hazard.Snapshot{}, nil, 0, fmt.Errorf("开始读取有界风险详情事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, selectSnapshotSQL+riskDetailWhere, snapshotID))
	if err != nil {
		return hazard.Snapshot{}, nil, 0, fmt.Errorf("读取有界风险快照: %w", err)
	}
	zones, total, err := boundedZonesBySnapshot(ctx, tx, snapshot.ID, maxZones)
	if err != nil {
		return hazard.Snapshot{}, nil, total, err
	}
	if err = tx.Commit(ctx); err != nil {
		return hazard.Snapshot{}, nil, total, fmt.Errorf("提交有界风险详情事务: %w", err)
	}
	return snapshot, zones, total, nil
}

func (r *HazardRepository) readRisk(ctx context.Context, where string,
	args ...any,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	tx, err := r.pool.BeginTx(ctx, completeRiskReadOptions)
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("开始读取风险分析事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, selectSnapshotSQL+where, args...))
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("读取完整风险快照: %w", err)
	}
	zones, err := zonesBySnapshot(ctx, tx, snapshot.ID)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("提交风险分析读取事务: %w", err)
	}
	return snapshot, zones, nil
}

// LatestAnalysis 返回指定灾种、模型和处理版本的最新完整分析。
func (r *HazardRepository) LatestAnalysis(ctx context.Context,
	selector hazard.AnalysisSelector,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if err := validateAnalysisSelector(selector); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("开始读取完整灾害分析事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx, selectSnapshotSQL+latestAnalysisWhere, selector.HazardType,
		selector.ModelName, selector.TransformVersion, selector.Provider, selector.Dataset)
	snapshot, err := scanSnapshot(row)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	zones, err := zonesBySnapshot(ctx, tx, snapshot.ID)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("提交完整灾害分析读取事务: %w", err)
	}
	return snapshot, zones, nil
}

// GetSnapshot 按标识读取快照。
func (r *HazardRepository) GetSnapshot(ctx context.Context, id string) (hazard.Snapshot, error) {
	row := r.pool.QueryRow(ctx, selectSnapshotSQL+` WHERE id=$1`, id)
	return scanSnapshot(row)
}

// SaveZones 原子替换某个快照的风险区。
func (r *HazardRepository) SaveZones(ctx context.Context, snapshotID string, zones []hazard.RiskZone) error {
	if err := validateZoneSet(snapshotID, zones); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始保存风险区事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = markAnalysisComplete(ctx, tx, snapshotID); err != nil {
		return err
	}
	if err = replaceZones(ctx, tx, snapshotID, zones); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交风险区事务: %w", err)
	}
	return nil
}

func validateCompleteAnalysis(snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	if snapshot.ID == "" || snapshot.HazardType == "" || snapshot.ModelName == "" ||
		snapshot.ModelVersion == "" || snapshot.RasterReference == "" ||
		snapshot.ProbabilitySemantics == "" || snapshot.Source.TransformVersion == "" {
		return fmt.Errorf("%w: 完整灾害分析字段不完整", domain.ErrInvalidInput)
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return fmt.Errorf("%w: 完整灾害分析状态不可读取", domain.ErrInvalidInput)
	}
	if snapshot.RunAt.IsZero() || snapshot.ValidFrom.IsZero() || snapshot.ValidTo.IsZero() ||
		snapshot.ValidTo.Before(snapshot.ValidFrom) {
		return fmt.Errorf("%w: 完整灾害分析时间窗口无效", domain.ErrInvalidInput)
	}
	for _, value := range []time.Time{snapshot.RunAt, snapshot.ValidFrom, snapshot.ValidTo} {
		if _, offset := value.Zone(); offset != 0 {
			return fmt.Errorf("%w: 完整灾害分析时间必须使用 UTC", domain.ErrInvalidInput)
		}
	}
	if err := snapshot.Source.Validate(); err != nil {
		return fmt.Errorf("校验完整灾害分析来源: %w", err)
	}
	if err := hazard.ValidateThresholds(snapshot.Thresholds); err != nil {
		return err
	}
	if snapshot.Coverage != nil {
		if err := snapshot.Coverage.Validate(); err != nil {
			return fmt.Errorf("校验完整灾害分析覆盖范围: %w", err)
		}
	}
	return validateZoneSet(snapshot.ID, zones)
}

func validateAnalysisSelector(selector hazard.AnalysisSelector) error {
	if selector.HazardType == "" || selector.ModelName == "" || selector.TransformVersion == "" ||
		selector.Provider == "" || selector.Dataset == "" {
		return fmt.Errorf("%w: 灾害分析查询条件不完整", domain.ErrInvalidInput)
	}
	return nil
}

func validateRiskHazardType(value hazard.Type) error {
	raw := string(value)
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 64 || !validRiskTypeStart(raw[0]) {
		return fmt.Errorf("%w: 灾种标识无效", domain.ErrInvalidInput)
	}
	for index := 1; index < len(raw); index++ {
		if !validRiskTypePart(raw[index]) {
			return fmt.Errorf("%w: 灾种标识无效", domain.ErrInvalidInput)
		}
	}
	return nil
}

func validRiskTypeStart(value byte) bool {
	return value >= 'a' && value <= 'z'
}

func validRiskTypePart(value byte) bool {
	return validRiskTypeStart(value) || value >= '0' && value <= '9' || value == '_'
}

func validateRiskSnapshotID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return fmt.Errorf("%w: 风险快照标识无效", domain.ErrInvalidInput)
	}
	return nil
}

func validateZoneSet(snapshotID string, zones []hazard.RiskZone) error {
	if snapshotID == "" {
		return fmt.Errorf("%w: 灾害快照标识为空", domain.ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		if zone.ID == "" || zone.SnapshotID != snapshotID {
			return fmt.Errorf("%w: 风险区标识或所属快照无效", domain.ErrInvalidInput)
		}
		if _, exists := seen[zone.ID]; exists {
			return fmt.Errorf("%w: 风险区标识重复", domain.ErrInvalidInput)
		}
		if err := zone.Geometry.Validate(); err != nil {
			return fmt.Errorf("校验风险区 %s 几何: %w", zone.ID, err)
		}
		seen[zone.ID] = struct{}{}
	}
	return nil
}

func replaceZones(ctx context.Context, tx pgx.Tx, snapshotID string, zones []hazard.RiskZone) error {
	var assessmentExists bool
	if err := tx.QueryRow(ctx, riskAssessmentExistsSQL, snapshotID).Scan(&assessmentExists); err != nil {
		return fmt.Errorf("检查快照风险评估绑定: %w", err)
	}
	if assessmentExists {
		stored, err := zonesBySnapshot(ctx, tx, snapshotID)
		if err != nil {
			return err
		}
		equal, err := sameCanonicalZoneInputs(stored, zones)
		if err != nil {
			return fmt.Errorf("规范化已固化风险区: %w", err)
		}
		if equal {
			return nil
		}
		return fmt.Errorf("%w: 快照 %s 已生成权威评估且风险区内容发生变化",
			domain.ErrInvalidInput, snapshotID)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM spatial_analyses WHERE snapshot_id=$1`, snapshotID); err != nil {
		return fmt.Errorf("使旧空间分析失效: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM risk_zones WHERE snapshot_id=$1`, snapshotID); err != nil {
		return fmt.Errorf("清理旧风险区: %w", err)
	}
	for _, zone := range zones {
		if err := saveZone(ctx, tx, snapshotID, zone); err != nil {
			return err
		}
	}
	return nil
}

func markAnalysisComplete(ctx context.Context, tx pgx.Tx, snapshotID string) error {
	result, err := tx.Exec(ctx, `UPDATE hazard_snapshots SET analysis_complete=TRUE WHERE id=$1`, snapshotID)
	if err != nil {
		return fmt.Errorf("标记完整灾害分析: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: 灾害快照 %s 不存在", domain.ErrNotFound, snapshotID)
	}
	return nil
}

// ZonesBySnapshot 返回某快照的全部风险区。
func (r *HazardRepository) ZonesBySnapshot(ctx context.Context, snapshotID string) ([]hazard.RiskZone, error) {
	return zonesBySnapshot(ctx, r.pool, snapshotID)
}

func zonesBySnapshot(ctx context.Context, queryer sqlQueryer, snapshotID string) ([]hazard.RiskZone, error) {
	return queryZones(ctx, queryer, selectZonesSQL, snapshotID)
}

func boundedZonesBySnapshot(ctx context.Context, queryer riskMapQueryer, snapshotID string,
	maxZones int,
) ([]hazard.RiskZone, int, error) {
	var total int
	if err := queryer.QueryRow(ctx, countZonesSQL, snapshotID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计地图风险区: %w", err)
	}
	if total > maxZones {
		return nil, total, fmt.Errorf("%w: 风险区总数 %d 超过地图读取上限 %d",
			domain.ErrInsufficientData, total, maxZones)
	}
	zones, err := queryZones(ctx, queryer, selectMapZonesSQL, snapshotID, maxZones)
	if err != nil {
		return nil, total, err
	}
	if len(zones) != total {
		return nil, total, fmt.Errorf("%w: 地图风险区计数与读取结果不一致", domain.ErrInsufficientData)
	}
	return zones, total, nil
}

func queryZones(ctx context.Context, queryer sqlQueryer, query string,
	args ...any,
) ([]hazard.RiskZone, error) {
	rows, err := queryer.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询风险区: %w", err)
	}
	defer rows.Close()
	values := make([]hazard.RiskZone, 0)
	for rows.Next() {
		value, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历风险区: %w", err)
	}
	return values, nil
}

func snapshotJSON(value hazard.Snapshot) ([]byte, []byte, []byte, []byte, error) {
	thresholds, err := json.Marshal(value.Thresholds)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码风险阈值: %w", err)
	}
	source, err := json.Marshal(value.Source)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码快照来源: %w", err)
	}
	var coverage []byte
	if value.Coverage != nil {
		coverage, err = json.Marshal(value.Coverage)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("编码快照覆盖范围: %w", err)
		}
	}
	limitations, err := json.Marshal(value.Limitations)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码快照限制: %w", err)
	}
	return thresholds, source, coverage, limitations, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type sqlExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type sqlQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type riskMapQueryer interface {
	sqlQueryer
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanSnapshot(row rowScanner) (hazard.Snapshot, error) {
	var value hazard.Snapshot
	var thresholds, source, coverage, limitations []byte
	err := row.Scan(&value.ID, &value.HazardType, &value.ModelName, &value.ModelVersion,
		&value.RunAt, &value.ValidFrom, &value.ValidTo, &value.RasterReference,
		&value.ProbabilitySemantics, &thresholds, &value.Status, &source, &coverage, &limitations)
	if errors.Is(err, pgx.ErrNoRows) {
		return hazard.Snapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return hazard.Snapshot{}, fmt.Errorf("扫描灾害快照: %w", err)
	}
	if err = decodeSnapshotJSON(&value, thresholds, source, coverage, limitations); err != nil {
		return hazard.Snapshot{}, err
	}
	return value, nil
}

func decodeSnapshotJSON(value *hazard.Snapshot, thresholds, source, coverage,
	limitations []byte,
) error {
	if err := json.Unmarshal(thresholds, &value.Thresholds); err != nil {
		return fmt.Errorf("解码风险阈值: %w", err)
	}
	if err := json.Unmarshal(source, &value.Source); err != nil {
		return fmt.Errorf("解码快照来源: %w", err)
	}
	if len(coverage) > 0 && string(coverage) != "null" {
		if err := json.Unmarshal(coverage, &value.Coverage); err != nil {
			return fmt.Errorf("解码快照覆盖范围: %w", err)
		}
	}
	if err := json.Unmarshal(limitations, &value.Limitations); err != nil {
		return fmt.Errorf("解码快照限制: %w", err)
	}
	return nil
}

func saveZone(ctx context.Context, tx pgx.Tx, snapshotID string, zone hazard.RiskZone) error {
	geometry, err := json.Marshal(zone.Geometry)
	if err != nil {
		return fmt.Errorf("编码风险区几何: %w", err)
	}
	adminCodes, _ := json.Marshal(zone.AdminCodes)
	inputReferences, _ := json.Marshal(zone.InputReferences)
	limitations, _ := json.Marshal(zone.Limitations)
	_, err = tx.Exec(ctx, saveZoneSQL, zone.ID, snapshotID, string(geometry), zone.Minimum,
		zone.Mean, zone.Maximum, zone.Level, zone.AreaSquareM, zone.AreaCalculated,
		adminCodes, inputReferences, limitations)
	if err != nil {
		return fmt.Errorf("保存风险区 %s: %w", zone.ID, err)
	}
	return nil
}

func scanZone(row rowScanner) (hazard.RiskZone, error) {
	var value hazard.RiskZone
	var geometry, adminCodes, inputs, limitations []byte
	err := row.Scan(&value.ID, &value.SnapshotID, &geometry, &value.Minimum, &value.Mean,
		&value.Maximum, &value.Level, &value.AreaSquareM, &value.AreaCalculated,
		&adminCodes, &inputs, &limitations)
	if err != nil {
		return hazard.RiskZone{}, fmt.Errorf("扫描风险区: %w", err)
	}
	if err = json.Unmarshal(geometry, &value.Geometry); err != nil {
		return hazard.RiskZone{}, fmt.Errorf("解码风险区几何: %w", err)
	}
	if err = json.Unmarshal(adminCodes, &value.AdminCodes); err != nil {
		return hazard.RiskZone{}, fmt.Errorf("解码行政区: %w", err)
	}
	if err = json.Unmarshal(inputs, &value.InputReferences); err != nil {
		return hazard.RiskZone{}, fmt.Errorf("解码输入引用: %w", err)
	}
	if err = json.Unmarshal(limitations, &value.Limitations); err != nil {
		return hazard.RiskZone{}, fmt.Errorf("解码风险区限制: %w", err)
	}
	return value, nil
}

const saveSnapshotSQL = `INSERT INTO hazard_snapshots (
    id,hazard_type,model_name,model_version,run_at,valid_from,valid_to,raster_reference,
    probability_semantics,thresholds,status,source,coverage,limitations,analysis_complete
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (id) DO UPDATE SET
    status=EXCLUDED.status, source=EXCLUDED.source, limitations=EXCLUDED.limitations,
    analysis_complete=hazard_snapshots.analysis_complete OR EXCLUDED.analysis_complete
WHERE hazard_snapshots.hazard_type=EXCLUDED.hazard_type
    AND hazard_snapshots.model_name=EXCLUDED.model_name
    AND hazard_snapshots.model_version=EXCLUDED.model_version
    AND hazard_snapshots.run_at=EXCLUDED.run_at
    AND hazard_snapshots.valid_from=EXCLUDED.valid_from
    AND hazard_snapshots.valid_to=EXCLUDED.valid_to
    AND hazard_snapshots.raster_reference=EXCLUDED.raster_reference
    AND hazard_snapshots.probability_semantics=EXCLUDED.probability_semantics
    AND hazard_snapshots.thresholds=EXCLUDED.thresholds
	AND hazard_snapshots.coverage IS NOT DISTINCT FROM EXCLUDED.coverage
    AND (NOT EXISTS (SELECT 1 FROM risk_assessments WHERE snapshot_id=EXCLUDED.id)
        OR (hazard_snapshots.status=EXCLUDED.status
            AND hazard_snapshots.source=EXCLUDED.source
            AND hazard_snapshots.limitations=EXCLUDED.limitations))`

const riskAssessmentExistsSQL = `SELECT EXISTS(
    SELECT 1 FROM risk_assessments WHERE snapshot_id=$1
)`

const selectSnapshotSQL = `SELECT id,hazard_type,model_name,model_version,run_at,valid_from,
    valid_to,raster_reference,probability_semantics,thresholds,status,source,coverage,limitations
    FROM hazard_snapshots`

const latestSnapshotWhere = ` WHERE hazard_type=$1 AND analysis_complete=TRUE
    AND status IN ('available','stale') AND coverage IS NOT NULL AND superseded_at IS NULL
    ORDER BY run_at DESC, created_at DESC, id DESC LIMIT 1`

const riskDetailWhere = ` WHERE id=$1 AND analysis_complete=TRUE
    AND status IN ('available','stale')`

const latestAnalysisWhere = ` WHERE hazard_type=$1 AND model_name=$2
    AND source->>'transformVersion'=$3 AND source->>'provider'=$4 AND source->>'dataset'=$5
    AND analysis_complete=TRUE AND superseded_at IS NULL
    AND status IN ('available','stale') ORDER BY run_at DESC, created_at DESC, id DESC LIMIT 1`

const reconcileAnalysisCoverageSQL = `WITH replacement AS (
    SELECT $6::jsonb AS coverage
), anchor AS (
    SELECT run_at,created_at,id FROM hazard_snapshots,replacement
    WHERE hazard_type=$1 AND model_name=$2
        AND source->>'transformVersion'=$3 AND source->>'provider'=$4 AND source->>'dataset'=$5
        AND analysis_complete=TRUE AND status IN ('available','stale')
    ORDER BY run_at DESC,created_at DESC,id DESC LIMIT 1
), updated AS (
    UPDATE hazard_snapshots AS candidate SET
        superseded_at=CASE WHEN ROW(
            candidate.coverage->>'boundaryId',candidate.coverage->>'boundaryVersion',
            candidate.coverage->>'sha256',candidate.coverage->>'geometrySha256'
        ) IS NOT DISTINCT FROM ROW(
            replacement.coverage->>'boundaryId',replacement.coverage->>'boundaryVersion',
            replacement.coverage->>'sha256',replacement.coverage->>'geometrySha256'
        ) THEN NULL ELSE COALESCE(candidate.superseded_at,$7) END,
        superseded_by_coverage=CASE WHEN ROW(
            candidate.coverage->>'boundaryId',candidate.coverage->>'boundaryVersion',
            candidate.coverage->>'sha256',candidate.coverage->>'geometrySha256'
        ) IS NOT DISTINCT FROM ROW(
            replacement.coverage->>'boundaryId',replacement.coverage->>'boundaryVersion',
            replacement.coverage->>'sha256',replacement.coverage->>'geometrySha256'
        ) THEN NULL ELSE COALESCE(candidate.superseded_by_coverage,replacement.coverage) END
    FROM anchor,replacement WHERE candidate.hazard_type=$1 AND candidate.model_name=$2
        AND candidate.source->>'transformVersion'=$3 AND candidate.source->>'provider'=$4
        AND candidate.source->>'dataset'=$5 AND candidate.analysis_complete=TRUE
        AND candidate.status IN ('available','stale')
        AND ROW(candidate.run_at,candidate.created_at,candidate.id) <= ROW(anchor.run_at,anchor.created_at,anchor.id)
        AND ((ROW(
            candidate.coverage->>'boundaryId',candidate.coverage->>'boundaryVersion',
            candidate.coverage->>'sha256',candidate.coverage->>'geometrySha256'
        ) IS NOT DISTINCT FROM ROW(
            replacement.coverage->>'boundaryId',replacement.coverage->>'boundaryVersion',
            replacement.coverage->>'sha256',replacement.coverage->>'geometrySha256'
        ) AND candidate.superseded_at IS NOT NULL) OR (ROW(
            candidate.coverage->>'boundaryId',candidate.coverage->>'boundaryVersion',
            candidate.coverage->>'sha256',candidate.coverage->>'geometrySha256'
        ) IS DISTINCT FROM ROW(
            replacement.coverage->>'boundaryId',replacement.coverage->>'boundaryVersion',
            replacement.coverage->>'sha256',replacement.coverage->>'geometrySha256'
        ) AND (candidate.superseded_at IS NULL OR ROW(
            candidate.superseded_by_coverage->>'boundaryId',
            candidate.superseded_by_coverage->>'boundaryVersion',
            candidate.superseded_by_coverage->>'sha256',
            candidate.superseded_by_coverage->>'geometrySha256'
        ) IS NOT DISTINCT FROM ROW(
            replacement.coverage->>'boundaryId',replacement.coverage->>'boundaryVersion',
            replacement.coverage->>'sha256',replacement.coverage->>'geometrySha256'))))
    RETURNING candidate.id
) SELECT COUNT(*) FROM updated`

const saveZoneSQL = `INSERT INTO risk_zones (
    id,snapshot_id,geometry,probability_minimum,probability_mean,probability_maximum,
    risk_level,area_square_meters,area_calculated,admin_codes,input_references,limitations
) VALUES ($1,$2,ST_SetSRID(ST_GeomFromGeoJSON($3),4326),$4,$5,$6,$7,$8,$9,$10,$11,$12)`

const selectZonesSQL = `SELECT id,snapshot_id,ST_AsGeoJSON(geometry)::jsonb,
    probability_minimum,probability_mean,probability_maximum,risk_level,area_square_meters,
    area_calculated,admin_codes,input_references,limitations
    FROM risk_zones WHERE snapshot_id=$1 ORDER BY id`

const countZonesSQL = `SELECT COUNT(*) FROM risk_zones WHERE snapshot_id=$1`

const selectMapZonesSQL = `SELECT id,snapshot_id,ST_AsGeoJSON(geometry)::jsonb,
    probability_minimum,probability_mean,probability_maximum,risk_level,area_square_meters,
    area_calculated,admin_codes,input_references,limitations
    FROM risk_zones WHERE snapshot_id=$1
    ORDER BY CASE risk_level WHEN 'very_high' THEN 4 WHEN 'high' THEN 3
        WHEN 'moderate' THEN 2 WHEN 'low' THEN 1 ELSE 0 END DESC,
        probability_maximum DESC,probability_mean DESC,id
    LIMIT $2`

var completeRiskReadOptions = pgx.TxOptions{
	IsoLevel:   pgx.RepeatableRead,
	AccessMode: pgx.ReadOnly,
}
