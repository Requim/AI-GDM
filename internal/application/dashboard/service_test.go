package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestOverviewUsesStoredDataAndObservedBusinessSuccess(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	risk, weather := availableInputs(now)
	observations := &observationStub{statuses: successStatuses(now, "lhasa", "weather", "amap", "bocha", "llm")}
	service := newTestService(t, risk, weather, observations, now)
	overview := service.Overview(context.Background())
	if risk.calls != 1 || weather.calls != 1 || observations.calls != 1 {
		t.Fatalf("risk=%d weather=%d observations=%d", risk.calls, weather.calls, observations.calls)
	}
	if overview.Summary != (Summary{Available: 7, Attention: 1}) {
		t.Fatalf("summary=%+v", overview.Summary)
	}
	if overview.Sources[0].State != StateAvailable || overview.Sources[1].State != StateAvailable {
		t.Fatalf("数据状态=%+v", overview.Sources[:2])
	}
}

func TestOverviewKeepsValidStoredDataWhileOnDemandComponentsWaitForObservation(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	risk, weather := availableInputs(now)
	service := newTestService(t, risk, weather, &observationStub{}, now)
	overview := service.Overview(context.Background())
	for _, index := range []int{0, 1} {
		if overview.Sources[index].State != StateAvailable {
			t.Fatalf("source[%d]=%+v", index, overview.Sources[index])
		}
	}
	for _, index := range []int{2, 3, 4} {
		if overview.Sources[index].State != StateWaiting {
			t.Fatalf("source[%d]=%+v", index, overview.Sources[index])
		}
	}
	if overview.Summary != (Summary{Available: 4, Attention: 4}) {
		t.Fatalf("summary=%+v", overview.Summary)
	}
}

func TestOverviewShowsDegradedAndFailedObservationsWithoutDroppingStoredData(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	risk, weather := availableInputs(now)
	observations := &observationStub{statuses: []ports.ComponentStatus{
		componentStatus("lhasa", ports.ObservationDegraded, now, 0),
		componentStatus("weather", ports.ObservationFailure, now, 2),
		componentStatus("amap", ports.ObservationFailure, now, 3),
	}}
	overview := newTestService(t, risk, weather, observations, now).Overview(context.Background())
	if overview.Sources[0].State != StateDegraded || overview.Sources[1].State != StateDegraded {
		t.Fatalf("stored=%+v", overview.Sources[:2])
	}
	if overview.Sources[2].State != StateUnavailable || !strings.Contains(overview.Sources[2].Detail, "连续 3 次") {
		t.Fatalf("amap=%+v", overview.Sources[2])
	}
	if overview.Sources[1].UpdatedAt.IsZero() || overview.Sources[1].LastAttemptAt.IsZero() {
		t.Fatalf("weather=%+v", overview.Sources[1])
	}
}

func TestOverviewKeepsStalePriorityOverObservedFailure(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	risk := &riskStub{snapshot: hazard.Snapshot{
		RunAt: now.Add(-13 * time.Hour), ValidTo: now.Add(-time.Hour), Status: hazard.SnapshotAvailable,
		Source: source(now.Add(-13*time.Hour), now.Add(-time.Hour)),
	}}
	weather := &weatherStub{values: []hazard.WeatherSnapshot{{
		Location: spatial.Point{Longitude: 104.0665, Latitude: 30.5723},
		Source:   source(now.Add(-13*time.Hour), now.Add(-time.Hour)),
	}}}
	observations := &observationStub{statuses: []ports.ComponentStatus{
		componentStatus("lhasa", ports.ObservationFailure, now, 2),
		componentStatus("weather", ports.ObservationDegraded, now, 0),
	}}
	overview := newTestService(t, risk, weather, observations, now).Overview(context.Background())
	if overview.Sources[0].State != StateStale || overview.Sources[1].State != StateStale {
		t.Fatalf("sources=%+v", overview.Sources[:2])
	}
	if !strings.Contains(overview.Sources[0].Detail, "已超过有效期") ||
		!strings.Contains(overview.Sources[0].Detail, "最近业务调用失败") {
		t.Fatalf("risk=%+v", overview.Sources[0])
	}
}

