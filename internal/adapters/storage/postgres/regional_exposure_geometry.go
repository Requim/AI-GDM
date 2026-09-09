package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

type regionalGeometryRead struct {
	input  exposurecollection.GeometryInput
	impact exposurecollection.RegionalImpact
}

// ReadExposureGeometryForRegion 先按行政边界筛选全部相交风险区，不使用全国热点窗口。
func (r *HazardRepository) ReadExposureGeometryForRegion(ctx context.Context,
	snapshotID, analysisID string, boundary exposurecollection.AdministrativeBoundary,
) (exposurecollection.GeometryInput, error) {
	value, err := r.readRegionalGeometry(ctx, snapshotID, analysisID, boundary, true)
	return value.input, err
}

// ReadRegionalImpact 独立计算行政区内完整风险交集；不依赖 WorldPop 或 Overpass。
func (r *HazardRepository) ReadRegionalImpact(ctx context.Context,
	snapshotID, analysisID string, boundary exposurecollection.AdministrativeBoundary,
) (exposurecollection.RegionalImpact, error) {
	value, err := r.readRegionalGeometry(ctx, snapshotID, analysisID, boundary, false)
	return value.impact, err
}

func (r *HazardRepository) readRegionalGeometry(ctx context.Context, snapshotID, analysisID string,
	boundary exposurecollection.AdministrativeBoundary, includeZones bool,
) (regionalGeometryRead, error) {
	if err := validateRegionalGeometryRequest(r, snapshotID, analysisID, boundary); err != nil {
		return regionalGeometryRead{}, err
	}
	tx, err := r.pool.BeginTx(ctx, exposureReadOptions)
	if err != nil {
		return regionalGeometryRead{}, fmt.Errorf("开始区域风险读取: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, err := readRegionalBase(ctx, tx, snapshotID, analysisID, boundary)
	if err != nil {
		return value, err
	}
	if err = readRegionalUnion(ctx, tx, analysisID, boundary, &value); err != nil {
		return value, err
	}
	if includeZones {
		if err = completeRegionalInput(ctx, tx, analysisID, boundary, &value); err != nil {
			return value, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return value, fmt.Errorf("提交区域风险读取: %w", err)
	}
	return value, nil
}

func validateRegionalGeometryRequest(r *HazardRepository, snapshotID, analysisID string,
	boundary exposurecollection.AdministrativeBoundary,
) error {
	var geometry spatial.Geometry
	if r == nil || r.pool == nil || !validExposureIdentifier(snapshotID) ||
		!validExposureIdentifier(analysisID) || !validExposureRegionCode(boundary.RegionCode) ||
		boundary.RegionCode == "CN" || !validExposureBoundaryID(boundary.BoundaryID) ||
		(boundary.BoundaryType != "ADM1" && boundary.BoundaryType != "ADM2") ||
		len(boundary.Geometry) > exposurecollection.MaxAdministrativeBoundaryBytes ||
		json.Unmarshal(boundary.Geometry, &geometry) != nil || geometry.ValidateArea() != nil {
		return fmt.Errorf("%w: 区域风险范围参数无效", domain.ErrInvalidInput)
	}
	return nil
}

func readRegionalBase(ctx context.Context, tx pgx.Tx, snapshotID, analysisID string,
	boundary exposurecollection.AdministrativeBoundary,
) (regionalGeometryRead, error) {
	var value regionalGeometryRead
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, lossProjectionSnapshotSQL, snapshotID))
	if err != nil {
		return value, fmt.Errorf("读取区域风险快照: %w", err)
	}
	var analysis applicationloss.LossSpatialProjection
	var status string
	var datasets []byte
	err = tx.QueryRow(ctx, `SELECT id,snapshot_id,algorithm_version,status,calculated_at,dataset_references
		FROM spatial_analyses WHERE id=$1 AND snapshot_id=$2 AND EXISTS (
		SELECT 1 FROM hazard_snapshots WHERE id=$2 AND analysis_complete=TRUE)`, analysisID, snapshotID).
		Scan(&analysis.ID, &analysis.SnapshotID, &analysis.Version, &status, &analysis.CalculatedAt, &datasets)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, domain.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("读取区域空间分析: %w", err)
	}
	if !validSpatialAnalysisStatus(status) {
		return value, fmt.Errorf("%w: 区域空间分析不可用", domain.ErrInsufficientData)
	}
	analysis.DatasetReferences, err = decodeExposureStrings(datasets)
	if err != nil {
		return value, err
	}
	analysis.Status, analysis.Digest = spatialanalysis.AnalysisStatus(status), spatialDigest(analysisID)
	analysis.InputReferences = append([]string{"urn:ai-gdm:spatial-analysis:" + analysisID}, boundary.InputReferences...)
	value.input = exposurecollection.GeometryInput{Snapshot: snapshot, Analysis: analysis}
	value.impact = exposurecollection.RegionalImpact{SnapshotID: snapshotID, RegionCode: boundary.RegionCode,
		BoundaryID: boundary.BoundaryID, BoundaryDigest: boundary.Digest,
		ValidFrom: snapshot.ValidFrom, ValidTo: snapshot.ValidTo}
	return value, nil
}

