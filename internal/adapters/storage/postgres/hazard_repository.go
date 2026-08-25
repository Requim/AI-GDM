package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
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

func saveSnapshot(ctx context.Context, executor sqlExecutor, value hazard.Snapshot,
	complete bool,
) error {
	thresholds, source, limitations, err := snapshotJSON(value)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, saveSnapshotSQL,
		value.ID, value.HazardType, value.ModelName, value.ModelVersion, value.RunAt,
		value.ValidFrom, value.ValidTo, value.RasterReference, value.ProbabilitySemantics,
		thresholds, value.Status, source, limitations, complete)
	if err != nil {
		return fmt.Errorf("保存灾害快照 %s: %w", value.ID, err)
	}
	return nil
}

// Latest 返回指定灾种最新的可用快照。
func (r *HazardRepository) Latest(ctx context.Context, hazardType hazard.Type) (hazard.Snapshot, error) {
	row := r.pool.QueryRow(ctx, selectSnapshotSQL+latestSnapshotWhere, hazardType)
	return scanSnapshot(row)
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
	return validateZoneSet(snapshot.ID, zones)
}

func validateAnalysisSelector(selector hazard.AnalysisSelector) error {
	if selector.HazardType == "" || selector.ModelName == "" || selector.TransformVersion == "" ||
		selector.Provider == "" || selector.Dataset == "" {
		return fmt.Errorf("%w: 灾害分析查询条件不完整", domain.ErrInvalidInput)
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
	rows, err := queryer.Query(ctx, selectZonesSQL, snapshotID)
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
	return values, rows.Err()
}

func snapshotJSON(value hazard.Snapshot) ([]byte, []byte, []byte, error) {
	thresholds, err := json.Marshal(value.Thresholds)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("编码风险阈值: %w", err)
	}
	source, err := json.Marshal(value.Source)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("编码快照来源: %w", err)
	}
	limitations, err := json.Marshal(value.Limitations)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("编码快照限制: %w", err)
	}
	return thresholds, source, limitations, nil
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

func scanSnapshot(row rowScanner) (hazard.Snapshot, error) {
	var value hazard.Snapshot
	var thresholds, source, limitations []byte
	err := row.Scan(&value.ID, &value.HazardType, &value.ModelName, &value.ModelVersion,
		&value.RunAt, &value.ValidFrom, &value.ValidTo, &value.RasterReference,
		&value.ProbabilitySemantics, &thresholds, &value.Status, &source, &limitations)
	if errors.Is(err, pgx.ErrNoRows) {
		return hazard.Snapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return hazard.Snapshot{}, fmt.Errorf("扫描灾害快照: %w", err)
	}
	if err = decodeSnapshotJSON(&value, thresholds, source, limitations); err != nil {
		return hazard.Snapshot{}, err
	}
	return value, nil
}

func decodeSnapshotJSON(value *hazard.Snapshot, thresholds, source, limitations []byte) error {
	if err := json.Unmarshal(thresholds, &value.Thresholds); err != nil {
		return fmt.Errorf("解码风险阈值: %w", err)
	}
	if err := json.Unmarshal(source, &value.Source); err != nil {
		return fmt.Errorf("解码快照来源: %w", err)
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
    probability_semantics,thresholds,status,source,limitations,analysis_complete
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (id) DO UPDATE SET
    status=EXCLUDED.status, source=EXCLUDED.source, limitations=EXCLUDED.limitations,
    analysis_complete=hazard_snapshots.analysis_complete OR EXCLUDED.analysis_complete`

const selectSnapshotSQL = `SELECT id,hazard_type,model_name,model_version,run_at,valid_from,
    valid_to,raster_reference,probability_semantics,thresholds,status,source,limitations
    FROM hazard_snapshots`

const latestSnapshotWhere = ` WHERE hazard_type=$1 AND analysis_complete=TRUE
    AND status IN ('available','stale') ORDER BY run_at DESC, created_at DESC, id DESC LIMIT 1`

const latestAnalysisWhere = ` WHERE hazard_type=$1 AND model_name=$2
    AND source->>'transformVersion'=$3 AND source->>'provider'=$4 AND source->>'dataset'=$5
    AND analysis_complete=TRUE
    AND status IN ('available','stale') ORDER BY run_at DESC, created_at DESC, id DESC LIMIT 1`

const saveZoneSQL = `INSERT INTO risk_zones (
    id,snapshot_id,geometry,probability_minimum,probability_mean,probability_maximum,
    risk_level,area_square_meters,area_calculated,admin_codes,input_references,limitations
) VALUES ($1,$2,ST_SetSRID(ST_GeomFromGeoJSON($3),4326),$4,$5,$6,$7,$8,$9,$10,$11,$12)`

const selectZonesSQL = `SELECT id,snapshot_id,ST_AsGeoJSON(geometry)::jsonb,
    probability_minimum,probability_mean,probability_maximum,risk_level,area_square_meters,
    area_calculated,admin_codes,input_references,limitations
    FROM risk_zones WHERE snapshot_id=$1 ORDER BY id`
