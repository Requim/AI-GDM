CREATE TABLE loss_exposure_baselines (
    dataset_version TEXT NOT NULL CHECK (BTRIM(dataset_version) <> ''),
    id TEXT NOT NULL CHECK (BTRIM(id) <> ''),
    region_code TEXT NOT NULL CHECK (BTRIM(region_code) <> ''),
    exposure_kind TEXT NOT NULL CHECK (exposure_kind IN ('population', 'road')),
    quantity DOUBLE PRECISION NOT NULL
        CHECK (quantity >= 0 AND quantity < 'Infinity'::DOUBLE PRECISION),
    unit TEXT NOT NULL,
    data_year INTEGER NOT NULL CHECK (data_year BETWEEN 1900 AND 9999),
    coverage_ratio DOUBLE PRECISION NOT NULL
        CHECK (coverage_ratio > 0 AND coverage_ratio <= 1),
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ,
    source JSONB NOT NULL CHECK (JSONB_TYPEOF(source) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (dataset_version, id),
    CHECK ((exposure_kind = 'population' AND unit = 'people')
        OR (exposure_kind = 'road' AND unit = 'meters')),
    CHECK (source->>'datasetVersion' = dataset_version),
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE INDEX loss_exposure_baselines_latest_idx
    ON loss_exposure_baselines (region_code, exposure_kind, valid_from DESC, dataset_version DESC);

CREATE TABLE loss_cost_baselines (
    dataset_version TEXT NOT NULL CHECK (BTRIM(dataset_version) <> ''),
    id TEXT NOT NULL CHECK (BTRIM(id) <> ''),
    asset_type TEXT NOT NULL CHECK (asset_type IN ('building', 'road', 'facility')),
    region_code TEXT NOT NULL CHECK (BTRIM(region_code) <> ''),
    unit TEXT NOT NULL CHECK (BTRIM(unit) <> ''),
    low_cents BIGINT NOT NULL CHECK (low_cents >= 0),
    central_cents BIGINT NOT NULL CHECK (central_cents >= low_cents),
    high_cents BIGINT NOT NULL CHECK (high_cents >= central_cents),
    currency TEXT NOT NULL CHECK (currency = 'CNY'),
    price_base_date TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('demo_only', 'approved')),
    approved_by TEXT NOT NULL DEFAULT '',
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ,
    source JSONB NOT NULL CHECK (JSONB_TYPEOF(source) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (dataset_version, id),
    CHECK ((status = 'demo_only' AND BTRIM(approved_by) = '')
        OR (status = 'approved' AND BTRIM(approved_by) <> '')),
    CHECK (source->>'datasetVersion' = dataset_version),
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE INDEX loss_cost_baselines_latest_idx
    ON loss_cost_baselines (region_code, valid_from DESC, dataset_version DESC);

CREATE TABLE loss_vulnerabilities (
    dataset_version TEXT NOT NULL CHECK (BTRIM(dataset_version) <> ''),
    id TEXT NOT NULL CHECK (BTRIM(id) <> ''),
    asset_type TEXT NOT NULL CHECK (asset_type IN ('building', 'road', 'facility')),
    hazard_type TEXT NOT NULL CHECK (BTRIM(hazard_type) <> ''),
    intensity_band TEXT NOT NULL CHECK (BTRIM(intensity_band) <> ''),
    impact_fraction_low DOUBLE PRECISION NOT NULL CHECK (impact_fraction_low >= 0 AND impact_fraction_low <= 1),
    impact_fraction_mid DOUBLE PRECISION NOT NULL CHECK (impact_fraction_mid >= impact_fraction_low AND impact_fraction_mid <= 1),
    impact_fraction_high DOUBLE PRECISION NOT NULL CHECK (impact_fraction_high >= impact_fraction_mid AND impact_fraction_high <= 1),
    damage_ratio_low DOUBLE PRECISION NOT NULL CHECK (damage_ratio_low >= 0 AND damage_ratio_low <= 1),
    damage_ratio_mid DOUBLE PRECISION NOT NULL CHECK (damage_ratio_mid >= damage_ratio_low AND damage_ratio_mid <= 1),
    damage_ratio_high DOUBLE PRECISION NOT NULL CHECK (damage_ratio_high >= damage_ratio_mid AND damage_ratio_high <= 1),
    calibration_region TEXT NOT NULL CHECK (BTRIM(calibration_region) <> ''),
    status TEXT NOT NULL CHECK (status IN ('demo_only', 'approved')),
    approved_by TEXT NOT NULL DEFAULT '',
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ,
    source JSONB NOT NULL CHECK (JSONB_TYPEOF(source) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (dataset_version, id),
    CHECK ((status = 'demo_only' AND BTRIM(approved_by) = '')
        OR (status = 'approved' AND BTRIM(approved_by) <> '')),
    CHECK (source->>'datasetVersion' = dataset_version),
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE INDEX loss_vulnerabilities_latest_idx
    ON loss_vulnerabilities (hazard_type, valid_from DESC, dataset_version DESC);
