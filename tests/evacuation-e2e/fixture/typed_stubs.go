package main

import (
	"context"
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

func (s *scenarioStore) FindNearby(ctx context.Context, _ spatial.Point,
	kind evacuation.FacilityType, _ int,
) ([]evacuation.Facility, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return typedFacilities(s.current(), kind), nil
}

func (s *scenarioStore) Plan(ctx context.Context, origin, destination spatial.Point,
	mode evacuation.TravelMode,
) ([]evacuation.Route, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return typedRoutes(s.current(), origin, destination, mode), nil
}

func (s *scenarioStore) PlanTransit(ctx context.Context, origin, destination spatial.Point,
	originCity, destinationCity string,
) ([]evacuation.Route, error) {
	if s.current() == "transit_citycodes" && (originCity != "028" || destinationCity != "021") {
		return nil, fmt.Errorf("%w: 公交 citycode 未完整透传", domain.ErrInvalidInput)
	}
	return s.Plan(ctx, origin, destination, evacuation.TravelTransit)
}

func (s *scenarioStore) LatestRisk(ctx context.Context, hazardType hazard.Type) (
	hazard.Snapshot, []hazard.RiskZone, error,
) {
	if err := ctx.Err(); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	name := s.current()
	return typedSnapshot(name, hazardType), typedRiskZones(name), nil
}

var _ ports.PlaceFinder = (*scenarioStore)(nil)
var _ ports.RoutePlanner = (*scenarioStore)(nil)
var _ ports.TransitRoutePlanner = (*scenarioStore)(nil)
var _ ports.LatestRiskReader = (*scenarioStore)(nil)