func TestOverviewFailsClosedWhenObservedCollectionHasNoStoredData(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	observations := &observationStub{statuses: []ports.ComponentStatus{
		componentStatus("lhasa", ports.ObservationSuccess, now, 0),
		componentStatus("weather", ports.ObservationFailure, now, 1),
	}}
	service := newTestService(t, &riskStub{err: domain.ErrNotFound},
		&weatherStub{err: domain.ErrNotFound}, observations, now)
	overview := service.Overview(context.Background())
	if overview.Sources[0].State != StateWaiting || overview.Sources[1].State != StateUnavailable {
		t.Fatalf("sources=%+v", overview.Sources[:2])
	}
}

func TestOverviewIncludesUnknownVerifiedLiveEventSource(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	risk, weather := availableInputs(now)
	overview := newTestService(t, risk, weather, &observationStub{}, now).Overview(context.Background())
	live := overview.Sources[5]
	if live.ID != "live-events" || live.State != StateUnknown ||
		live.Detail != "未接入经核验的实时事件源，无法判断当前是否存在实时事件" {
		t.Fatalf("live=%+v", live)
	}
	for _, forbidden := range []string{"没有事件", "没有灾害"} {
		if strings.Contains(live.Detail, forbidden) {
			t.Fatalf("实时事件状态不得推断 %q", forbidden)
		}
	}
}

func TestSummarizeTreatsNonAvailableOperationalStatesAsAttention(t *testing.T) {
	sources := []SourceStatus{{State: StateAvailable}, {State: StateDegraded}, {State: StateStale},
		{State: StateWaiting}, {State: StateConfigured}, {State: StateUnknown},
		{State: StateDisabled}, {State: StateUnavailable}}
	if got := summarize(sources); got != (Summary{Available: 1, Attention: 5, Unavailable: 2}) {
		t.Fatalf("summary=%+v", got)
	}
}

func TestNewRejectsMissingClock(t *testing.T) {
	if _, err := New(nil, nil, Capabilities{}, nil, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("New() error=%v", err)
	}
}

func availableInputs(now time.Time) (*riskStub, *weatherStub) {
	risk := &riskStub{snapshot: hazard.Snapshot{
		RunAt: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Status: hazard.SnapshotAvailable,
		Source: source(now.Add(-time.Hour), now.Add(time.Hour)),
	}, zones: []hazard.RiskZone{{ID: "zone-1"}}}
	weather := &weatherStub{values: []hazard.WeatherSnapshot{{
		Location: spatial.Point{Longitude: 104.0665, Latitude: 30.5723},
		Source:   source(now.Add(-30*time.Minute), now.Add(90*time.Minute)),
	}}}
	return risk, weather
}

func newTestService(t *testing.T, risk *riskStub, weather *weatherStub,
	observations ports.ComponentStatusReader, now time.Time,
) *Service {
	t.Helper()
	service, err := New(risk, weather, Capabilities{
		Environment: "test", Version: "v0", Database: true, Cache: true, Refresh: true,
		Map: true, Search: true, LLM: true, LLMProvider: "测试 LLM", LLMModel: "test-model",
		WeatherPoints: []spatial.Point{{Longitude: 104.0665, Latitude: 30.5723}},
	}, observations, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func successStatuses(now time.Time, ids ...string) []ports.ComponentStatus {
	result := make([]ports.ComponentStatus, 0, len(ids))
	for _, id := range ids {
		result = append(result, componentStatus(id, ports.ObservationSuccess, now, 0))
	}
	return result
}

func componentStatus(id string, outcome ports.ObservationOutcome, now time.Time,
	failures uint64,
) ports.ComponentStatus {
	status := ports.ComponentStatus{ComponentID: id, LastAttemptAt: now.Add(-time.Minute),
		LastOutcome: outcome, ConsecutiveFailures: failures}
	if outcome == ports.ObservationSuccess {
		status.LastSuccessAt = status.LastAttemptAt
	}
	return status
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

type observationStub struct {
	statuses []ports.ComponentStatus
	calls    int
}

func (s *observationStub) Snapshot() []ports.ComponentStatus {
	s.calls++
	return append([]ports.ComponentStatus(nil), s.statuses...)
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
