package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestOverviewUsesStoredDataWithoutRefresh(t *testing.T) {
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	risk := &riskStub{snapshot: hazard.Snapshot{
		RunAt: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Status: hazard.SnapshotAvailable,
		Source: source(now.Add(-time.Hour), now.Add(time.Hour)),
	}, zones: []hazard.RiskZone{{ID: "zone-1"}}}
	weather := &weatherStub{values: []hazard.WeatherSnapshot{{
		Location: spatial.Point{Longitude: 104.0665, Latitude: 30.5723},
		Source:   source(now.Add(-30*time.Minute), now.Add(90*time.Minute)),
	}}}
	service := newTestService(t, risk, weather, now)
	overview := service.Overview(context.Background())
	if risk.calls != 1 || weather.calls != 1 || overview.Summary.Available != 7 {
		t.Fatalf("overview=%+v riskCalls=%d weatherCalls=%d", overview, risk.calls, weather.calls)
	}
	if overview.Sources[0].State != StateAvailable || overview.Sources[1].State != StateAvailable {
		t.Fatalf("数据状态 = %+v", overview.Sources[:2])
	}
}

func TestOverviewMarksStaleAndWaiting(t *testing.T) {
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	risk := &riskStub{snapshot: hazard.Snapshot{
		RunAt: now.Add(-13 * time.Hour), ValidTo: now.Add(-time.Hour), Status: hazard.SnapshotAvailable,
		Source: source(now.Add(-13*time.Hour), now.Add(-time.Hour)),
	}}
	service := newTestService(t, risk, &weatherStub{err: domain.ErrNotFound}, now)
	overview := service.Overview(context.Background())
	if overview.Sources[0].State != StateStale || overview.Sources[1].State != StateWaiting ||
		overview.Summary.Attention != 2 {
		t.Fatalf("overview=%+v", overview)
	}
}

func TestNewRejectsMissingClock(t *testing.T) {
	if _, err := New(nil, nil, Capabilities{}, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("New() error=%v", err)
	}
}

func newTestService(t *testing.T, risk *riskStub, weather *weatherStub, now time.Time) *Service {
	t.Helper()
	service, err := New(risk, weather, Capabilities{
		Environment: "test", Version: "v0", Database: true, Cache: true, Refresh: true,
		Map: true, Search: true, LLM: true, LLMProvider: "测试 LLM", LLMModel: "test-model",
		WeatherPoints: []spatial.Point{{Longitude: 104.0665, Latitude: 30.5723}},
	}, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func source(fetchedAt, validTo time.Time) provenance.Provenance {
	return provenance.Provenance{Provider: "provider", Dataset: "dataset", SourceURI: "https://example.test/data",
		DataKind: provenance.DataKindNowcast, FetchedAt: fetchedAt, ValidTo: validTo}
}

type riskStub struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
	calls    int
}

func (s *riskStub) LatestRisk(context.Context, hazard.Type) (hazard.Snapshot, []hazard.RiskZone, error) {
	s.calls++
	return s.snapshot, s.zones, s.err
}

type weatherStub struct {
	values []hazard.WeatherSnapshot
	err    error
	calls  int
}

func (s *weatherStub) Latest(context.Context, []spatial.Point) ([]hazard.WeatherSnapshot, error) {
	s.calls++
	return s.values, s.err
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
