ALTER TABLE risk_zones
    ADD COLUMN area_calculated BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE risk_zones
    ADD CONSTRAINT risk_zones_id_snapshot_unique UNIQUE (id, snapshot_id);

CREATE TABLE spatial_analyses (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES hazard_snapshots(id) ON DELETE CASCADE,
    algorithm_version TEXT NOT NULL CHECK (BTRIM(algorithm_version) <> ''),
    area_method TEXT NOT NULL CHECK (BTRIM(area_method) <> ''),
    status TEXT NOT NULL CHECK (BTRIM(status) <> ''),
    zone_count INTEGER NOT NULL CHECK (zone_count >= 0),
    merged_area_square_meters DOUBLE PRECISION NOT NULL
        CHECK (merged_area_square_meters >= 0 AND merged_area_square_meters < 'Infinity'::DOUBLE PRECISION),
    calculated_at TIMESTAMPTZ NOT NULL,
    dataset_references JSONB NOT NULL CHECK (JSONB_TYPEOF(dataset_references) = 'array'),
    area_input_references JSONB NOT NULL CHECK (JSONB_TYPEOF(area_input_references) = 'array'),
    input_references JSONB NOT NULL CHECK (JSONB_TYPEOF(input_references) = 'array'),
    limitations JSONB NOT NULL CHECK (JSONB_TYPEOF(limitations) = 'array'),
    CONSTRAINT spatial_analyses_id_snapshot_unique UNIQUE (id, snapshot_id)
);

CREATE INDEX spatial_analyses_snapshot_latest_idx
    ON spatial_analyses (snapshot_id, calculated_at DESC, id DESC);

CREATE TABLE spatial_zone_results (
    analysis_id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    area_square_meters DOUBLE PRECISION NOT NULL
        CHECK (area_square_meters >= 0 AND area_square_meters < 'Infinity'::DOUBLE PRECISION),
    admin_matches JSONB NOT NULL CHECK (JSONB_TYPEOF(admin_matches) = 'object'),
    exposures JSONB NOT NULL CHECK (JSONB_TYPEOF(exposures) = 'object'),
    input_references JSONB NOT NULL CHECK (JSONB_TYPEOF(input_references) = 'array'),
    limitations JSONB NOT NULL CHECK (JSONB_TYPEOF(limitations) = 'array'),
    PRIMARY KEY (analysis_id, zone_id),
    CONSTRAINT spatial_zone_results_analysis_fk
        FOREIGN KEY (analysis_id, snapshot_id)
        REFERENCES spatial_analyses(id, snapshot_id) ON DELETE CASCADE,
    CONSTRAINT spatial_zone_results_zone_fk
        FOREIGN KEY (zone_id, snapshot_id)
        REFERENCES risk_zones(id, snapshot_id) ON DELETE CASCADE
);

CREATE INDEX spatial_zone_results_zone_idx
    ON spatial_zone_results (zone_id, analysis_id);

CREATE INDEX spatial_zone_results_snapshot_idx
    ON spatial_zone_results (snapshot_id, analysis_id);
