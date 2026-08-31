ALTER TABLE hazard_snapshots
    ADD COLUMN coverage JSONB;

ALTER TABLE hazard_snapshots
    ADD CONSTRAINT hazard_snapshots_coverage_object
    CHECK (coverage IS NULL OR JSONB_TYPEOF(coverage) = 'object');

ALTER TABLE hazard_snapshots
    ADD COLUMN superseded_at TIMESTAMPTZ,
    ADD COLUMN superseded_by_coverage JSONB;

ALTER TABLE hazard_snapshots
    ADD CONSTRAINT hazard_snapshots_superseded_coverage_object
    CHECK (superseded_by_coverage IS NULL OR JSONB_TYPEOF(superseded_by_coverage) = 'object'),
    ADD CONSTRAINT hazard_snapshots_supersession_complete
    CHECK ((superseded_at IS NULL) = (superseded_by_coverage IS NULL)),
    ADD CONSTRAINT hazard_snapshots_supersession_changes_coverage
    CHECK (superseded_by_coverage IS NULL OR coverage IS NULL OR ROW(
        coverage->>'boundaryId', coverage->>'boundaryVersion',
        coverage->>'sha256', coverage->>'geometrySha256'
    ) IS DISTINCT FROM ROW(
        superseded_by_coverage->>'boundaryId', superseded_by_coverage->>'boundaryVersion',
        superseded_by_coverage->>'sha256', superseded_by_coverage->>'geometrySha256'
    ));

CREATE INDEX hazard_snapshots_current_complete_latest_idx
    ON hazard_snapshots (hazard_type, model_name, run_at DESC, id DESC)
    WHERE analysis_complete = TRUE AND status IN ('available', 'stale')
        AND superseded_at IS NULL;
