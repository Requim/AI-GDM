package postgis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

type calculatedZone struct {
	id        string
	area      float64
	reference string
}

func calculateInput(ctx context.Context, tx pgx.Tx, snapshotID string,
	calculatedAt time.Time,
) (spatialanalysis.AnalysisInput, error) {
	zones, err := loadZoneAreas(ctx, tx, snapshotID)
	if err != nil {
		return spatialanalysis.AnalysisInput{}, err
	}
	total, err := mergedArea(ctx, tx, snapshotID, len(zones))
	if err != nil {
		return spatialanalysis.AnalysisInput{}, err
	}
	zoneResults := make([]spatialanalysis.ZoneResult, len(zones))
	references := []string{"snapshot:" + snapshotID}
	for index, zone := range zones {
		zoneResults[index] = areaOnlyZone(zone)
		references = append(references, zone.reference)
	}
	limitations := []string{"缺少已导入的真实人口、道路、POI 和行政边界数据集，仅提供风险区面积"}
	if len(zones) == 0 {
		limitations = append(limitations, "完整快照没有风险区，空间分析已完成且合并面积为零")
	}
	return spatialanalysis.AnalysisInput{
		SnapshotID: snapshotID,
		Area: spatialanalysis.AreaCalculation{
			Method: spatialanalysis.AreaMethod, TotalSquareMeters: total,
			InputReferences: references,
		},
		Zones: zoneResults, CalculatedAt: calculatedAt,
		InputReferences: references, Limitations: limitations,
	}, nil
}

func loadZoneAreas(ctx context.Context, tx pgx.Tx, snapshotID string) ([]calculatedZone, error) {
	rows, err := tx.Query(ctx, zoneAreaSQL, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("查询风险区面积输入: %w", err)
	}
	defer rows.Close()
	values := make([]calculatedZone, 0)
	for rows.Next() {
		value, err := scanCalculatedZone(rows, snapshotID)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历风险区面积输入: %w", err)
	}
	return values, nil
}

func scanCalculatedZone(row pgx.Row, snapshotID string) (calculatedZone, error) {
	var id, geometryType string
	var valid, empty bool
	var area pgtype.Float8
	var ewkb []byte
	if err := row.Scan(&id, &geometryType, &valid, &empty, &area, &ewkb); err != nil {
		return calculatedZone{}, fmt.Errorf("扫描风险区面积输入: %w", err)
	}
	if (geometryType != "ST_Polygon" && geometryType != "ST_MultiPolygon") || !valid || empty {
		return calculatedZone{}, fmt.Errorf("%w: 风险区 %s 必须是有效非空面几何", domain.ErrInvalidInput, id)
	}
	if !area.Valid || !finitePositive(area.Float64) || len(ewkb) == 0 {
		return calculatedZone{}, fmt.Errorf("%w: 风险区 %s 的地表面积无效", domain.ErrInvalidInput, id)
	}
	return calculatedZone{id: id, area: area.Float64, reference: zoneReference(snapshotID, id, ewkb)}, nil
}

func mergedArea(ctx context.Context, tx pgx.Tx, snapshotID string, zoneCount int) (float64, error) {
	if zoneCount == 0 {
		return 0, nil
	}
	var value float64
	if err := tx.QueryRow(ctx, mergedAreaSQL, snapshotID).Scan(&value); err != nil {
		return 0, fmt.Errorf("计算合并风险区面积: %w", err)
	}
	if !finitePositive(value) {
		return 0, fmt.Errorf("%w: 合并风险区地表面积无效", domain.ErrInvalidInput)
	}
	return value, nil
}

func areaOnlyZone(value calculatedZone) spatialanalysis.ZoneResult {
	return spatialanalysis.ZoneResult{
		ZoneID: value.id,
		Area: spatialanalysis.ZoneArea{
			SquareMeters: value.area, InputReferences: []string{value.reference},
		},
		Population: unavailablePopulation(), Roads: unavailableRoads(), POIs: unavailablePOIs(),
		Administration: unavailableAdministration(),
		Limitations:    []string{"当前仅完成基于风险区几何的真实面积计算"},
	}
}

func unavailablePopulation() spatialanalysis.PopulationExposureMetric {
	return spatialanalysis.PopulationExposureMetric{
		Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.PopulationUnit,
		Limitations: []string{"未导入真实、版本化且带覆盖范围的人口数据集"},
	}
}

func unavailableRoads() spatialanalysis.RoadExposureMetric {
	return spatialanalysis.RoadExposureMetric{
		Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.RoadUnit,
		Limitations: []string{"未导入真实、版本化且带覆盖范围的道路数据集"},
	}
}

func unavailablePOIs() spatialanalysis.POIExposureMetric {
	return spatialanalysis.POIExposureMetric{
		Status: spatialanalysis.MetricUnavailable, Unit: spatialanalysis.POIUnit,
		Limitations: []string{"未导入真实、版本化且带覆盖范围的 POI 数据集"},
	}
}

func unavailableAdministration() spatialanalysis.AdministrativeMatch {
	return spatialanalysis.AdministrativeMatch{
		Status:      spatialanalysis.AdminMatchUnavailable,
		Limitations: []string{"未导入真实、版本化且带覆盖范围的行政边界数据集"},
	}
}

func zoneReference(snapshotID, zoneID string, ewkb []byte) string {
	digest := sha256.Sum256(ewkb)
	return fmt.Sprintf("risk-zone:%s/%s#sha256=%s", snapshotID, zoneID,
		hex.EncodeToString(digest[:]))
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

const zoneAreaSQL = `SELECT id,ST_GeometryType(geometry),ST_IsValid(geometry),ST_IsEmpty(geometry),
    CASE WHEN ST_GeometryType(geometry) IN ('ST_Polygon','ST_MultiPolygon')
        AND ST_IsValid(geometry) AND NOT ST_IsEmpty(geometry)
        THEN ST_Area(geometry::geography) END,
    ST_AsEWKB(geometry)
    FROM risk_zones WHERE snapshot_id=$1 ORDER BY id`

const mergedAreaSQL = `SELECT ST_Area(ST_UnaryUnion(ST_Collect(geometry))::geography)
    FROM risk_zones WHERE snapshot_id=$1`