func readRegionalUnion(ctx context.Context, tx pgx.Tx, analysisID string,
	boundary exposurecollection.AdministrativeBoundary, value *regionalGeometryRead,
) error {
	var maxPoints, totalPoints int64
	args := []any{analysisID, string(boundary.Geometry)}
	err := tx.QueryRow(ctx, regionalZonesCTE+`SELECT COUNT(*)::BIGINT,
		COALESCE(MAX(ST_NPoints(geom)),0),COALESCE(SUM(ST_NPoints(geom)),0) FROM included`, args...).
		Scan(&value.impact.ZoneCount, &maxPoints, &totalPoints)
	if err != nil {
		return fmt.Errorf("预检行政区风险几何: %w", err)
	}
	if value.impact.ZoneCount > 100000 || totalPoints > hardMaxLossTotalGeometryPoints {
		return fmt.Errorf("%w: 行政区风险几何超过安全预算", domain.ErrInsufficientData)
	}
	if value.impact.ZoneCount == 0 {
		value.impact.Geometry = json.RawMessage("null")
		return nil
	}
	var union exposureUnionBudget
	err = tx.QueryRow(ctx, regionalUnionCTE+`SELECT ST_NPoints(geom),ST_MemSize(geom),
		ST_Area(geom::geography),ST_IsValid(geom) FROM merged`, args...).
		Scan(&union.points, &union.memoryBytes, &union.area, &union.valid)
	if err != nil {
		return fmt.Errorf("%w: 区域联合几何不可用: %w", domain.ErrInsufficientData, err)
	}
	if !validExposureUnionBudget(union) {
		return fmt.Errorf("%w: 区域联合几何超过安全预算", domain.ErrInsufficientData)
	}
	var geometry []byte
	bounds := &value.input.Bounds
	err = tx.QueryRow(ctx, regionalUnionCTE+`SELECT ST_AsGeoJSON(geom,9,0)::JSONB,
		ST_XMin(Box2D(geom)),ST_YMin(Box2D(geom)),ST_XMax(Box2D(geom)),ST_YMax(Box2D(geom)) FROM merged`, args...).
		Scan(&geometry, &bounds.West, &bounds.South, &bounds.East, &bounds.North)
	if err != nil {
		return fmt.Errorf("读取区域联合几何: %w", err)
	}
	value.impact.Geometry, value.impact.AreaSquareMeters = geometry, union.area
	value.input.UnionGeometry, value.input.Analysis.TotalAreaSquareMeters = geometry, union.area
	value.input.Stats = exposurecollection.GeometryStats{ZoneCount: value.impact.ZoneCount,
		MaxZonePoints: maxPoints, TotalZonePoints: totalPoints, UnionGeometryBytes: int64(len(geometry))}
	return nil
}

func completeRegionalInput(ctx context.Context, tx pgx.Tx, analysisID string,
	boundary exposurecollection.AdministrativeBoundary, value *regionalGeometryRead,
) error {
	if value.impact.ZoneCount == 0 || value.impact.ZoneCount > exposurecollection.MaxRiskZones {
		return fmt.Errorf("%w: 所选行政区没有相交风险或超出单次暴露采集预算", domain.ErrInsufficientData)
	}
	rows, err := tx.Query(ctx, regionalZonesCTE+`SELECT zone_id,snapshot_id,risk_level,
		ST_Area(geom::geography) FROM included ORDER BY zone_id`, analysisID, string(boundary.Geometry))
	if err != nil {
		return fmt.Errorf("读取区域风险区: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var zone applicationloss.LossRiskZone
		var level string
		if err = rows.Scan(&zone.ID, &zone.SnapshotID, &level, &zone.AreaSquareM); err != nil {
			return fmt.Errorf("扫描区域风险区: %w", err)
		}
		zone.Level, zone.AreaCalculated = hazard.RiskLevel(level), true
		zone.AdminCodes = []string{boundary.RegionCode}
		value.input.Zones = append(value.input.Zones, zone)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("遍历区域风险区: %w", err)
	}
	if len(value.input.Zones) != value.impact.ZoneCount {
		return fmt.Errorf("%w: 行政区风险数量不一致", domain.ErrInsufficientData)
	}
	scope := exposurecollection.ExposureScope{Policy: exposurecollection.RegionalScopePolicy,
		RegionCode: boundary.RegionCode, SeedZoneID: value.input.Zones[0].ID, Window: value.input.Bounds,
		SelectedZoneCount: len(value.input.Zones), TotalZoneCount: len(value.input.Zones),
		SelectedAreaSquareMeters: value.impact.AreaSquareMeters, TotalAreaSquareMeters: value.impact.AreaSquareMeters}
	if err = exposurecollection.BindExposureScopeIdentity(&scope, value.input.Zones); err != nil {
		return err
	}
	value.input.Scope = scope
	return nil
}

const regionalZonesCTE = `WITH boundary AS (
	SELECT ST_SetSRID(ST_GeomFromGeoJSON($2),4326) AS geom
), clipped AS (
	SELECT rz.id AS zone_id,rz.snapshot_id,rz.risk_level,
		ST_CollectionExtract(ST_MakeValid(ST_Intersection(rz.geometry,b.geom)),3) AS geom
	FROM spatial_zone_results szr JOIN risk_zones rz ON rz.id=szr.zone_id AND rz.snapshot_id=szr.snapshot_id
	CROSS JOIN boundary b WHERE szr.analysis_id=$1 AND ST_Intersects(rz.geometry,b.geom)
), included AS (
	SELECT * FROM clipped WHERE NOT ST_IsEmpty(geom) AND ST_Area(geom::geography)>0
) `

const regionalUnionCTE = regionalZonesCTE + `, merged AS (
	SELECT ST_UnaryUnion(ST_Collect(geom)) AS geom FROM included
) `
