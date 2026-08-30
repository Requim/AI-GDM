package main

import (
	"context"
	"errors"
	"sync"
	"time"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	componentLHASA   = "lhasa"
	componentWeather = "weather"
	componentAMap    = "amap"
	componentBocha   = "bocha"
	componentLLM     = "llm"
)

type mapProvider interface {
	ports.PlaceFinder
	ports.RoutePlanner
	ports.TransitRoutePlanner
}

type observedMapProvider struct {
	inner    mapProvider
	recorder ports.ObservationRecorder
}

func (p observedMapProvider) FindNearby(ctx context.Context, center spatial.Point,
	kind evacuation.FacilityType, radiusM int,
) ([]evacuation.Facility, error) {
	started := time.Now()
	values, err := p.inner.FindNearby(ctx, center, kind, radiusM)
	recordProviderObservation(p.recorder, componentAMap, started, err)
	return values, err
}

func (p observedMapProvider) Plan(ctx context.Context, origin, destination spatial.Point,
	mode evacuation.TravelMode,
) ([]evacuation.Route, error) {
	started := time.Now()
	values, err := p.inner.Plan(ctx, origin, destination, mode)
	recordProviderObservation(p.recorder, componentAMap, started, err)
	return values, err
}

func (p observedMapProvider) PlanTransit(ctx context.Context, origin, destination spatial.Point,
	city1, city2 string,
) ([]evacuation.Route, error) {
	started := time.Now()
	values, err := p.inner.PlanTransit(ctx, origin, destination, city1, city2)
	recordProviderObservation(p.recorder, componentAMap, started, err)
	return values, err
}

type observedEvidenceSearcher struct {
	inner ports.EvidenceSearcher
}

func (s observedEvidenceSearcher) Search(ctx context.Context, query string, limit int) ([]report.Evidence, error) {
	started := time.Now()
	values, err := s.inner.Search(ctx, query, limit)
	if trace := aiTraceFromContext(ctx); trace != nil {
		trace.recordSearch(time.Since(started), err)
	}
	return values, err
}

type observedNarrativeGenerator struct {
	inner ports.NarrativeGenerator
}

func (g observedNarrativeGenerator) Generate(ctx context.Context,
	input report.NarrativeInput,
) (report.Narrative, error) {
	started := time.Now()
	value, err := g.inner.Generate(ctx, input)
	if trace := aiTraceFromContext(ctx); trace != nil {
		trace.recordNarrative(time.Since(started), err)
	}
	return value, err
}

type aiReporter interface {
	Generate(context.Context, applicationagent.Input) (applicationagent.Result, error)
}

type observedAIReporter struct {
	inner    aiReporter
	recorder ports.ObservationRecorder
}

func (r observedAIReporter) Generate(ctx context.Context,
	input applicationagent.Input,
) (applicationagent.Result, error) {
	trace := &aiObservationTrace{}
	result, err := r.inner.Generate(context.WithValue(ctx, aiObservationTraceKey{}, trace), input)
	search, narrative := trace.snapshot()
	recordAIObservations(r.recorder, search, narrative, result, err)
	return result, err
}

type aiObservationTraceKey struct{}

type aiProviderCall struct {
	called   bool
	duration time.Duration
	err      error
}

type aiObservationTrace struct {
	mu        sync.Mutex
	search    aiProviderCall
	narrative aiProviderCall
}

func (t *aiObservationTrace) recordSearch(duration time.Duration, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.search = aiProviderCall{called: true, duration: duration, err: err}
}

func (t *aiObservationTrace) recordNarrative(duration time.Duration, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.narrative = aiProviderCall{called: true, duration: duration, err: err}
}

func (t *aiObservationTrace) snapshot() (aiProviderCall, aiProviderCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.search, t.narrative
}

func aiTraceFromContext(ctx context.Context) *aiObservationTrace {
	trace, _ := ctx.Value(aiObservationTraceKey{}).(*aiObservationTrace)
	return trace
}

func recordAIObservations(recorder ports.ObservationRecorder, search, narrative aiProviderCall,
	result applicationagent.Result, resultErr error,
) {
	if search.called {
		outcome, class := searchObservationOutcome(search.err, result, resultErr)
		recordObservation(recorder, componentBocha, search.duration, outcome, class)
	}
	if narrative.called {
		outcome, class := narrativeObservationOutcome(narrative.err, result, resultErr)
		recordObservation(recorder, componentLLM, narrative.duration, outcome, class)
	}
}

func searchObservationOutcome(providerErr error, result applicationagent.Result,
	resultErr error,
) (ports.ObservationOutcome, string) {
	if providerErr != nil {
		return ports.ObservationFailure, providerErrorClass(providerErr)
	}
	if resultErr != nil {
		return ports.ObservationDegraded, "report_unpublished"
	}
	if result.EvidenceAvailable && len(result.Evidence) > 0 {
		return ports.ObservationSuccess, ""
	}
	return ports.ObservationDegraded, "evidence_unavailable"
}

func narrativeObservationOutcome(providerErr error, result applicationagent.Result,
	resultErr error,
) (ports.ObservationOutcome, string) {
	if providerErr != nil {
		return ports.ObservationFailure, providerErrorClass(providerErr)
	}
	if resultErr != nil {
		return ports.ObservationDegraded, "report_unpublished"
	}
	if result.NarrativeAvailable && result.Narrative.Available {
		return ports.ObservationSuccess, ""
	}
	return ports.ObservationDegraded, "narrative_unavailable"
}

func recordProviderObservation(recorder ports.ObservationRecorder, componentID string,
	started time.Time, err error,
) {
	outcome := ports.ObservationSuccess
	if err != nil {
		outcome = ports.ObservationFailure
	}
	recordObservation(recorder, componentID, time.Since(started), outcome, providerErrorClass(err))
}

func recordObservation(recorder ports.ObservationRecorder, componentID string,
	duration time.Duration, outcome ports.ObservationOutcome, errorClass string,
) {
	if recorder == nil {
		return
	}
	recorder.RecordObservation(ports.Observation{
		ComponentID: componentID, Outcome: outcome, ObservedAt: time.Now().UTC(),
		Duration: duration, ErrorClass: errorClass,
	})
}

func providerErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, domain.ErrInvalidInput):
		return "unsafe_provider_result"
	default:
		return "operation_failed"
	}
}
