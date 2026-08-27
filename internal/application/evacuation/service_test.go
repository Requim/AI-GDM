package evacuation

import (
	"context"
	"errors"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
	domainevacuation "github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestServiceSearchFiltersFacilitiesInsideRiskZones(t *testing.T) {
	zone := testRiskZone("zone-1", "snapshot-1", `[[[116,39],[117,39],[117,40],[116,40],[116,39]]]`)
	places := &placeFinderStub{result: []domainevacuation.Facility{
		{ID: "inside", Name: "风险区内医院", Type: domainevacuation.FacilityHospital,
			Location: spatial.Point{Longitude: 116.5, Latitude: 39.5}},
		{ID: "outside", Name: "风险区外医院", Type: domainevacuation.FacilityHospital,
			Location: spatial.Point{Longitude: 118, Latitude: 39.5}},
	}}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotAvailable), zones: []hazard.RiskZone{zone}}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide,
		Center:     spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind:       domainevacuation.FacilityHospital, RadiusMeters: 2_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facilities) != 1 || result.Facilities[0].ID != "outside" {
		t.Fatalf("安全候选错误: %+v", result.Facilities)
	}
	if len(result.Excluded) != 1 || result.Excluded[0].Facility.ID != "inside" {
		t.Fatalf("排除候选错误: %+v", result.Excluded)
	}
	if len(result.Excluded[0].ZoneIDs) != 1 || result.Excluded[0].ZoneIDs[0] != zone.ID {
		t.Fatalf("排除风险区错误: %+v", result.Excluded[0])
	}
	if places.calls != 1 || risks.calls != 1 {
		t.Fatalf("依赖调用次数错误: places=%d risks=%d", places.calls, risks.calls)
	}
}

func TestServiceSearchRejectsMissingRiskZonesBeforeProvider(t *testing.T) {
	places := &placeFinderStub{result: []domainevacuation.Facility{{
		ID: "candidate", Type: domainevacuation.FacilityShelter,
		Location: spatial.Point{Longitude: 116.5, Latitude: 39.5},
	}}}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotAvailable), zones: nil}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide,
		Center:     spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind:       domainevacuation.FacilityShelter, RadiusMeters: 1_000,
	})
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("缺少风险区应返回 ErrInsufficientData，实际为 %v", err)
	}
	if places.calls != 0 {
		t.Fatal("风险区缺失时不应调用 POI 供应商")
	}
}

func TestServiceSearchConvertsMissingSnapshotToInsufficientData(t *testing.T) {
	risks := &riskReaderStub{err: domain.ErrNotFound}
	service, err := NewService(&placeFinderStub{}, risks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide,
		Center:     spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind:       domainevacuation.FacilityShelter, RadiusMeters: 1_000,
	})
	if !errors.Is(err, domain.ErrInsufficientData) || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺少快照应同时保留数据不足和未找到语义，实际为 %v", err)
	}
}

func TestServiceSearchAllowsCompleteEmptyRiskZoneSet(t *testing.T) {
	places := &placeFinderStub{result: []domainevacuation.Facility{{
		ID: "shelter-1", Type: domainevacuation.FacilityShelter,
		Location: spatial.Point{Longitude: 116.5, Latitude: 39.5},
	}}}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotAvailable), zones: []hazard.RiskZone{}}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide,
		Center:     spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind:       domainevacuation.FacilityShelter, RadiusMeters: 1_000,
	})
	if err != nil || len(result.Facilities) != 1 || len(result.Excluded) != 0 {
		t.Fatalf("完整空风险区结果错误: result=%+v err=%v", result, err)
	}
}

func TestServiceSearchRejectsInvalidRiskGeometryBeforeProvider(t *testing.T) {
	invalidZone := testRiskZone("zone-invalid", "snapshot-1", `[[[116,39],[117,39],[117,40],[116,40]]]`)
	places := &placeFinderStub{}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotAvailable), zones: []hazard.RiskZone{invalidZone}}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide, Center: spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind: domainevacuation.FacilityShelter, RadiusMeters: 1_000,
	})
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("非法风险几何应返回 ErrInsufficientData，实际为 %v", err)
	}
	if places.calls != 0 {
		t.Fatal("非法风险几何时不应调用 POI 供应商")
	}
}

func TestServiceSearchAddsStaleLimitation(t *testing.T) {
	zone := testRiskZone("zone-1", "snapshot-1", `[[[116,39],[117,39],[117,40],[116,40],[116,39]]]`)
	places := &placeFinderStub{result: []domainevacuation.Facility{}}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotStale), zones: []hazard.RiskZone{zone}}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide, Center: spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind: domainevacuation.FacilityTransport, RadiusMeters: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Limitations) != 1 {
		t.Fatalf("过期风险区限制说明缺失: %+v", result.Limitations)
	}
}

func TestServiceSearchRejectsProviderTypeMismatch(t *testing.T) {
	zone := testRiskZone("zone-1", "snapshot-1", `[[[116,39],[117,39],[117,40],[116,40],[116,39]]]`)
	places := &placeFinderStub{result: []domainevacuation.Facility{{
		ID: "wrong-type", Type: domainevacuation.FacilityHospital,
		Location: spatial.Point{Longitude: 118, Latitude: 39.5},
	}}}
	risks := &riskReaderStub{snapshot: testSnapshot(hazard.SnapshotAvailable), zones: []hazard.RiskZone{zone}}
	service, err := NewService(places, risks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchInput{
		HazardType: hazard.TypeLandslide, Center: spatial.Point{Longitude: 116.4, Latitude: 39.4},
		Kind: domainevacuation.FacilityShelter, RadiusMeters: 500,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("供应商类型不一致应返回 ErrInvalidInput，实际为 %v", err)
	}
}

func testSnapshot(status hazard.SnapshotStatus) hazard.Snapshot {
	return hazard.Snapshot{ID: "snapshot-1", HazardType: hazard.TypeLandslide, Status: status}
}

func testRiskZone(id, snapshotID, coordinates string) hazard.RiskZone {
	return hazard.RiskZone{
		ID: id, SnapshotID: snapshotID,
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: []byte(coordinates)},
	}
}

type placeFinderStub struct {
	result []domainevacuation.Facility
	err    error
	calls  int
}

func (s *placeFinderStub) FindNearby(_ context.Context, _ spatial.Point,
	_ domainevacuation.FacilityType, _ int,
) ([]domainevacuation.Facility, error) {
	s.calls++
	return s.result, s.err
}

type riskReaderStub struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
	calls    int
}

func (s *riskReaderStub) LatestRisk(_ context.Context, _ hazard.Type) (hazard.Snapshot, []hazard.RiskZone, error) {
	s.calls++
	return s.snapshot, s.zones, s.err
}
