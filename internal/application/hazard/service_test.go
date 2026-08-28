package hazard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestServiceLatestBuildsDeterministicAssessment(t *testing.T) {
	now := testNow()
	snapshot := testSnapshot("snapshot-latest", hazarddomain.TypeLandslide)
	reader := &riskReaderStub{snapshot: snapshot}
	evaluator := &evaluatorStub{}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, evaluator, fixedClock{now: now})

	got, err := service.Latest(context.Background(), hazarddomain.TypeLandslide)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.ID != snapshot.ID || got.Assessment.SnapshotID != snapshot.ID {
		t.Fatalf("Latest() = %+v", got)
	}
	if reader.latestType != hazarddomain.TypeLandslide || !evaluator.input.EvaluatedAt.Equal(now) {
		t.Fatalf("reader type=%s, evaluator input=%+v", reader.latestType, evaluator.input)
	}
	if got.Zones == nil {
		t.Fatal("Latest() 必须把空风险区编码为 JSON 数组")
	}
}

func TestServiceLatestMapAppliesReadLimitBeforeAssessment(t *testing.T) {
	now := testNow()
	reader := &riskReaderStub{
		snapshot: testSnapshot("snapshot-map", hazarddomain.TypeLandslide),
		zones:    []hazarddomain.RiskZone{},
		mapTotal: 0,
	}
	evaluator := &evaluatorStub{}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, evaluator, fixedClock{now: now})

	got, err := service.LatestMap(context.Background(), hazarddomain.TypeLandslide, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if reader.mapMaxZones != 100000 || got.TotalZoneCount != 0 ||
		got.Assessment.SnapshotID != "snapshot-map" {
		t.Fatalf("LatestMap() = %+v, reader=%+v", got, reader)
	}
}

func TestServiceLatestMapRejectsIncompleteBoundedRead(t *testing.T) {
	reader := &riskReaderStub{snapshot: testSnapshot("snapshot-map", hazarddomain.TypeLandslide),
		mapTotal: 100001}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, &evaluatorStub{}, fixedClock{now: testNow()})
	if _, err := service.LatestMap(context.Background(), hazarddomain.TypeLandslide, 100000); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("LatestMap() error = %v", err)
	}
}

func TestServiceGetValidatesIdentity(t *testing.T) {
	reader := &riskReaderStub{snapshot: testSnapshot("snapshot-1", hazarddomain.TypeLandslide)}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, &evaluatorStub{}, fixedClock{now: testNow()})

	got, err := service.Get(context.Background(), hazarddomain.TypeLandslide, "snapshot-1")
	if err != nil || got.Snapshot.ID != "snapshot-1" || reader.detailID != "snapshot-1" {
		t.Fatalf("Get() = %+v, detailID=%q, error=%v", got, reader.detailID, err)
	}
	if _, err = service.Get(context.Background(), hazarddomain.TypeLandslide, " snapshot-1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Get(invalid) error = %v", err)
	}
	reader.snapshot.HazardType = hazarddomain.TypeFlood
	if _, err = service.Get(context.Background(), hazarddomain.TypeLandslide, "snapshot-1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Get(mismatch) error = %v", err)
	}
	reader.snapshot = testSnapshot("snapshot-other", hazarddomain.TypeLandslide)
	if _, err = service.Get(context.Background(), hazarddomain.TypeLandslide, "snapshot-1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Get(snapshot mismatch) error = %v", err)
	}
}

func TestServiceRefreshUsesRegisteredProvider(t *testing.T) {
	snapshot := testSnapshot("snapshot-refreshed", hazarddomain.TypeLandslide)
	refresher := &refresherStub{snapshot: snapshot, zones: []hazarddomain.RiskZone{}}
	reader := &riskReaderStub{}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		refresher, &evaluatorStub{}, fixedClock{now: testNow()})

	got, err := service.Refresh(context.Background(), hazarddomain.TypeLandslide)
	if err != nil || got.Snapshot.ID != snapshot.ID || refresher.calls != 1 {
		t.Fatalf("Refresh() = %+v, calls=%d, error=%v", got, refresher.calls, err)
	}
}

func TestServicePreservesDependencyErrors(t *testing.T) {
	sentinel := errors.Join(domain.ErrProviderUnavailable, errors.New("upstream offline"))
	reader := &riskReaderStub{err: sentinel}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{err: sentinel}, &evaluatorStub{}, fixedClock{now: testNow()})

	if _, err := service.Latest(context.Background(), hazarddomain.TypeLandslide); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Latest() error = %v", err)
	}
	if _, err := service.Get(context.Background(), hazarddomain.TypeLandslide, "snapshot-1"); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := service.Refresh(context.Background(), hazarddomain.TypeLandslide); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Refresh() error = %v", err)
	}
}

