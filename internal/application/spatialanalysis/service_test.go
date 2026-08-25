package spatialanalysis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestServiceAnalyze(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	executor := &executorStub{result: validAnalysis(t, "snapshot-1", now)}
	service, err := New(executor, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Analyze(context.Background(), "snapshot-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || executor.snapshotID != "snapshot-1" || !executor.calculatedAt.Equal(now) {
		t.Fatalf("Analyze() = %+v, executor=%+v", got, executor)
	}
}

func TestServiceRejectsInvalidInputOrResult(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		snapshotID string
		clock      fixedClock
		result     spatialdomain.Analysis
	}{
		{name: "空标识", snapshotID: "", clock: fixedClock{now: now}},
		{name: "标识含空白", snapshotID: " snapshot-1", clock: fixedClock{now: now}},
		{name: "非UTC时间", snapshotID: "snapshot-1", clock: fixedClock{now: now.In(time.FixedZone("CST", 8*60*60))}},
		{name: "返回其他快照", snapshotID: "snapshot-1", clock: fixedClock{now: now},
			result: validAnalysis(t, "snapshot-2", now)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(&executorStub{result: test.result}, test.clock)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Analyze(context.Background(), test.snapshotID); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Analyze() error = %v", err)
			}
		})
	}
}

func TestServicePreservesExecutorError(t *testing.T) {
	want := errors.Join(domain.ErrInsufficientData, errors.New("风险区尚未保存完整"))
	service, err := New(&executorStub{err: want}, fixedClock{
		now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Analyze(context.Background(), "snapshot-1"); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("Analyze() error = %v", err)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := New(nil, fixedClock{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("New() error = %v", err)
	}
}

func validAnalysis(t *testing.T, snapshotID string, now time.Time) spatialdomain.Analysis {
	t.Helper()
	value, err := spatialdomain.NewAnalysis(spatialdomain.AnalysisInput{
		SnapshotID: snapshotID,
		Area: spatialdomain.AreaCalculation{
			Method: spatialdomain.AreaMethod, InputReferences: []string{"snapshot:" + snapshotID},
		},
		CalculatedAt: now,
		Limitations:  []string{"完整快照没有风险区，空间分析已完成"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type executorStub struct {
	result       spatialdomain.Analysis
	err          error
	snapshotID   string
	calculatedAt time.Time
}

func (s *executorStub) Execute(_ context.Context, snapshotID string,
	calculatedAt time.Time,
) (spatialdomain.Analysis, error) {
	s.snapshotID, s.calculatedAt = snapshotID, calculatedAt
	return s.result, s.err
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
