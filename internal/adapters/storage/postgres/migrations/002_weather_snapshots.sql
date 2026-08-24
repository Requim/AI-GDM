CREATE TABLE weather_snapshot_batches (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    point_set_hash CHAR(64) NOT NULL CHECK (point_set_hash ~ '^[0-9a-f]{64}$'),
    point_count INTEGER NOT NULL CHECK (point_count > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX weather_snapshot_batches_latest_idx
    ON weather_snapshot_batches (point_set_hash, created_at DESC, id DESC);

CREATE TABLE weather_snapshots (
    batch_id BIGINT NOT NULL REFERENCES weather_snapshot_batches(id) ON DELETE CASCADE,
    point_key TEXT NOT NULL CHECK (
        point_key ~ '^-?[0-9]+[.][0-9]{6},-?[0-9]+[.][0-9]{6}$'
    ),
    location geometry(Point, 4326) NOT NULL,
    hourly JSONB NOT NULL CHECK (jsonb_typeof(hourly) = 'array'),
    source JSONB NOT NULL CHECK (jsonb_typeof(source) = 'object'),
    PRIMARY KEY (batch_id, point_key)
);

CREATE INDEX weather_snapshots_location_idx ON weather_snapshots USING GIST (location);
