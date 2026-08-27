package mapapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestMapAPIForwardsTransitCities(t *testing.T) {
	transit := &transitPlannerStub{result: []evacuation.Route{{ID: "transit-1"}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := NewWithTransit(&facilitySearcherStub{}, &routePlannerStub{}, transit, logger)
	if err != nil {
		t.Fatal(err)
	}
	handler = middleware.RequestID(handler)
	response := serveJSON(t, handler, http.MethodPost, "/routes",
		`{"origin":{"longitude":116.4,"latitude":39.9},"destination":{"longitude":116.5,"latitude":39.8},"mode":"transit","originCity":"010","destinationCity":"021"}`)
	if response.Code != http.StatusOK || transit.city1 != "010" || transit.city2 != "021" || transit.calls != 1 {
		t.Fatalf("公交代理错误: status=%d body=%s stub=%+v", response.Code, response.Body.String(), transit)
	}
}

type transitPlannerStub struct {
	result []evacuation.Route
	err    error
	city1  string
	city2  string
	calls  int
}

func (s *transitPlannerStub) PlanTransit(_ context.Context, _, _ spatial.Point, city1, city2 string) ([]evacuation.Route, error) {
	s.calls++
	s.city1, s.city2 = city1, city2
	return s.result, s.err
}

var _ applicationevacuation.FacilitySearcher = (*facilitySearcherStub)(nil)
