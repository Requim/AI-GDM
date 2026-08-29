CREATE FUNCTION valid_exposure_projection_limitations(value JSONB)
RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item JSONB;
DECLARE text_value TEXT;
DECLARE previous TEXT;
DECLARE total_bytes INTEGER := 0;
BEGIN
    IF JSONB_TYPEOF(value) <> 'array' THEN
        RETURN FALSE;
    END IF;
    IF JSONB_ARRAY_LENGTH(value) > 100 THEN
        RETURN FALSE;
    END IF;
    FOR item IN SELECT element FROM JSONB_ARRAY_ELEMENTS(value) AS elements(element) LOOP
        IF JSONB_TYPEOF(item) <> 'string' THEN RETURN FALSE; END IF;
        text_value := item #>> '{}';
        total_bytes := total_bytes + OCTET_LENGTH(text_value);
        IF text_value = '' OR text_value <> BTRIM(text_value)
            OR OCTET_LENGTH(text_value) > 4096 OR total_bytes > 65536
            OR (previous IS NOT NULL AND (text_value COLLATE "C") <= (previous COLLATE "C")) THEN
            RETURN FALSE;
        END IF;
        previous := text_value;
    END LOOP;
    RETURN TRUE;
END $$;

CREATE FUNCTION valid_exposure_sha256_array(value JSONB)
RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item JSONB;
DECLARE text_value TEXT;
DECLARE previous TEXT;
DECLARE item_count INTEGER := 0;
BEGIN
    IF JSONB_TYPEOF(value) <> 'array' THEN
        RETURN FALSE;
    END IF;
    IF JSONB_ARRAY_LENGTH(value) = 0 OR JSONB_ARRAY_LENGTH(value) > 1000 THEN
        RETURN FALSE;
    END IF;
    FOR item IN SELECT element FROM JSONB_ARRAY_ELEMENTS(value) AS elements(element) LOOP
        IF JSONB_TYPEOF(item) <> 'string' THEN RETURN FALSE; END IF;
        text_value := item #>> '{}';
        IF text_value !~ '^[0-9a-f]{64}$'
            OR (previous IS NOT NULL AND (text_value COLLATE "C") <= (previous COLLATE "C")) THEN
            RETURN FALSE;
        END IF;
        previous := text_value;
        item_count := item_count + 1;
    END LOOP;
    RETURN item_count > 0;
END $$;

CREATE FUNCTION valid_exposure_reference_array(value JSONB)
RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item JSONB;
DECLARE text_value TEXT;
DECLARE previous TEXT;
DECLARE total_bytes INTEGER := 0;
BEGIN
    IF JSONB_TYPEOF(value) <> 'array' OR JSONB_ARRAY_LENGTH(value) = 0
        OR JSONB_ARRAY_LENGTH(value) > 1000 THEN
        RETURN FALSE;
    END IF;
    FOR item IN SELECT element FROM JSONB_ARRAY_ELEMENTS(value) AS elements(element) LOOP
        IF JSONB_TYPEOF(item) <> 'string' THEN RETURN FALSE; END IF;
        text_value := item #>> '{}';
        total_bytes := total_bytes + OCTET_LENGTH(text_value);
        IF text_value = '' OR text_value <> BTRIM(text_value)
            OR OCTET_LENGTH(text_value) > 4096 OR total_bytes > 1048576
            OR (previous IS NOT NULL AND (text_value COLLATE "C") <= (previous COLLATE "C")) THEN
            RETURN FALSE;
        END IF;
        previous := text_value;
    END LOOP;
    RETURN TRUE;
END $$;

