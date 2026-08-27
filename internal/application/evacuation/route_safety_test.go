package evacuation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
	domainevacuation "github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestSafetyServiceFiltersAndSortsRoutes(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	zones := []hazard.RiskZone{testRiskZone("zone-route", "snapshot-1", "[[[116,39],[117,39],[117,40],[116,40],[116,39]]]")}
	planner := &safetyRoutePlannerStub{result: []domainevacuation.Route{
		testRoute("blocked", origin, destination, 1, 300, 3_000, "[[115,39.5],[118,39.5]]"),
		testRoute("high-risk", origin, destination, 30, 600, 2_500, "[[115,39.5],[115.5,39.8]]"),
		testRoute("low-risk", origin, destination, 5, 900, 2_000, "[[115,39.5],[115.5,38.8]]"),
	}}
	service := newSafetyService(t, planner, zones)
	result, err := service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelDriving,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routes) != 2 || result.Routes[0].ID != "low-risk" || result.Routes[1].ID != "high-risk" {
		t.Fatalf("路线安全排序错误: %+v", result.Routes)
	}
	if result.Routes[0].Rank != 1 || result.Routes[1].Rank != 2 {
		t.Fatalf("路线排名错误: %+v", result.Routes)
	}
	if len(result.Excluded) != 1 || result.Excluded[0].Route.ID != "blocked" ||
		len(result.Excluded[0].ZoneIDs) != 1 || result.Excluded[0].ZoneIDs[0] != "zone-route" {
		t.Fatalf("风险路线排除记录错误: %+v", result.Excluded)
	}
	if planner.calls != 1 {
		t.Fatalf("路线供应商调用次数错误: %d", planner.calls)
	}
}

func TestSafetyServiceAllowsCompleteEmptyRiskZones(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	planner := &safetyRoutePlannerStub{result: []domainevacuation.Route{
		testRoute("safe", origin, destination, 0, 300, 1_000, "[[115,39.5],[118,39.5]]"),
	}}
	planner.result[0].Mode = domainevacuation.TravelWalking
	service := newSafetyService(t, planner, []hazard.RiskZone{})
	result, err := service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelWalking,
	})
	if err != nil || len(result.Routes) != 1 || len(result.Excluded) != 0 {
		t.Fatalf("空风险区应允许安全路线: result=%+v err=%v", result, err)
	}
	if len(result.Limitations) < 2 {
		t.Fatalf("缺少无风险分数限制说明: %+v", result.Limitations)
	}
}

func TestSafetyServiceRejectsMissingRiskDataBeforePlanning(t *testing.T) {
	planner := &safetyRoutePlannerStub{}
	service, err := NewRouteSafetyService(planner, &routeRiskReaderStub{
		snapshot: testSnapshot(hazard.SnapshotAvailable), zones: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide,
		Origin:     spatial.Point{Longitude: 115, Latitude: 39.5}, Destination: spatial.Point{Longitude: 118, Latitude: 39.5},
		Mode: domainevacuation.TravelDriving,
	})
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("风险区缺失错误=%v", err)
	}
	if planner.calls != 0 {
		t.Fatal("风险区缺失时不应调用路线供应商")
	}
}

func TestSafetyServiceRejectsInvalidRouteGeometry(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	planner := &safetyRoutePlannerStub{result: []domainevacuation.Route{
		testRoute("invalid", origin, destination, 0, 300, 1_000, "[[115,39.5]]"),
	}}
	service := newSafetyService(t, planner, []hazard.RiskZone{})
	_, err := service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelDriving,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法路线几何错误=%v", err)
	}
}

func TestSafetyServiceTransitUsesDedicatedPlanner(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	planner := &safetyRoutePlannerStub{transitResult: []domainevacuation.Route{
		testRoute("transit", origin, destination, 2, 1_200, 4_000, "[[115,39.5],[118,39.5]]"),
	}}
	planner.transitResult[0].Mode = domainevacuation.TravelTransit
	service := newSafetyService(t, planner, []hazard.RiskZone{})
	result, err := service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelTransit, OriginCity: "010", DestinationCity: "021",
	})
	if err != nil || len(result.Routes) != 1 || planner.transitCalls != 1 || planner.calls != 0 {
		t.Fatalf("公交路线端口调用错误: result=%+v err=%v planner=%+v", result, err, planner)
	}
}

func TestSafetyServiceTransitRequiresDedicatedPlanner(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	planner := &routeOnlyPlannerStub{}
	service, err := NewRouteSafetyService(planner, &routeRiskReaderStub{
		snapshot: testSnapshot(hazard.SnapshotAvailable), zones: []hazard.RiskZone{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelTransit, OriginCity: "010", DestinationCity: "021",
	})
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("缺少公交端口错误=%v", err)
	}
}

func TestSafetyServiceRejectsProviderRiskScoreOutOfRange(t *testing.T) {
	origin := spatial.Point{Longitude: 115, Latitude: 39.5}
	destination := spatial.Point{Longitude: 118, Latitude: 39.5}
	route := testRoute("bad-score", origin, destination, 101, 300, 1_000, "[[115,39.5],[118,39.5]]")
	service := newSafetyService(t, &safetyRoutePlannerStub{result: []domainevacuation.Route{route}}, []hazard.RiskZone{})
	_, err := service.SearchRoutes(context.Background(), RouteSearchInput{
		HazardType: hazard.TypeLandslide, Origin: origin, Destination: destination,
		Mode: domainevacuation.TravelDriving,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("越界风险分数错误=%v", err)
	}
}

func newSafetyService(t *testing.T, planner portsRoutePlanner, zones []hazard.RiskZone) *SafetyService {
	t.Helper()
	service, err := NewRouteSafetyService(planner, &routeRiskReaderStub{
		snapshot: testSnapshot(hazard.SnapshotAvailable), zones: zones,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testRoute(id string, origin, destination spatial.Point, risk float64,
	duration int64, distance float64, coordinates string,
) domainevacuation.Route {
	return domainevacuation.Route{
		ID: id, Origin: origin, Destination: destination, Mode: domainevacuation.TravelDriving,
		DistanceMeters: distance, DurationSeconds: duration, RiskScore: risk,
		Geometry: spatial.Geometry{Type: "LineString", Coordinates: json.RawMessage(coordinates)},
	}
}

type portsRoutePlanner interface {
	Plan(context.Context, spatial.Point, spatial.Point, domainevacuation.TravelMode) ([]domainevacuation.Route, error)
}

type safetyRoutePlannerStub struct {
	result        []domainevacuation.Route
	transitResult []domainevacuation.Route
	err           error
	calls         int
	transitCalls  int
}

func (s *safetyRoutePlannerStub) Plan(_ context.Context, _, _ spatial.Point,
	_ domainevacuation.TravelMode,
) ([]domainevacuation.Route, error) {
	s.calls++
	return s.result, s.err
}

func (s *safetyRoutePlannerStub) PlanTransit(_ context.Context, _, _ spatial.Point, _, _ string,
) ([]domainevacuation.Route, error) {
	s.transitCalls++
	return s.transitResult, s.err
}

type routeOnlyPlannerStub struct{}

func (routeOnlyPlannerStub) Plan(context.Context, spatial.Point, spatial.Point,
	domainevacuation.TravelMode,
) ([]domainevacuation.Route, error) {
	return nil, nil
}

type routeRiskReaderStub struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
}

func (s *routeRiskReaderStub) LatestRisk(context.Context, hazard.Type) (hazard.Snapshot,
	[]hazard.RiskZone, error,
) {
	return s.snapshot, s.zones, s.err
}
