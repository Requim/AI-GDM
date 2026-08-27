CREATE TABLE loss_assessments (
    id TEXT PRIMARY KEY CHECK (BTRIM(id) <> ''),
    snapshot_id TEXT NOT NULL CHECK (BTRIM(snapshot_id) <> ''),
    hazard_type TEXT NOT NULL CHECK (BTRIM(hazard_type) <> ''),
    region_code TEXT NOT NULL CHECK (BTRIM(region_code) <> ''),
    formula_version TEXT NOT NULL CHECK (BTRIM(formula_version) <> ''),
    status TEXT NOT NULL CHECK (status IN ('available', 'insufficient_data')),
    calculated_at TIMESTAMPTZ NOT NULL,
    assessment JSONB NOT NULL CHECK (JSONB_TYPEOF(assessment) = 'object'),
    source_references JSONB NOT NULL CHECK (JSONB_TYPEOF(source_references) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (id, snapshot_id),
    CHECK (assessment->>'id' = id),
    CHECK (assessment->>'snapshotId' = snapshot_id),
    CHECK (assessment->>'formulaVersion' = formula_version),
    CHECK (assessment->>'status' = status)
);

CREATE INDEX loss_assessments_snapshot_idx
    ON loss_assessments (snapshot_id, calculated_at DESC, id DESC);
