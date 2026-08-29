ALTER TABLE loss_assessments
    ADD COLUMN IF NOT EXISTS assessment_bytes INTEGER
        GENERATED ALWAYS AS (OCTET_LENGTH(assessment::TEXT)) STORED,
    ADD COLUMN IF NOT EXISTS source_references_bytes INTEGER
        GENERATED ALWAYS AS (OCTET_LENGTH(source_references::TEXT)) STORED;

ALTER TABLE loss_assessments
    ADD CONSTRAINT loss_assessments_id_bytes
        CHECK (BTRIM(id) = id AND OCTET_LENGTH(id) BETWEEN 1 AND 128),
    ADD CONSTRAINT loss_assessments_snapshot_bytes
        CHECK (BTRIM(snapshot_id) = snapshot_id AND OCTET_LENGTH(snapshot_id) BETWEEN 1 AND 128),
    ADD CONSTRAINT loss_assessments_hazard_bytes
        CHECK (BTRIM(hazard_type) = hazard_type AND OCTET_LENGTH(hazard_type) BETWEEN 1 AND 128),
    ADD CONSTRAINT loss_assessments_region_bytes
        CHECK (BTRIM(region_code) = region_code AND OCTET_LENGTH(region_code) BETWEEN 1 AND 128),
    ADD CONSTRAINT loss_assessments_formula_bytes
        CHECK (BTRIM(formula_version) = formula_version AND OCTET_LENGTH(formula_version) BETWEEN 1 AND 128),
    ADD CONSTRAINT loss_assessments_status_bytes
        CHECK (BTRIM(status) = status AND OCTET_LENGTH(status) BETWEEN 1 AND 64),
    ADD CONSTRAINT loss_assessments_assessment_bytes
        CHECK (assessment_bytes BETWEEN 1 AND 1048576),
    ADD CONSTRAINT loss_assessments_references_bytes
        CHECK (source_references_bytes BETWEEN 1 AND 1048576),
    ADD CONSTRAINT loss_assessments_id_binding
        CHECK (((assessment ? 'id') AND (assessment->>'id' = id)) IS TRUE),
    ADD CONSTRAINT loss_assessments_snapshot_binding
        CHECK (((assessment ? 'snapshotId') AND (assessment->>'snapshotId' = snapshot_id)) IS TRUE),
    ADD CONSTRAINT loss_assessments_hazard_binding
        CHECK (((assessment ? 'hazardType') AND (assessment->>'hazardType' = hazard_type)) IS TRUE),
    ADD CONSTRAINT loss_assessments_region_binding
        CHECK (((assessment ? 'regionCode') AND (assessment->>'regionCode' = region_code)) IS TRUE),
    ADD CONSTRAINT loss_assessments_formula_binding
        CHECK (((assessment ? 'formulaVersion') AND (assessment->>'formulaVersion' = formula_version)) IS TRUE),
    ADD CONSTRAINT loss_assessments_status_binding
        CHECK (((assessment ? 'status') AND (assessment->>'status' = status)) IS TRUE),
    ADD CONSTRAINT loss_assessments_calculated_at_binding
        CHECK (((assessment ? 'calculatedAt') AND
            ((assessment->>'calculatedAt')::TIMESTAMPTZ = calculated_at)) IS TRUE),
    ADD CONSTRAINT loss_assessments_references_binding
        CHECK (((assessment ? 'inputReferences') AND
            (assessment->'inputReferences' = source_references)) IS TRUE);
