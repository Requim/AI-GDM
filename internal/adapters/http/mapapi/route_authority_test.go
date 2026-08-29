package mapapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestMapAPISafeRouteReturnsAuthorityReference(t *testing.T) {
	route := projectionRoute("safe-route", 2, 0, true)
	route.Rank = 1
	safety := &routeSafetyStub{result: applicationevacuation.RouteSearchResult{
		Snapshot: hazard.Snapshot{ID: "snapshot-1"}, Routes: []evacuation.Route{route},
		RuleVersion: applicationevacuation.RouteSafetyRuleVersion,
	}}
	recorder := &routeAuthorityRecorderStub{reference: &report.AnalysisReference{
		Kind: report.AuthorityEvacuationRoute, ID: "route-authority-1",
	}}
	handler := newAuthorityMapHandler(t, safety, recorder)
	response := serveJSON(t, handler, http.MethodPost, "/routes", validRouteRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data safeRouteResult `json:"data"`
	}
	decodeJSON(t, response.Body.String(), &payload)
	if len(payload.Data.Routes) != 1 || payload.Data.Routes[0].AnalysisRef == nil ||
		payload.Data.Routes[0].AnalysisRef.ID != "route-authority-1" {
		t.Fatalf("安全路线缺少 analysisRef: %+v", payload.Data.Routes)
	}
	if recorder.ruleVersion != applicationevacuation.RouteSafetyRuleVersion || recorder.calls != 1 {
		t.Fatalf("路线权威规则或调用次数错误: %+v", recorder)
	}
}

func TestMapAPIRouteAuthorityCacheFailureKeepsRoute(t *testing.T) {
	route := projectionRoute("safe-route", 2, 0, false)
	route.Rank = 1
	safety := &routeSafetyStub{result: applicationevacuation.RouteSearchResult{
		Snapshot: hazard.Snapshot{ID: "snapshot-1"}, Routes: []evacuation.Route{route},
		RuleVersion: applicationevacuation.RouteSafetyRuleVersion,
	}}
	recorder := &routeAuthorityRecorderStub{err: errors.New("redis down")}
	response := serveJSON(t, newAuthorityMapHandler(t, safety, recorder),
		http.MethodPost, "/routes", validRouteRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("缓存失败不应影响确定性路线: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data safeRouteResult `json:"data"`
	}
	decodeJSON(t, response.Body.String(), &payload)
	if len(payload.Data.Routes) != 1 || payload.Data.Routes[0].AnalysisRef != nil {
		t.Fatalf("缓存失败后的路线或引用错误: %+v", payload.Data.Routes)
	}
	if !containsLimitation(payload.Data.Limitations, "路线权威引用缓存失败") {
		t.Fatalf("缓存失败限制说明缺失: %v", payload.Data.Limitations)
	}
}

func TestMapAPIRouteAuthorityUnavailableKeepsRouteWithoutReference(t *testing.T) {
	route := projectionRoute("safe-route", 2, 0, false)
	route.Rank = 1
	safety := &routeSafetyStub{result: applicationevacuation.RouteSearchResult{
		Snapshot: hazard.Snapshot{ID: "snapshot-1"}, Routes: []evacuation.Route{route},
	}}
	response := serveJSON(t, newAuthorityMapHandler(t, safety, &routeAuthorityRecorderStub{}),
		http.MethodPost, "/routes", validRouteRequest)
	var payload struct {
		Data safeRouteResult `json:"data"`
	}
	decodeJSON(t, response.Body.String(), &payload)
	if response.Code != http.StatusOK || payload.Data.Routes[0].AnalysisRef != nil ||
		!containsLimitation(payload.Data.Limitations, "缓存未配置或快照已过期") {
		t.Fatalf("无路线权威缓存的降级错误: status=%d data=%+v", response.Code, payload.Data)
	}
}

func TestMapAPIRejectsUnknownRouteSafetyRuleVersion(t *testing.T) {
	route := projectionRoute("safe-route", 2, 0, true)
	route.Rank = 1
	safety := &routeSafetyStub{result: applicationevacuation.RouteSearchResult{
		Snapshot: hazard.Snapshot{ID: "snapshot-1"}, Routes: []evacuation.Route{route},
		RuleVersion: "route-rules-v2",
	}}
	response := serveJSON(t, newAuthorityMapHandler(t, safety, &routeAuthorityRecorderStub{}),
		http.MethodPost, "/routes", validRouteRequest)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"unsafe_provider_result"`) {
		t.Fatalf("未知路线规则版本未 fail-closed: status=%d body=%s",
			response.Code, response.Body.String())
	}
}

type routeAuthorityRecorderStub struct {
	reference   *report.AnalysisReference
	err         error
	ruleVersion string
	calls       int
}

func (s *routeAuthorityRecorderStub) RecordRoute(_ context.Context, _ hazard.Snapshot,
	_ evacuation.Route, ruleVersion string,
) (*report.AnalysisReference, error) {
	s.calls++
	s.ruleVersion = ruleVersion
	return s.reference, s.err
}

func newAuthorityMapHandler(t *testing.T, safety applicationevacuation.RouteSafetySearcher,
	recorder RouteAuthorityRecorder,
) http.Handler {
	t.Helper()
	handler, err := NewWithTransitSafetyAndAuthority(&facilitySearcherStub{}, &routePlannerStub{},
		nil, safety, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return middleware.RequestID(handler)
}

func decodeJSON(t *testing.T, value string, destination any) {
	t.Helper()
	if err := json.NewDecoder(strings.NewReader(value)).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func containsLimitation(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(value, wanted) {
			return true
		}
	}
	return false
}

const validRouteRequest = `{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"driving"}`
