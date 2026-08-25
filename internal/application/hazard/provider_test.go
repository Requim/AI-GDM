package hazard

import (
	"context"
	"testing"
	"time"

	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestHazardProviderSerializesRefresh(t *testing.T) {
	refresher := &blockingRefresher{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	provider, err := NewHazardProvider(hazarddomain.TypeLandslide, refresher, &evaluatorStub{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	go refreshAndSignal(provider, done)
	waitSignal(t, refresher.entered, "首次刷新未进入底层刷新器")
	started := make(chan struct{})
	go func() {
		close(started)
		refreshAndSignal(provider, done)
	}()
	<-started
	assertNoSignal(t, refresher.entered, "并发刷新进入了同一底层刷新器")

	refresher.release <- struct{}{}
	waitSignal(t, refresher.entered, "第二次刷新未在锁释放后执行")
	refresher.release <- struct{}{}
	waitSignal(t, done, "首次刷新未结束")
	waitSignal(t, done, "第二次刷新未结束")
}

func TestRefreshFuncAdaptsCollector(t *testing.T) {
	want := testSnapshot("snapshot-1", hazarddomain.TypeLandslide)
	called := false
	refresh := RefreshFunc(func(context.Context) (
		hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
	) {
		called = true
		return want, []hazarddomain.RiskZone{}, nil
	})
	got, zones, err := refresh.Refresh(context.Background())
	if err != nil || !called || got.ID != want.ID || zones == nil {
		t.Fatalf("Refresh() = %+v, zones=%v, called=%v, error=%v", got, zones, called, err)
	}
}

func TestHazardProviderStopsWaitingWhenContextIsCanceled(t *testing.T) {
	refresher := &blockingRefresher{
		entered: make(chan struct{}, 1), release: make(chan struct{}, 1),
	}
	provider, err := NewHazardProvider(hazarddomain.TypeLandslide, refresher, &evaluatorStub{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	go refreshAndSignal(provider, done)
	waitSignal(t, refresher.entered, "首次刷新未进入底层刷新器")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = provider.Refresh(ctx); err == nil || err != nil && ctx.Err() == nil {
		t.Fatalf("Refresh(canceled) error = %v", err)
	}
	refresher.release <- struct{}{}
	waitSignal(t, done, "首次刷新未结束")
}

func TestHazardProviderDoesNotRefreshWithCanceledContext(t *testing.T) {
	refresher := &countingRefresher{}
	provider, err := NewHazardProvider(hazarddomain.TypeLandslide, refresher, &evaluatorStub{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = provider.Refresh(ctx); err == nil || refresher.calls != 0 {
		t.Fatalf("Refresh(canceled) error=%v calls=%d", err, refresher.calls)
	}
}

func refreshAndSignal(provider *HazardProvider, done chan<- struct{}) {
	_, _, _ = provider.Refresh(context.Background())
	done <- struct{}{}
}

func waitSignal(t *testing.T, values <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-values:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func assertNoSignal(t *testing.T, values <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-values:
		t.Fatal(message)
	case <-time.After(25 * time.Millisecond):
	}
}

type blockingRefresher struct {
	entered chan struct{}
	release chan struct{}
}

type countingRefresher struct{ calls int }

func (s *countingRefresher) Refresh(context.Context) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	s.calls++
	return hazarddomain.Snapshot{}, []hazarddomain.RiskZone{}, nil
}

func (s *blockingRefresher) Refresh(context.Context) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	s.entered <- struct{}{}
	<-s.release
	return hazarddomain.Snapshot{}, nil, nil
}
