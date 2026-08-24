CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE provider_artifacts (
    reference TEXT PRIMARY KEY,
    media_type TEXT NOT NULL,
    local_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    provenance JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE hazard_snapshots (
    id TEXT PRIMARY KEY,
    hazard_type TEXT NOT NULL,
    model_name TEXT NOT NULL,
    model_version TEXT NOT NULL,
    run_at TIMESTAMPTZ NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ NOT NULL,
    raster_reference TEXT NOT NULL,
    probability_semantics TEXT NOT NULL,
    thresholds JSONB NOT NULL,
    status TEXT NOT NULL,
    source JSONB NOT NULL,
    limitations JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX hazard_snapshots_latest_idx
    ON hazard_snapshots (hazard_type, run_at DESC);

CREATE TABLE risk_zones (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES hazard_snapshots(id) ON DELETE CASCADE,
    geometry geometry(Geometry, 4326) NOT NULL,
    probability_minimum DOUBLE PRECISION NOT NULL CHECK (probability_minimum BETWEEN 0 AND 1),
    probability_mean DOUBLE PRECISION NOT NULL CHECK (probability_mean BETWEEN 0 AND 1),
    probability_maximum DOUBLE PRECISION NOT NULL CHECK (probability_maximum BETWEEN 0 AND 1),
    risk_level TEXT NOT NULL,
    area_square_meters DOUBLE PRECISION NOT NULL CHECK (area_square_meters >= 0),
    admin_codes JSONB NOT NULL,
    input_references JSONB NOT NULL,
    limitations JSONB NOT NULL
);

CREATE INDEX risk_zones_snapshot_idx ON risk_zones (snapshot_id);
CREATE INDEX risk_zones_geometry_idx ON risk_zones USING GIST (geometry);
