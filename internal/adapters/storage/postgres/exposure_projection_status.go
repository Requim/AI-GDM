package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
)

// HasCurrentExposureProjection 判断快照是否已有当前可用的完整真实暴露投影。
func (r *HazardRepository) HasCurrentExposureProjection(ctx context.Context, snapshotID, analysisID string,
	now time.Time,
) (bool, error) {
	if r == nil || r.pool == nil || !validExposureIdentifier(snapshotID) ||
		!validExposureIdentifier(analysisID) || now.IsZero() || !isUTCTime(now) {
		return false, fmt.Errorf("%w: 当前暴露投影查询参数无效", domain.ErrInvalidInput)
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, currentExposureProjectionSQL, snapshotID, analysisID, now).Scan(&exists); err != nil {
		return false, fmt.Errorf("查询当前暴露投影: %w", err)
	}
	if !exists {
		return false, nil
	}
	value, err := r.readLossInput(ctx, snapshotID, analysisID, now,
		applicationloss.DefaultRiskProjectionLimits())
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, domain.ErrInsufficientData) || errors.Is(err, domain.ErrInvalidInput) ||
			errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("复核当前暴露投影: %w", err)
	}
	if err = applicationloss.ValidateRiskProjectionIdentity(value); err != nil {
		return false, nil
	}
	return true, nil
}

func isUTCTime(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

const currentExposureProjectionSQL = `SELECT EXISTS(
    SELECT 1 FROM spatial_exposure_projections ep
	JOIN spatial_analyses sa ON sa.id=ep.analysis_id
	CROSS JOIN LATERAL (SELECT COUNT(*)::BIGINT AS zones,
		COALESCE(SUM(pz.area_square_meters),0) AS total_area,
		COALESCE(MAX(pz.area_square_meters),0) AS max_area,
		COALESCE(BOOL_AND(pz.area_square_meters>0 AND pz.admin_codes='["CN"]'::JSONB),FALSE) AS valid
		FROM spatial_exposure_projection_zones pz WHERE pz.projection_id=ep.id) z
	CROSS JOIN LATERAL (SELECT COUNT(*)::BIGINT AS features,
		COUNT(DISTINCT f.feature_kind) FILTER (WHERE f.status='available' AND f.provided=TRUE) AS kinds,
		COALESCE(BOOL_AND(f.status='available' AND f.provided=TRUE),FALSE) AS valid
		FROM spatial_exposure_features f WHERE f.projection_id=ep.id) f
	WHERE sa.snapshot_id=$1 AND sa.id=$2 AND ep.complete=TRUE AND ep.projection_status='available'
		AND ep.id='exposure-'||ep.projection_digest AND ep.region_code='CN'
		AND ep.admin_boundary_id LIKE 'CHN-ADM0-%'
		AND valid_exposure_reference_array(ep.input_references)
		AND valid_exposure_reference_array(ep.dataset_references)
		AND valid_exposure_sha256_array(ep.source_reference_digests)
		AND ep.collected_at<=$3 AND ep.valid_from<=$3 AND ep.valid_to>$3
		AND ep.zone_count=z.zones AND z.valid AND ep.feature_count=f.features AND f.kinds=3 AND f.valid
		AND ep.union_area_square_meters<=z.total_area+GREATEST(0.000001,z.total_area*0.000000001)
		AND ep.union_area_square_meters>=z.max_area-GREATEST(0.000001,z.total_area*0.000000001)
		AND NOT EXISTS(SELECT 1 FROM spatial_exposure_features feature WHERE feature.projection_id=ep.id
			AND NOT EXISTS(SELECT 1 FROM spatial_exposure_feature_zones fz
				WHERE fz.projection_id=ep.id AND fz.feature_id=feature.feature_id))
		AND NOT EXISTS(SELECT 1 FROM spatial_exposure_projection_zones pz WHERE pz.projection_id=ep.id
			AND NOT EXISTS(SELECT 1 FROM spatial_exposure_feature_zones fz
				WHERE fz.projection_id=ep.id AND fz.zone_id=pz.zone_id))
)`
