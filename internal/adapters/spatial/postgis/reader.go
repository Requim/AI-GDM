package postgis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

type analysisQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Get 按标识读取已完成的空间分析。
func (e *Executor) Get(ctx context.Context, id string) (spatialanalysis.Analysis, error) {
	if e == nil || e.pool == nil || id == "" || id != strings.TrimSpace(id) {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 空间分析查询参数无效", domain.ErrInvalidInput)
	}
	return e.readConsistently(ctx, func(tx pgx.Tx) (spatialanalysis.Analysis, error) {
		return loadAnalysisByID(ctx, tx, id)
	})
}

// LatestBySnapshot 返回某灾害快照最新完成的空间分析。
func (e *Executor) LatestBySnapshot(ctx context.Context,
	snapshotID string,
) (spatialanalysis.Analysis, error) {
	if e == nil || e.pool == nil || snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 空间分析查询参数无效", domain.ErrInvalidInput)
	}
	return e.readConsistently(ctx, func(tx pgx.Tx) (spatialanalysis.Analysis, error) {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM spatial_analyses WHERE snapshot_id=$1
            ORDER BY calculated_at DESC,id DESC LIMIT 1`, snapshotID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return spatialanalysis.Analysis{}, domain.ErrNotFound
		}
		if err != nil {
			return spatialanalysis.Analysis{}, fmt.Errorf("查询快照最新空间分析: %w", err)
		}
		return loadAnalysisByID(ctx, tx, id)
	})
}

func (e *Executor) readConsistently(ctx context.Context,
	read func(pgx.Tx) (spatialanalysis.Analysis, error),
) (spatialanalysis.Analysis, error) {
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("开始读取空间分析事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, err := read(tx)
	if err != nil {
		return spatialanalysis.Analysis{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("提交空间分析读取事务: %w", err)
	}
	return value, nil
}

func loadAnalysisByID(ctx context.Context, queryer analysisQueryer,
	id string,
) (spatialanalysis.Analysis, error) {
	value, zoneCount, err := loadAnalysisHeader(ctx, queryer, id)
	if err != nil {
		return spatialanalysis.Analysis{}, err
	}
	zones, err := loadZoneResults(ctx, queryer, id)
	if err != nil {
		return spatialanalysis.Analysis{}, err
	}
	if len(zones) != zoneCount {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 空间分析区级结果数量不完整", domain.ErrInvalidInput)
	}
	input := spatialanalysis.AnalysisInput{
		SnapshotID: value.SnapshotID, Area: value.Area, Zones: zones,
		CalculatedAt: value.CalculatedAt, InputReferences: value.InputReferences,
		DatasetReferences: value.DatasetReferences, Limitations: value.Limitations,
	}
	normalized, err := spatialanalysis.NewAnalysis(input)
	if err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("校验已存空间分析: %w", err)
	}
	if normalized.ID != value.ID || normalized.Status != value.Status {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 已存空间分析标识或状态不一致", domain.ErrInvalidInput)
	}
	return normalized, nil
}

func loadAnalysisHeader(ctx context.Context, queryer analysisQueryer,
	id string,
) (spatialanalysis.Analysis, int, error) {
	var value spatialanalysis.Analysis
	var zoneCount int
	var datasets, areaInputs, inputs, limitations []byte
	err := queryer.QueryRow(ctx, selectAnalysisSQL, id).Scan(
		&value.ID, &value.SnapshotID, &value.Version, &value.Area.Method, &value.Status,
		&zoneCount, &value.Area.TotalSquareMeters, &value.CalculatedAt,
		&datasets, &areaInputs, &inputs, &limitations)
	if errors.Is(err, pgx.ErrNoRows) {
		return spatialanalysis.Analysis{}, 0, domain.ErrNotFound
	}
	if err != nil {
		return spatialanalysis.Analysis{}, 0, fmt.Errorf("扫描空间分析: %w", err)
	}
	if err = decodeHeader(&value, datasets, areaInputs, inputs, limitations); err != nil {
		return spatialanalysis.Analysis{}, 0, err
	}
	return value, zoneCount, nil
}

func loadZoneResults(ctx context.Context, queryer analysisQueryer,
	analysisID string,
) ([]spatialanalysis.ZoneResult, error) {
	rows, err := queryer.Query(ctx, selectZoneResultsSQL, analysisID)
	if err != nil {
		return nil, fmt.Errorf("查询空间分析区级结果: %w", err)
	}
	defer rows.Close()
	values := make([]spatialanalysis.ZoneResult, 0)
	for rows.Next() {
		value, err := scanZoneResult(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历空间分析区级结果: %w", err)
	}
	return values, nil
}

func scanZoneResult(row pgx.Row) (spatialanalysis.ZoneResult, error) {
	var value spatialanalysis.ZoneResult
	var admin, exposures, inputs, limitations []byte
	err := row.Scan(&value.ZoneID, &value.Area.SquareMeters, &admin, &exposures,
		&inputs, &limitations)
	if err != nil {
		return spatialanalysis.ZoneResult{}, fmt.Errorf("扫描空间分析区级结果: %w", err)
	}
	if err = decodeZone(&value, admin, exposures, inputs, limitations); err != nil {
		return spatialanalysis.ZoneResult{}, err
	}
	return value, nil
}

func decodeHeader(value *spatialanalysis.Analysis, datasets, areaInputs, inputs,
	limitations []byte,
) error {
	if err := json.Unmarshal(datasets, &value.DatasetReferences); err != nil {
		return fmt.Errorf("解码空间数据集引用: %w", err)
	}
	if err := json.Unmarshal(areaInputs, &value.Area.InputReferences); err != nil {
		return fmt.Errorf("解码面积输入引用: %w", err)
	}
	if err := json.Unmarshal(inputs, &value.InputReferences); err != nil {
		return fmt.Errorf("解码空间分析输入引用: %w", err)
	}
	if err := json.Unmarshal(limitations, &value.Limitations); err != nil {
		return fmt.Errorf("解码空间分析限制: %w", err)
	}
	return nil
}

func decodeZone(value *spatialanalysis.ZoneResult, admin, exposures, inputs,
	limitations []byte,
) error {
	var metrics storedExposures
	if err := json.Unmarshal(admin, &value.Administration); err != nil {
		return fmt.Errorf("解码行政匹配: %w", err)
	}
	if err := json.Unmarshal(exposures, &metrics); err != nil {
		return fmt.Errorf("解码风险区暴露量: %w", err)
	}
	value.Population, value.Roads, value.POIs = metrics.Population, metrics.Roads, metrics.POIs
	if err := json.Unmarshal(inputs, &value.Area.InputReferences); err != nil {
		return fmt.Errorf("解码风险区输入引用: %w", err)
	}
	if err := json.Unmarshal(limitations, &value.Limitations); err != nil {
		return fmt.Errorf("解码风险区限制: %w", err)
	}
	return nil
}

const selectAnalysisSQL = `SELECT id,snapshot_id,algorithm_version,area_method,status,
    zone_count,merged_area_square_meters,calculated_at,dataset_references,
    area_input_references,input_references,limitations FROM spatial_analyses WHERE id=$1`

const selectZoneResultsSQL = `SELECT zone_id,area_square_meters,admin_matches,exposures,
    input_references,limitations FROM spatial_zone_results WHERE analysis_id=$1 ORDER BY zone_id`
