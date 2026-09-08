-- 行政区暴露投影支持省市代码；核心人口、道路和设施仍必须完整可用。
CREATE FUNCTION valid_exposure_region_code(value TEXT)
RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
    SELECT BTRIM(value) = value
        AND OCTET_LENGTH(value) BETWEEN 1 AND 128
        AND value ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
$$;

CREATE FUNCTION valid_exposure_boundary_id(value TEXT)
RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
    SELECT value ~ '^CHN-ADM(0|1|2)-[A-Za-z0-9._:-]+$'
$$;

CREATE FUNCTION valid_exposure_admin_codes(value JSONB)
RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item JSONB;
DECLARE text_value TEXT;
DECLARE previous TEXT;
BEGIN
    IF JSONB_TYPEOF(value) <> 'array' OR JSONB_ARRAY_LENGTH(value) < 1
        OR JSONB_ARRAY_LENGTH(value) > 16 THEN
        RETURN FALSE;
    END IF;
    FOR item IN SELECT element FROM JSONB_ARRAY_ELEMENTS(value) AS elements(element) LOOP
        IF JSONB_TYPEOF(item) <> 'string' THEN
            RETURN FALSE;
        END IF;
        text_value := item #>> '{}';
        IF NOT valid_exposure_region_code(text_value)
            OR (previous IS NOT NULL AND (text_value COLLATE "C") <= (previous COLLATE "C")) THEN
            RETURN FALSE;
        END IF;
        previous := text_value;
    END LOOP;
    RETURN TRUE;
END $$;

ALTER TABLE spatial_exposure_projections
    DROP CONSTRAINT IF EXISTS spatial_exposure_projections_region_code_check,
    DROP CONSTRAINT IF EXISTS spatial_exposure_projections_admin_boundary_id_check;

ALTER TABLE spatial_exposure_projections
    ADD CONSTRAINT spatial_exposure_projections_region_code_check
        CHECK (valid_exposure_region_code(region_code)),
    ADD CONSTRAINT spatial_exposure_projections_admin_boundary_id_check
        CHECK (valid_exposure_boundary_id(admin_boundary_id));

ALTER TABLE spatial_exposure_projection_zones
    DROP CONSTRAINT IF EXISTS spatial_exposure_projection_zones_admin_codes_check;

ALTER TABLE spatial_exposure_projection_zones
    ADD CONSTRAINT spatial_exposure_projection_zones_admin_codes_check
        CHECK (valid_exposure_admin_codes(admin_codes));

DO $$
DECLARE item RECORD;
BEGIN
    FOR item IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'spatial_exposure_features'::regclass
            AND contype = 'c'
            AND pg_get_constraintdef(oid) ILIKE '%feature_kind%'
            AND pg_get_constraintdef(oid) NOT ILIKE '%trunc%'
    LOOP
        EXECUTE format('ALTER TABLE spatial_exposure_features DROP CONSTRAINT %I', item.conname);
    END LOOP;
END $$;

ALTER TABLE spatial_exposure_features
    ADD CONSTRAINT spatial_exposure_features_feature_kind_check
        CHECK (feature_kind IN ('population', 'road', 'facility', 'building')),
    ADD CONSTRAINT spatial_exposure_features_kind_unit_check
        CHECK ((feature_kind = 'population' AND unit = 'people')
            OR (feature_kind = 'road' AND unit = 'meters')
            OR (feature_kind = 'facility' AND unit = 'count')
            OR (feature_kind = 'building' AND unit = 'square_meters'));

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
            OR (SELECT COUNT(DISTINCT f.feature_kind) FILTER (
                    WHERE f.feature_kind IN ('population','road','facility'))
                FROM spatial_exposure_features f WHERE f.projection_id=NEW.id) <> 3
            OR EXISTS(SELECT 1 FROM spatial_exposure_features f WHERE f.projection_id=NEW.id
                AND (f.status <> 'available' OR NOT f.provided))
            OR EXISTS(SELECT 1 FROM spatial_exposure_projection_zones z WHERE z.projection_id=NEW.id
                AND NOT z.admin_codes ? NEW.region_code)
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
