package mapapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestMapAPIProxiesNormalizedRequests(t *testing.T) {
	places := &placeFinderStub{result: []evacuation.Facility{{ID: "shelter-1"}}}
	routes := &routePlannerStub{result: []evacuation.Route{{ID: "route-1"}}}
	handler := newHandler(t, places, routes)

	placeResponse := serveJSON(t, handler, http.MethodPost, "/places/nearby",
		`{"center":{"longitude":116.4,"latitude":39.9},"kind":"shelter","radiusMeters":2000}`)
	if placeResponse.Code != http.StatusOK || places.radius != 2000 || places.kind != evacuation.FacilityShelter {
		t.Fatalf("POI 代理错误: status=%d body=%s stub=%+v", placeResponse.Code, placeResponse.Body.String(), places)
	}
	if strings.Contains(placeResponse.Body.String(), "key") || strings.Contains(placeResponse.Body.String(), "jscode") {
		t.Fatalf("响应包含供应商密钥字段: %s", placeResponse.Body.String())
	}

	routeResponse := serveJSON(t, handler, http.MethodPost, "/routes",
		`{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"walking"}`)
	if routeResponse.Code != http.StatusOK || routes.mode != evacuation.TravelWalking {
		t.Fatalf("路线代理错误: status=%d body=%s stub=%+v", routeResponse.Code, routeResponse.Body.String(), routes)
	}
}

func TestMapAPIRejectsInvalidInputBeforeProvider(t *testing.T) {
	places := &placeFinderStub{}
	routes := &routePlannerStub{}
	handler := newHandler(t, places, routes)
	response := serveJSON(t, handler, http.MethodPost, "/places/nearby",
		`{"center":{"longitude":999,"latitude":39.9},"kind":"shelter","radiusMeters":100}`)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	if places.calls != 0 {
		t.Fatal("无效请求仍调用了高德适配器")
	}
}

func TestMapAPIHidesProviderFailure(t *testing.T) {
	handler := newHandler(t, &placeFinderStub{err: errors.New("供应商口令 secret-key")}, &routePlannerStub{})
	response := serveJSON(t, handler, http.MethodPost, "/places/nearby",
		`{"center":{"longitude":116.4,"latitude":39.9},"kind":"hospital","radiusMeters":100}`)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret-key") {
		t.Fatalf("供应商错误泄漏: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMapAPIReturnsJSONForUnknownRoute(t *testing.T) {
	handler := newHandler(t, &placeFinderStub{}, &routePlannerStub{})
	response := serveJSON(t, handler, http.MethodGet, "/unknown", "{}")
	assertError(t, response, http.StatusNotFound, "route_not_found")
}

func TestMapAPIRejectsOversizedRequestBody(t *testing.T) {
	handler := newHandler(t, &placeFinderStub{}, &routePlannerStub{})
	body := strings.Repeat("x", maxRequestBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/places/nearby", strings.NewReader(body))
	request.ContentLength = int64(len(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
}

func TestMapAPIRejectsTransitUntilCityContractExists(t *testing.T) {
	handler := newHandler(t, &placeFinderStub{}, &routePlannerStub{})
	response := serveJSON(t, handler, http.MethodPost, "/routes",
		`{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"transit"}`)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
}

func newHandler(t *testing.T, places ports.PlaceFinder, routes ports.RoutePlanner) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := New(places, routes, logger)
	if err != nil {
		t.Fatal(err)
	}
	return middleware.RequestID(handler)
}

func serveJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code || payload.Error.RequestID == "" {
		t.Fatalf("error=%+v", payload.Error)
	}
}

type placeFinderStub struct {
	result []evacuation.Facility
	err    error
	center spatial.Point
	kind   evacuation.FacilityType
	radius int
	calls  int
}

func (s *placeFinderStub) FindNearby(_ context.Context, center spatial.Point,
	kind evacuation.FacilityType, radiusM int,
) ([]evacuation.Facility, error) {
	s.calls++
	s.center, s.kind, s.radius = center, kind, radiusM
	return s.result, s.err
}

type routePlannerStub struct {
	result []evacuation.Route
	err    error
	mode   evacuation.TravelMode
}

func (s *routePlannerStub) Plan(_ context.Context, _, _ spatial.Point,
	mode evacuation.TravelMode,
) ([]evacuation.Route, error) {
	s.mode = mode
	return s.result, s.err
}
