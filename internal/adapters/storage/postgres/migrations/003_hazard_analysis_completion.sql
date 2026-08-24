ALTER TABLE hazard_snapshots
    ADD COLUMN analysis_complete BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX hazard_snapshots_complete_latest_idx
    ON hazard_snapshots (hazard_type, model_name, run_at DESC, id DESC)
    WHERE analysis_complete = TRUE AND status IN ('available', 'stale');
