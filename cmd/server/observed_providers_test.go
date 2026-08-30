package main

import (
	"context"
	"errors"
	"testing"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

type observationRecorderStub struct{ values []ports.Observation }

func (s *observationRecorderStub) RecordObservation(value ports.Observation) {
	s.values = append(s.values, value)
}

type observedMapStub struct{ err error }

func (s observedMapStub) FindNearby(context.Context, spatial.Point, evacuation.FacilityType, int) ([]evacuation.Facility, error) {
	return nil, s.err
}

func (s observedMapStub) Plan(context.Context, spatial.Point, spatial.Point, evacuation.TravelMode) ([]evacuation.Route, error) {
	return nil, s.err
}

func (s observedMapStub) PlanTransit(context.Context, spatial.Point, spatial.Point, string, string) ([]evacuation.Route, error) {
	return nil, s.err
}

type evidenceSearcherObservationStub struct{ err error }

func (s evidenceSearcherObservationStub) Search(context.Context, string, int) ([]report.Evidence, error) {
	return nil, s.err
}

type narrativeGeneratorObservationStub struct{ err error }

func (s narrativeGeneratorObservationStub) Generate(context.Context, report.NarrativeInput) (report.Narrative, error) {
	return report.Narrative{}, s.err
}

type aiReporterObservationStub struct {
	search    ports.EvidenceSearcher
	generator ports.NarrativeGenerator
	result    applicationagent.Result
	err       error
}

func (s aiReporterObservationStub) Generate(ctx context.Context,
	_ applicationagent.Input,
) (applicationagent.Result, error) {
	if s.search != nil {
		_, _ = s.search.Search(ctx, "公开灾害", 1)
	}
	if s.generator != nil {
		_, _ = s.generator.Generate(ctx, report.NarrativeInput{})
	}
	return s.result, s.err
}

func TestObservedMapProviderRecordsBoundedOutcomes(t *testing.T) {
	recorder := &observationRecorderStub{}
	mapProvider := observedMapProvider{inner: observedMapStub{}, recorder: recorder}
	_, _ = mapProvider.FindNearby(context.Background(), spatial.Point{}, evacuation.FacilityHospital, 1)

	if len(recorder.values) != 1 {
		t.Fatalf("观测数量 = %d，期望 1", len(recorder.values))
	}
	assertObservation(t, recorder.values[0], componentAMap, ports.ObservationSuccess, "")
}

func TestObservedAIReporterRecordsBusinessOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		searchErr     error
		generatorErr  error
		result        applicationagent.Result
		resultErr     error
		searchOutcome ports.ObservationOutcome
		searchClass   string
		llmOutcome    ports.ObservationOutcome
		llmClass      string
	}{
		{
			name: "编排成功", result: applicationagent.Result{
				Evidence: []report.Evidence{{}}, EvidenceAvailable: true,
				Narrative: report.Narrative{Available: true}, NarrativeAvailable: true,
			},
			searchOutcome: ports.ObservationSuccess, llmOutcome: ports.ObservationSuccess,
		},
		{
			name: "供应商成功但业务降级", result: applicationagent.Result{
				Evidence: []report.Evidence{}, Narrative: report.Narrative{Available: false},
			},
			searchOutcome: ports.ObservationDegraded, searchClass: "evidence_unavailable",
			llmOutcome: ports.ObservationDegraded, llmClass: "narrative_unavailable",
		},
		{
			name: "供应商失败", searchErr: domain.ErrProviderUnavailable,
			generatorErr: context.DeadlineExceeded, result: applicationagent.Result{
				Evidence: []report.Evidence{}, Narrative: report.Narrative{Available: false},
			},
			searchOutcome: ports.ObservationFailure, searchClass: "provider_unavailable",
			llmOutcome: ports.ObservationFailure, llmClass: "timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &observationRecorderStub{}
			reporter := observedAIReporter{recorder: recorder, inner: aiReporterObservationStub{
				search: observedEvidenceSearcher{inner: evidenceSearcherObservationStub{err: test.searchErr}},
				generator: observedNarrativeGenerator{
					inner: narrativeGeneratorObservationStub{err: test.generatorErr},
				},
				result: test.result, err: test.resultErr,
			}}
			_, _ = reporter.Generate(context.Background(), applicationagent.Input{})
			if len(recorder.values) != 2 {
				t.Fatalf("观测数量 = %d，期望 2", len(recorder.values))
			}
			assertObservation(t, recorder.values[0], componentBocha, test.searchOutcome, test.searchClass)
			assertObservation(t, recorder.values[1], componentLLM, test.llmOutcome, test.llmClass)
		})
	}
}

func TestProviderErrorClassUsesStableAllowlist(t *testing.T) {
	values := []struct {
		err      error
		expected string
	}{
		{nil, ""},
		{context.Canceled, "canceled"},
		{domain.ErrInvalidInput, "unsafe_provider_result"},
		{errors.New("包含令牌 secret-value"), "operation_failed"},
	}
	for _, value := range values {
		if actual := providerErrorClass(value.err); actual != value.expected {
			t.Fatalf("providerErrorClass(%v) = %q，期望 %q", value.err, actual, value.expected)
		}
	}
}

func assertObservation(t *testing.T, value ports.Observation, componentID string,
	outcome ports.ObservationOutcome, errorClass string,
) {
	t.Helper()
	if value.ComponentID != componentID || value.Outcome != outcome || value.ErrorClass != errorClass {
		t.Fatalf("观测 = %#v，期望 component=%s outcome=%s error=%s", value, componentID, outcome, errorClass)
	}
	if value.ObservedAt.IsZero() || value.Duration < 0 {
		t.Fatalf("观测时间无效: %#v", value)
	}
}