CREATE TABLE spatial_exposure_projections (
    id TEXT PRIMARY KEY CHECK (id ~ '^exposure-[0-9a-f]{64}$'),
    analysis_id TEXT NOT NULL REFERENCES spatial_analyses(id),
    projection_version TEXT NOT NULL CHECK (BTRIM(projection_version) <> ''),
    projection_digest TEXT NOT NULL CHECK (projection_digest ~ '^[0-9a-f]{64}$'),
    projection_status TEXT NOT NULL CHECK (projection_status = 'available'),
    collected_at TIMESTAMPTZ NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ NOT NULL,
    region_code TEXT NOT NULL CHECK (region_code = 'CN'),
    union_area_square_meters DOUBLE PRECISION NOT NULL
        CHECK (union_area_square_meters > 0
            AND union_area_square_meters < 'Infinity'::DOUBLE PRECISION),
    admin_boundary_id TEXT NOT NULL CHECK (admin_boundary_id LIKE 'CHN-ADM0-%'),
    admin_boundary_digest TEXT NOT NULL CHECK (admin_boundary_digest ~ '^[0-9a-f]{64}$'),
    admin_boundary_reference TEXT NOT NULL CHECK (BTRIM(admin_boundary_reference) <> ''),
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    zone_count INTEGER NOT NULL CHECK (zone_count > 0),
    feature_count INTEGER NOT NULL CHECK (feature_count > 0),
    input_references JSONB NOT NULL CHECK (valid_exposure_reference_array(input_references)),
    dataset_references JSONB NOT NULL CHECK (valid_exposure_reference_array(dataset_references)),
    source_reference_digests JSONB NOT NULL
        CHECK (valid_exposure_sha256_array(source_reference_digests)),
    limitations JSONB NOT NULL CHECK (valid_exposure_projection_limitations(limitations)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (id, analysis_id),
    UNIQUE (analysis_id, projection_digest),
    CHECK (id = 'exposure-' || projection_digest),
    CHECK (valid_to > valid_from),
    CHECK (collected_at >= valid_from AND collected_at < valid_to)
);

CREATE INDEX spatial_exposure_projections_latest_idx
    ON spatial_exposure_projections (analysis_id, collected_at DESC, id DESC)
    WHERE complete = TRUE;

CREATE TABLE spatial_exposure_projection_zones (
    projection_id TEXT NOT NULL,
    analysis_id TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    area_square_meters DOUBLE PRECISION NOT NULL
        CHECK (area_square_meters > 0 AND area_square_meters < 'Infinity'::DOUBLE PRECISION),
    admin_codes JSONB NOT NULL CHECK (admin_codes = '["CN"]'::JSONB),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (projection_id, zone_id),
    CONSTRAINT spatial_exposure_projection_zones_projection_fk
        FOREIGN KEY (projection_id, analysis_id)
        REFERENCES spatial_exposure_projections(id, analysis_id) ON DELETE CASCADE,
    CONSTRAINT spatial_exposure_projection_zones_result_fk
        FOREIGN KEY (analysis_id, zone_id)
        REFERENCES spatial_zone_results(analysis_id, zone_id),
    CONSTRAINT spatial_exposure_projection_zones_unique
        UNIQUE (projection_id, analysis_id, zone_id)
);

CREATE INDEX spatial_exposure_projection_zones_zone_idx
    ON spatial_exposure_projection_zones (projection_id, zone_id);

CREATE TABLE spatial_exposure_features (
    projection_id TEXT NOT NULL,
    analysis_id TEXT NOT NULL,
    feature_id TEXT NOT NULL CHECK (BTRIM(feature_id) <> ''),
    feature_kind TEXT NOT NULL CHECK (feature_kind IN ('population', 'road', 'facility')),
    quantity DOUBLE PRECISION NOT NULL
        CHECK (quantity >= 0 AND quantity < 'Infinity'::DOUBLE PRECISION),
    unit TEXT NOT NULL CHECK (BTRIM(unit) <> ''),
    coverage_ratio DOUBLE PRECISION NOT NULL
        CHECK (coverage_ratio > 0 AND coverage_ratio <= 1),
    status TEXT NOT NULL CHECK (status IN ('available', 'partial', 'unavailable')),
    provided BOOLEAN NOT NULL,
    input_references JSONB NOT NULL CHECK (JSONB_TYPEOF(input_references) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (projection_id, feature_id),
    CONSTRAINT spatial_exposure_features_projection_fk
        FOREIGN KEY (projection_id, analysis_id)
        REFERENCES spatial_exposure_projections(id, analysis_id) ON DELETE CASCADE,
    CONSTRAINT spatial_exposure_features_projection_unique
        UNIQUE (projection_id, analysis_id, feature_id),
    CHECK ((feature_kind = 'population' AND unit = 'people')
        OR (feature_kind = 'road' AND unit = 'meters')
        OR (feature_kind = 'facility' AND unit = 'count')),
    CHECK ((status = 'available' AND provided = TRUE)
        OR (status <> 'available' AND provided = FALSE)),
    CHECK (feature_kind <> 'facility' OR quantity = TRUNC(quantity))
);

CREATE INDEX spatial_exposure_features_kind_idx
    ON spatial_exposure_features (projection_id, feature_kind, feature_id);

CREATE TABLE spatial_exposure_feature_zones (
    projection_id TEXT NOT NULL,
    analysis_id TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    PRIMARY KEY (projection_id, feature_id, zone_id),
    CONSTRAINT spatial_exposure_feature_zones_feature_fk
        FOREIGN KEY (projection_id, analysis_id, feature_id)
        REFERENCES spatial_exposure_features(projection_id, analysis_id, feature_id) ON DELETE CASCADE,
    CONSTRAINT spatial_exposure_feature_zones_result_fk
        FOREIGN KEY (projection_id, analysis_id, zone_id)
        REFERENCES spatial_exposure_projection_zones(projection_id, analysis_id, zone_id) ON DELETE CASCADE
);

CREATE INDEX spatial_exposure_feature_zones_zone_idx
    ON spatial_exposure_feature_zones (projection_id, zone_id, feature_id);

CREATE FUNCTION reject_completed_exposure_projection_mutation()
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
        SELECT COUNT(*) INTO actual_features FROM spatial_exposure_features
            WHERE projection_id=NEW.id;
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
            WHERE id = OLD.projection_id FOR UPDATE;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        SELECT complete INTO new_projection_complete FROM spatial_exposure_projections
            WHERE id = NEW.projection_id FOR UPDATE;
    END IF;
    IF old_projection_complete OR new_projection_complete THEN
        RAISE EXCEPTION 'completed exposure projection content is immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER spatial_exposure_projections_immutable
BEFORE INSERT OR UPDATE OR DELETE ON spatial_exposure_projections
FOR EACH ROW EXECUTE FUNCTION reject_completed_exposure_projection_mutation();

CREATE TRIGGER spatial_exposure_features_immutable
BEFORE INSERT OR UPDATE OR DELETE ON spatial_exposure_features
FOR EACH ROW EXECUTE FUNCTION reject_completed_exposure_projection_mutation();

CREATE TRIGGER spatial_exposure_projection_zones_immutable
BEFORE INSERT OR UPDATE OR DELETE ON spatial_exposure_projection_zones
FOR EACH ROW EXECUTE FUNCTION reject_completed_exposure_projection_mutation();

CREATE TRIGGER spatial_exposure_feature_zones_immutable
BEFORE INSERT OR UPDATE OR DELETE ON spatial_exposure_feature_zones
FOR EACH ROW EXECUTE FUNCTION reject_completed_exposure_projection_mutation();
