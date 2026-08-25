package postgis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

type storedExposures struct {
	Population spatialanalysis.PopulationExposureMetric `json:"population"`
	Roads      spatialanalysis.RoadExposureMetric       `json:"roads"`
	POIs       spatialanalysis.POIExposureMetric        `json:"pois"`
}

func persistAnalysis(ctx context.Context, tx pgx.Tx,
	value spatialanalysis.Analysis,
) (spatialanalysis.Analysis, error) {
	if err := verifyZoneCount(ctx, tx, value); err != nil {
		return spatialanalysis.Analysis{}, err
	}
	if err := insertAnalysisHeader(ctx, tx, value); err != nil {
		return spatialanalysis.Analysis{}, err
	}
	for _, zone := range value.Zones {
		if err := upsertZoneResult(ctx, tx, value, zone); err != nil {
			return spatialanalysis.Analysis{}, err
		}
		if err := updateZoneArea(ctx, tx, value.SnapshotID, zone); err != nil {
			return spatialanalysis.Analysis{}, err
		}
	}
	return loadAnalysisByID(ctx, tx, value.ID)
}

func verifyZoneCount(ctx context.Context, tx pgx.Tx, value spatialanalysis.Analysis) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM risk_zones WHERE snapshot_id=$1`,
		value.SnapshotID).Scan(&count); err != nil {
		return fmt.Errorf("核对空间分析风险区数量: %w", err)
	}
	if count != len(value.Zones) {
		return fmt.Errorf("%w: 空间分析风险区集合在事务内发生变化", domain.ErrInvalidInput)
	}
	return nil
}

func insertAnalysisHeader(ctx context.Context, tx pgx.Tx, value spatialanalysis.Analysis) error {
	datasets, areaInputs, inputs, limitations, err := headerJSON(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, insertAnalysisSQL, value.ID, value.SnapshotID, value.Version,
		value.Area.Method, value.Status, len(value.Zones), value.Area.TotalSquareMeters,
		value.CalculatedAt, datasets, areaInputs, inputs, limitations)
	if err != nil {
		return fmt.Errorf("保存空间分析 %s: %w", value.ID, err)
	}
	var snapshotID string
	if err = tx.QueryRow(ctx, `SELECT snapshot_id FROM spatial_analyses WHERE id=$1`,
		value.ID).Scan(&snapshotID); err != nil {
		return fmt.Errorf("核对空间分析标识: %w", err)
	}
	if snapshotID != value.SnapshotID {
		return fmt.Errorf("%w: 空间分析标识与其他快照冲突", domain.ErrInvalidInput)
	}
	return nil
}

func upsertZoneResult(ctx context.Context, tx pgx.Tx, analysis spatialanalysis.Analysis,
	zone spatialanalysis.ZoneResult,
) error {
	admin, exposures, inputs, limitations, err := zoneJSON(zone)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, upsertZoneResultSQL, analysis.ID, analysis.SnapshotID, zone.ZoneID,
		zone.Area.SquareMeters, admin, exposures, inputs, limitations)
	if err != nil {
		return fmt.Errorf("保存风险区 %s 空间结果: %w", zone.ZoneID, err)
	}
	return nil
}

func updateZoneArea(ctx context.Context, tx pgx.Tx, snapshotID string,
	zone spatialanalysis.ZoneResult,
) error {
	result, err := tx.Exec(ctx, `UPDATE risk_zones SET area_square_meters=$1,
        area_calculated=TRUE WHERE id=$2 AND snapshot_id=$3`,
		zone.Area.SquareMeters, zone.ZoneID, snapshotID)
	if err != nil {
		return fmt.Errorf("更新风险区 %s 面积: %w", zone.ZoneID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: 风险区 %s 不属于当前快照", domain.ErrNotFound, zone.ZoneID)
	}
	return nil
}

func headerJSON(value spatialanalysis.Analysis) ([]byte, []byte, []byte, []byte, error) {
	datasets, err := marshalStringArray(value.DatasetReferences)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码空间数据集引用: %w", err)
	}
	areaInputs, err := marshalStringArray(value.Area.InputReferences)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码面积输入引用: %w", err)
	}
	inputs, err := marshalStringArray(value.InputReferences)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码空间分析输入引用: %w", err)
	}
	limitations, err := marshalStringArray(value.Limitations)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码空间分析限制: %w", err)
	}
	return datasets, areaInputs, inputs, limitations, nil
}

func zoneJSON(value spatialanalysis.ZoneResult) ([]byte, []byte, []byte, []byte, error) {
	admin, err := json.Marshal(value.Administration)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码行政匹配: %w", err)
	}
	exposures, err := json.Marshal(storedExposures{
		Population: value.Population, Roads: value.Roads, POIs: value.POIs,
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码风险区暴露量: %w", err)
	}
	inputs, err := marshalStringArray(value.Area.InputReferences)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码风险区输入引用: %w", err)
	}
	limitations, err := marshalStringArray(value.Limitations)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("编码风险区限制: %w", err)
	}
	return admin, exposures, inputs, limitations, nil
}

func marshalStringArray(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	return json.Marshal(values)
}

const insertAnalysisSQL = `INSERT INTO spatial_analyses (
    id,snapshot_id,algorithm_version,area_method,status,zone_count,
    merged_area_square_meters,calculated_at,dataset_references,area_input_references,
    input_references,limitations
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (id) DO NOTHING`

const upsertZoneResultSQL = `INSERT INTO spatial_zone_results (
    analysis_id,snapshot_id,zone_id,area_square_meters,admin_matches,exposures,
    input_references,limitations
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (analysis_id,zone_id) DO UPDATE SET
    area_square_meters=EXCLUDED.area_square_meters,
    admin_matches=EXCLUDED.admin_matches,exposures=EXCLUDED.exposures,
    input_references=EXCLUDED.input_references,limitations=EXCLUDED.limitations`
