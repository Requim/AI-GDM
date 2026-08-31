ALTER TABLE loss_assessments
    DROP CONSTRAINT loss_assessments_status_check;

ALTER TABLE loss_assessments
    ADD CONSTRAINT loss_assessments_status_check
    CHECK (status IN ('available', 'insufficient_data', 'reference_only'));
