package hazard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
)

func TestRegistryRegistersAndSortsHazardTypes(t *testing.T) {
	landslide := testProvider(t, hazarddomain.TypeLandslide)
	flood := testProvider(t, hazarddomain.TypeFlood)
	registry, err := NewRegistry(flood, landslide)
	if err != nil {
		t.Fatal(err)
	}
	values := registry.Types()
	if len(values) != 2 || values[0] != hazarddomain.TypeFlood || values[1] != hazarddomain.TypeLandslide {
		t.Fatalf("Types() = %v", values)
	}
	got, err := registry.Resolve(hazarddomain.TypeLandslide)
	if err != nil || got != landslide {
		t.Fatalf("Resolve() = %p, error=%v", got, err)
	}
}

func TestRegistryRejectsDuplicateAndUnknownType(t *testing.T) {
	provider := testProvider(t, hazarddomain.TypeLandslide)
	registry, err := NewRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.Register(provider); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err = registry.Resolve(hazarddomain.TypeFlood); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err = registry.Resolve(hazarddomain.TypeFlood); !errors.Is(err, ErrHazardNotSupported) {
		t.Fatalf("Resolve() missing capability error = %v", err)
	}
}

func TestRegistrySupportsConcurrentRegistrationAndLookup(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			hazardType := hazarddomain.Type(fmt.Sprintf("hazard_%02d", index))
			provider, providerErr := NewHazardProvider(hazardType, &refresherStub{}, &evaluatorStub{})
			if providerErr != nil {
				t.Errorf("NewHazardProvider(%s) error = %v", hazardType, providerErr)
				return
			}
			if registerErr := registry.Register(provider); registerErr != nil {
				t.Errorf("Register(%s) error = %v", hazardType, registerErr)
				return
			}
			if _, resolveErr := registry.Resolve(hazardType); resolveErr != nil {
				t.Errorf("Resolve(%s) error = %v", hazardType, resolveErr)
			}
		}(index)
	}
	group.Wait()
	if got := len(registry.Types()); got != 20 {
		t.Fatalf("Types() count = %d", got)
	}
}

func TestRegistryZeroValueCanRegister(t *testing.T) {
	registry := &Registry{}
	provider := testProvider(t, hazarddomain.TypeLandslide)
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.Resolve(hazarddomain.TypeLandslide); err != nil || got != provider {
		t.Fatalf("Resolve() = %p, error=%v", got, err)
	}
}

func TestNewHazardProviderValidatesDependenciesAndType(t *testing.T) {
	refresh := RefreshFunc(func(context.Context) (
		hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
	) {
		return hazarddomain.Snapshot{}, nil, nil
	})
	evaluator := &evaluatorStub{}
	checks := []struct {
		name string
		err  error
	}{
		{name: "空灾种", err: providerError("", refresh, evaluator)},
		{name: "非法灾种", err: providerError("Land Slide", refresh, evaluator)},
		{name: "空刷新器", err: providerError(hazarddomain.TypeLandslide, nil, evaluator)},
		{name: "类型化空刷新器", err: providerError(
			hazarddomain.TypeLandslide, RefreshFunc(nil), evaluator)},
		{name: "空研判器", err: providerError(hazarddomain.TypeLandslide, refresh, nil)},
	}
	for _, check := range checks {
		if !errors.Is(check.err, domain.ErrInvalidInput) {
			t.Errorf("%s error = %v", check.name, check.err)
		}
	}
}

func providerError(hazardType hazarddomain.Type, refresher interface {
	Refresh(context.Context) (
		hazarddomain.Snapshot, []hazarddomain.RiskZone, error)
}, evaluator interface {
	Evaluate(input risk.Input) (risk.Assessment, error)
}) error {
	_, err := NewHazardProvider(hazardType, refresher, evaluator)
	return err
}

func testProvider(t *testing.T, hazardType hazarddomain.Type) *HazardProvider {
	t.Helper()
	provider, err := NewHazardProvider(hazardType, &refresherStub{}, &evaluatorStub{})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
