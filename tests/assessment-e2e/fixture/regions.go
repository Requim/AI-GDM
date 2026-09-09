package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
)

type fixtureRegions struct{}

func (fixtureRegions) RegionCatalog(_ context.Context, _, level string) ([]exposurecollection.AdministrativeRegion, error) {
	if level == "ADM1" {
		return []exposurecollection.AdministrativeRegion{
			{Code: "CN-130000", Name: "河北省", Level: "ADM1"},
			{Code: "CN-320000", Name: "江苏省", Level: "ADM1"},
		}, nil
	}
	return []exposurecollection.AdministrativeRegion{
		{Code: "CN-130100", ParentCode: "CN-130000", Name: "石家庄市", Level: "ADM2"},
		{Code: "CN-320100", ParentCode: "CN-320000", Name: "南京市", Level: "ADM2"},
	}, nil
}

func (fixtureRegions) CollectRegion(_ context.Context, snapshot, code string) (exposurecollection.ExposureProjection, error) {
	now := fixtureServiceNow()
	return exposurecollection.ExposureProjection{Input: applicationloss.LossInputProjection{
		Analysis: applicationloss.LossSpatialProjection{SnapshotID: snapshot, RegionCode: code,
			Status: "available", ProjectionID: "exposure-region-fixture"}},
		ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour)}, nil
}

func (fixtureRegions) PreviewRegion(_ context.Context, snapshot, code string) (exposurecollection.RegionalImpact, error) {
	now := fixtureServiceNow()
	return exposurecollection.RegionalImpact{SnapshotID: snapshot, RegionCode: code,
		BoundaryID: "CHN-ADM1-fixture", BoundaryDigest: "fixture-boundary-digest",
		ZoneCount: 1, AreaSquareMeters: 150, ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour),
		Geometry: json.RawMessage(`{"type":"Polygon","coordinates":[[[116,39],[116.01,39],[116.01,39.01],[116,39]]]}`)}, nil
}

func bindFixtureRegion(value applicationloss.LossInputProjection, code string) (applicationloss.LossInputProjection, error) {
	if code == "" || code == "CN" {
		return value, nil
	}
	value.Analysis.RegionCode = code
	value.Analysis.AdminBoundaryID = "CHN-ADM1-fixture"
	for index := range value.Zones {
		value.Zones[index].AdminCodes = []string{code}
	}
	if err := applicationloss.BindRiskProjectionIdentity(&value); err != nil {
		return value, err
	}
	return value, nil
}

func (r fixtureLossProjectionReader) ReadLossInputForRegion(ctx context.Context, snapshotID, code string,
	now time.Time, limits applicationloss.RiskProjectionLimits,
) (applicationloss.LossInputProjection, error) {
	if code != r.value.Analysis.RegionCode {
		return applicationloss.LossInputProjection{}, fmt.Errorf("%w: fixture 区域不匹配", domain.ErrNotFound)
	}
	return r.ReadLossInput(ctx, snapshotID, now, limits)
}
