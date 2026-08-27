package survival

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

func TestAssessmentServiceEvaluatesScenario(t *testing.T) {
	const id = "scenario-1"
	scenario := survivaldomain.Scenario{ID: id, AsOf: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
		ElapsedMinutes: 20, InputCompleteness: 0.9, Synthetic: true,
		Environment: map[string]string{"air_pocket": "yes"},
		Entrapment:  map[string]string{"communication": "yes"}}
	service, err := NewAssessmentService(scenarioReaderStub{values: map[string]survivaldomain.Scenario{id: scenario}}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Assess(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if value.ScenarioID != id || value.ModelVersion != survivaldomain.ModelVersion || value.Priority == "" {
		t.Fatalf("assessment = %+v", value)
	}
	if _, err = service.Assess(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing scenario error = %v", err)
	}
}

func TestNewAssessmentServiceRejectsMissingDependencies(t *testing.T) {
	if _, err := NewAssessmentService(nil, fixedClock{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil scenario reader error = %v", err)
	}
	if _, err := NewAssessmentService(scenarioReaderStub{}, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil clock error = %v", err)
	}
}

type scenarioReaderStub struct {
	values map[string]survivaldomain.Scenario
}

func (s scenarioReaderStub) GetScenario(_ context.Context, id string) (survivaldomain.Scenario, error) {
	value, ok := s.values[id]
	if !ok {
		return survivaldomain.Scenario{}, domain.ErrNotFound
	}
	return value, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC) }
