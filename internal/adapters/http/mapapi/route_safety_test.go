package mapapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestMapAPIRoutesUseSafetySearcherWhenConfigured(t *testing.T) {
	safety := &routeSafetyStub{result: applicationevacuation.RouteSearchResult{
		Snapshot: hazard.Snapshot{ID: "snapshot-1", HazardType: hazard.TypeLandslide},
		Routes:   []evacuation.Route{{ID: "safe-route", Rank: 1}},
		Excluded: []applicationevacuation.ExcludedRoute{{
			Route: evacuation.Route{ID: "blocked-route"}, ZoneIDs: []string{"zone-1"},
		}},
	}}
	planner := &routePlannerStub{}
	handler := newSafetyMapHandler(t, &facilitySearcherStub{}, planner, safety)
	response := serveJSON(t, handler, http.MethodPost, "/routes", `{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"driving"}`)
	if response.Code != http.StatusOK || safety.calls != 1 || planner.mode != "" {
		t.Fatalf("安全路线服务未接管: status=%d body=%s safety=%+v planner=%+v", response.Code, response.Body.String(), safety, planner)
	}
	var payload struct {
		Data applicationevacuation.RouteSearchResult `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Snapshot.ID != "snapshot-1" || len(payload.Data.Routes) != 1 ||
		len(payload.Data.Excluded) != 1 {
		t.Fatalf("安全路线响应错误: %+v", payload.Data)
	}
}

func TestMapAPISafeRoutesForwardTransitCityCodes(t *testing.T) {
	safety := &routeSafetyStub{}
	handler := newSafetyMapHandler(t, &facilitySearcherStub{}, &routePlannerStub{}, safety)
	response := serveJSON(t, handler, http.MethodPost, "/routes", `{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"transit","originCity":"010","destinationCity":"021"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("公交安全路线请求失败: status=%d body=%s", response.Code, response.Body.String())
	}
	if safety.input.Mode != evacuation.TravelTransit || safety.input.OriginCity != "010" || safety.input.DestinationCity != "021" {
		t.Fatalf("公交参数未转发: %+v", safety.input)
	}
}

func TestMapAPISafeRoutesMapInsufficientData(t *testing.T) {
	safety := &routeSafetyStub{err: domain.ErrInsufficientData}
	handler := newSafetyMapHandler(t, &facilitySearcherStub{}, &routePlannerStub{}, safety)
	response := serveJSON(t, handler, http.MethodPost, "/routes", `{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"walking"}`)
	assertError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestMapAPISafeRoutesRejectTransitWithoutCities(t *testing.T) {
	safety := &routeSafetyStub{}
	handler := newSafetyMapHandler(t, &facilitySearcherStub{}, &routePlannerStub{}, safety)
	response := serveJSON(t, handler, http.MethodPost, "/routes", `{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"transit"}`)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	if safety.calls != 0 {
		t.Fatal("缺少公交城市编码时不应调用安全路线服务")
	}
}

type routeSafetyStub struct {
	result applicationevacuation.RouteSearchResult
	err    error
	input  applicationevacuation.RouteSearchInput
	calls  int
}

func (s *routeSafetyStub) SearchRoutes(_ context.Context,
	input applicationevacuation.RouteSearchInput,
) (applicationevacuation.RouteSearchResult, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return applicationevacuation.RouteSearchResult{}, errors.Join(domain.ErrInsufficientData, s.err)
	}
	return s.result, nil
}

var _ applicationevacuation.RouteSafetySearcher = (*routeSafetyStub)(nil)
var _ ports.RoutePlanner = (*routePlannerStub)(nil)

func newSafetyMapHandler(t *testing.T, facilities applicationevacuation.FacilitySearcher,
	routes ports.RoutePlanner, safety applicationevacuation.RouteSafetySearcher,
) http.Handler {
	t.Helper()
	handler, err := NewWithSafety(facilities, routes, safety,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return middleware.RequestID(handler)
}
