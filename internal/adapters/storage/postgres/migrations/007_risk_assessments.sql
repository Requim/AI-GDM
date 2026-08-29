CREATE TABLE risk_assessments (
    snapshot_id TEXT PRIMARY KEY REFERENCES hazard_snapshots(id) ON DELETE RESTRICT,
    assessment_id TEXT NOT NULL UNIQUE CHECK (BTRIM(assessment_id) <> ''),
    hazard_type TEXT NOT NULL CHECK (BTRIM(hazard_type) <> ''),
    rule_version TEXT NOT NULL CHECK (BTRIM(rule_version) <> ''),
    status TEXT NOT NULL CHECK (status IN ('available', 'degraded')),
    data_status TEXT NOT NULL CHECK (data_status IN ('current', 'fallback')),
    evaluated_at TIMESTAMPTZ NOT NULL,
    snapshot JSONB NOT NULL CHECK (JSONB_TYPEOF(snapshot) = 'object'),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^[0-9a-f]{64}$'),
    assessment JSONB NOT NULL CHECK (JSONB_TYPEOF(assessment) = 'object'),
    assessment_digest TEXT NOT NULL CHECK (assessment_digest ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (assessment->>'id' = assessment_id),
    CHECK (assessment->>'snapshotId' = snapshot_id),
    CHECK (assessment->>'hazardType' = hazard_type),
    CHECK (assessment->>'ruleVersion' = rule_version),
    CHECK (assessment->>'status' = status),
    CHECK (assessment->>'dataStatus' = data_status),
    CHECK (snapshot->>'id' = snapshot_id),
    CHECK (snapshot->>'hazardType' = hazard_type)
);

CREATE INDEX risk_assessments_evaluated_idx
    ON risk_assessments (evaluated_at DESC, assessment_id DESC);
