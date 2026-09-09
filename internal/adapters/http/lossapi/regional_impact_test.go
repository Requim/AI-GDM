package lossapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
)

type impactStub struct {
	regionProjectionStub
	impact exposurecollection.RegionalImpact
}

func (s *impactStub) PreviewRegion(context.Context, string, string) (exposurecollection.RegionalImpact, error) {
	return s.impact, nil
}

func TestRegionalImpactIdentityAndEmptyResult(t *testing.T) {
	now := time.Now().UTC()
	stub := &impactStub{impact: exposurecollection.RegionalImpact{RegionCode: "CN-110000", SnapshotID: "snapshot-1",
		BoundaryID: "CHN-ADM1-AMAP-110000", BoundaryDigest: strings.Repeat("b", 64),
		ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Geometry: json.RawMessage("null")}}
	handler, err := NewWithRegionCatalogAndProjector(&estimatorStub{}, &assessmentStoreStub{},
		&assessmentStoreStub{}, "/api/v1/loss", testLogger(), nil, stub)
	if err != nil {
		t.Fatal(err)
	}
	response := performJSON(t, handler, http.MethodGet, "/regions/CN-110000/impact?snapshotId=snapshot-1", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"zoneCount":0`) {
		t.Fatalf("无风险交集响应: %d %s", response.Code, response.Body)
	}
	stub.impact.RegionCode = "CN-310000"
	response = performJSON(t, handler, http.MethodGet, "/regions/CN-110000/impact?snapshotId=snapshot-1", "")
	if response.Code == http.StatusOK {
		t.Fatal("返回了其他地区的风险覆盖")
	}
	stub.impact.RegionCode = "CN-110000"
	stub.impact.AreaSquareMeters = 10
	response = performJSON(t, handler, http.MethodGet, "/regions/CN-110000/impact?snapshotId=snapshot-1", "")
	if response.Code == http.StatusOK {
		t.Fatal("零个风险区却返回正面积")
	}
}

func TestCityDirectoryFiltersParentCode(t *testing.T) {
	catalog := regionCatalogStub{regions: []exposurecollection.AdministrativeRegion{
		{Code: "CN-130100", ParentCode: "CN-130000", Name: "石家庄市", Level: "ADM2"},
		{Code: "CN-320100", ParentCode: "CN-320000", Name: "南京市", Level: "ADM2"},
	}}
	handler, err := NewWithRegionCatalog(&estimatorStub{}, &assessmentStoreStub{},
		&assessmentStoreStub{}, "/api/v1/loss", testLogger(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	response := performJSON(t, handler, http.MethodGet, "/regions?level=ADM2&parentCode=CN-130000", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "石家庄市") ||
		strings.Contains(response.Body.String(), "南京市") {
		t.Fatalf("城市列表跨省: %d %s", response.Code, response.Body)
	}
}

func TestRegionalProjectionRejectsForeignRegion(t *testing.T) {
	stub := &regionProjectionStub{}
	stub.value.Input.Analysis.RegionCode = "CN-310000"
	stub.value.Input.Analysis.SnapshotID = "snapshot-1"
	handler, err := NewWithRegionCatalogAndProjector(&estimatorStub{}, &assessmentStoreStub{},
		&assessmentStoreStub{}, "/api/v1/loss", testLogger(), nil, stub)
	if err != nil {
		t.Fatal(err)
	}
	response := performJSON(t, handler, http.MethodPost, "/regions/CN-110000/projection", `{"snapshotId":"snapshot-1"}`)
	if response.Code == http.StatusCreated {
		t.Fatal("其他行政区投影被标记为当前区域")
	}
}
