-- 单快照保留只允许随父快照删除派生数据；直接修改已完成投影仍被拒绝。
ALTER TABLE spatial_exposure_projections DROP CONSTRAINT spatial_exposure_projections_analysis_id_fkey;
ALTER TABLE spatial_exposure_projections ADD CONSTRAINT spatial_exposure_projections_analysis_id_fkey
    FOREIGN KEY (analysis_id) REFERENCES spatial_analyses(id) ON DELETE CASCADE;

-- 父快照有风险区与空间分析两条级联路径，关联一致性在同一事务末尾检查。
ALTER TABLE spatial_exposure_projection_zones
    ALTER CONSTRAINT spatial_exposure_projection_zones_result_fk DEFERRABLE INITIALLY DEFERRED;

CREATE OR REPLACE FUNCTION reject_completed_exposure_projection_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_projection_complete BOOLEAN;
DECLARE new_projection_complete BOOLEAN;
DECLARE actual_zones BIGINT;
DECLARE actual_features BIGINT;
DECLARE total_zone_area DOUBLE PRECISION;
DECLARE max_zone_area DOUBLE PRECISION;
DECLARE area_tolerance DOUBLE PRECISION;
BEGIN
    IF TG_TABLE_NAME = 'spatial_exposure_projections' THEN
        IF TG_OP = 'INSERT' THEN
            IF NEW.complete THEN
                RAISE EXCEPTION 'exposure projection must be inserted incomplete'
                    USING ERRCODE = 'integrity_constraint_violation';
            END IF;
            RETURN NEW;
        END IF;
        IF TG_OP = 'DELETE'
            AND COALESCE(NULLIF(current_setting('ai_gdm.retiring_analyses',TRUE),''),'[]')::jsonb ? OLD.analysis_id
            AND NOT EXISTS(SELECT 1 FROM spatial_analyses WHERE id=OLD.analysis_id) THEN
            RETURN OLD;
        END IF;
        IF OLD.complete THEN
            RAISE EXCEPTION 'completed exposure projection is immutable'
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        IF NOT NEW.complete OR (TO_JSONB(NEW) - 'complete') IS DISTINCT FROM (TO_JSONB(OLD) - 'complete') THEN
            RAISE EXCEPTION 'exposure projection only allows an atomic completion transition'
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        SELECT COUNT(*),COALESCE(SUM(area_square_meters),0),COALESCE(MAX(area_square_meters),0)
            INTO actual_zones,total_zone_area,max_zone_area FROM spatial_exposure_projection_zones
            WHERE projection_id=NEW.id;
        SELECT COUNT(*) INTO actual_features FROM spatial_exposure_features WHERE projection_id=NEW.id;
        area_tolerance := GREATEST(0.000001,total_zone_area*0.000000001);
        IF actual_zones <> NEW.zone_count OR actual_features <> NEW.feature_count
            OR NEW.id <> 'exposure-' || NEW.projection_digest
            OR NOT valid_exposure_reference_array(NEW.input_references)
            OR NOT valid_exposure_reference_array(NEW.dataset_references)
            OR NOT valid_exposure_sha256_array(NEW.source_reference_digests)
            OR NEW.union_area_square_meters > total_zone_area+area_tolerance
            OR NEW.union_area_square_meters < max_zone_area-area_tolerance
            OR (SELECT COUNT(DISTINCT f.feature_kind) FROM spatial_exposure_features f
                WHERE f.projection_id=NEW.id) <> 3
            OR EXISTS(SELECT 1 FROM spatial_exposure_features f WHERE f.projection_id=NEW.id
                AND (f.status <> 'available' OR NOT f.provided))
            OR EXISTS(SELECT 1 FROM spatial_exposure_projection_zones z WHERE z.projection_id=NEW.id
                AND z.admin_codes <> '["CN"]'::JSONB)
            OR EXISTS(SELECT 1 FROM spatial_exposure_features f WHERE f.projection_id=NEW.id
                AND NOT EXISTS(SELECT 1 FROM spatial_exposure_feature_zones b
                    WHERE b.projection_id=f.projection_id AND b.feature_id=f.feature_id))
            OR EXISTS(SELECT 1 FROM spatial_exposure_projection_zones z WHERE z.projection_id=NEW.id
                AND NOT EXISTS(SELECT 1 FROM spatial_exposure_feature_zones b
                    WHERE b.projection_id=z.projection_id AND b.zone_id=z.zone_id)) THEN
            RAISE EXCEPTION 'exposure projection cannot complete with invalid content'
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.projection_id <> NEW.projection_id THEN
        RAISE EXCEPTION 'exposure projection child cannot move between projections'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF TG_OP <> 'INSERT' THEN
        SELECT complete INTO old_projection_complete FROM spatial_exposure_projections
            WHERE id=OLD.projection_id FOR UPDATE;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        SELECT complete INTO new_projection_complete FROM spatial_exposure_projections
            WHERE id=NEW.projection_id FOR UPDATE;
    END IF;
    IF old_projection_complete OR new_projection_complete THEN
        RAISE EXCEPTION 'completed exposure projection content is immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