func TestServicePreservesEvaluatorError(t *testing.T) {
	sentinel := errors.Join(domain.ErrInsufficientData, errors.New("snapshot expired"))
	reader := &riskReaderStub{snapshot: testSnapshot("snapshot-1", hazarddomain.TypeLandslide)}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, &evaluatorStub{err: sentinel}, fixedClock{now: testNow()})
	if _, err := service.Latest(context.Background(), hazarddomain.TypeLandslide); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestServiceRejectsInvalidClockOrAssessment(t *testing.T) {
	snapshot := testSnapshot("snapshot-1", hazarddomain.TypeLandslide)
	reader := &riskReaderStub{snapshot: snapshot}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, &evaluatorStub{}, fixedClock{now: testNow().In(time.FixedZone("CST", 8*60*60))})
	if _, err := service.Latest(context.Background(), hazarddomain.TypeLandslide); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Latest(non-UTC) error = %v", err)
	}

	evaluator := &evaluatorStub{mutate: func(value *risk.Assessment) { value.SnapshotID = "other" }}
	service = testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, evaluator, fixedClock{now: testNow()})
	if _, err := service.Latest(context.Background(), hazarddomain.TypeLandslide); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Latest(mismatch) error = %v", err)
	}
}

func TestServiceRejectsUnregisteredHazardBeforeReading(t *testing.T) {
	reader := &riskReaderStub{}
	service := testService(t, reader, reader, hazarddomain.TypeLandslide,
		&refresherStub{}, &evaluatorStub{}, fixedClock{now: testNow()})
	if _, err := service.Latest(context.Background(), hazarddomain.TypeFlood); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Latest() error = %v", err)
	}
	if _, err := service.Latest(context.Background(), hazarddomain.TypeFlood); !errors.Is(err, ErrHazardNotSupported) {
		t.Fatalf("Latest() unsupported error = %v", err)
	}
	if reader.latestCalls != 0 {
		t.Fatalf("未注册灾种仍访问仓储，calls=%d", reader.latestCalls)
	}
}

func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewService(nil, &riskReaderStub{}, &riskReaderStub{}, registry, fixedClock{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err = NewService(&riskReaderStub{}, nil, &riskReaderStub{}, registry, fixedClock{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("NewService(map nil) error = %v", err)
	}
}

func testService(t *testing.T, latest *riskReaderStub, detail *riskReaderStub,
	hazardType hazarddomain.Type, refresher interface {
		Refresh(context.Context) (
			hazarddomain.Snapshot, []hazarddomain.RiskZone, error)
	}, evaluator *evaluatorStub,
	clock fixedClock,
) *Service {
	t.Helper()
	provider, err := NewHazardProvider(hazardType, refresher, evaluator)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(latest, latest, detail, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testNow() time.Time {
	return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
}

func testSnapshot(id string, hazardType hazarddomain.Type) hazarddomain.Snapshot {
	return hazarddomain.Snapshot{ID: id, HazardType: hazardType}
}

type riskReaderStub struct {
	snapshot    hazarddomain.Snapshot
	zones       []hazarddomain.RiskZone
	err         error
	latestType  hazarddomain.Type
	detailID    string
	latestCalls int
	mapTotal    int
	mapMaxZones int
}

func (s *riskReaderStub) LatestMapRisk(_ context.Context, value hazarddomain.Type,
	maxZones int,
) (ports.MapRiskRead, error) {
	s.latestCalls++
	s.latestType = value
	s.mapMaxZones = maxZones
	return ports.MapRiskRead{Snapshot: s.snapshot, Zones: s.zones,
		TotalZoneCount: s.mapTotal}, s.err
}

func (s *riskReaderStub) LatestRisk(_ context.Context, value hazarddomain.Type) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	s.latestCalls++
	s.latestType = value
	return s.snapshot, s.zones, s.err
}

func (s *riskReaderStub) RiskDetail(_ context.Context, id string) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	s.detailID = id
	return s.snapshot, s.zones, s.err
}

type refresherStub struct {
	snapshot hazarddomain.Snapshot
	zones    []hazarddomain.RiskZone
	err      error
	calls    int
}

func (s *refresherStub) Refresh(context.Context) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	s.calls++
	return s.snapshot, s.zones, s.err
}

type evaluatorStub struct {
	input  risk.Input
	err    error
	mutate func(*risk.Assessment)
}

func (s *evaluatorStub) Evaluate(input risk.Input) (risk.Assessment, error) {
	s.input = input
	if s.err != nil {
		return risk.Assessment{}, s.err
	}
	value := risk.Assessment{
		ID: "risk-test", HazardType: input.Snapshot.HazardType, SnapshotID: input.Snapshot.ID,
		Status: risk.AssessmentAvailable, DataStatus: risk.DataCurrent,
		ContextStatus: risk.ContextAbsent, RuleVersion: risk.RuleVersion,
		Confidence: risk.Confidence{Level: risk.ConfidenceHigh}, EvaluatedAt: input.EvaluatedAt,
	}
	if s.mutate != nil {
		s.mutate(&value)
	}
	return value, nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
