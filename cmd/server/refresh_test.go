package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/platform/config"
)

func TestRefreshGroupRunsSchedulersIndependently(t *testing.T) {
	sentinel := errors.New("weather stopped")
	weatherStarted, lhasaStarted := make(chan struct{}), make(chan struct{})
	releaseWeather, lhasaStopped := make(chan struct{}), make(chan struct{})
	group := &refreshGroup{services: []namedRefreshService{
		{name: "weather", service: serviceFunc(func(context.Context) error {
			close(weatherStarted)
			<-releaseWeather
			return sentinel
		})},
		{name: "lhasa", service: serviceFunc(func(ctx context.Context) error {
			close(lhasaStarted)
			<-ctx.Done()
			close(lhasaStopped)
			return nil
		})},
	}}
	done := make(chan error, 1)
	go func() { done <- group.Run(context.Background()) }()
	waitServiceSignal(t, weatherStarted)
	waitServiceSignal(t, lhasaStarted)
	close(releaseWeather)
	if err := receiveRefreshError(t, done); !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v", err)
	}
	waitServiceSignal(t, lhasaStopped)
}

func TestRefreshGroupRejectsInvalidServices(t *testing.T) {
	values := []*refreshGroup{
		{},
		{services: []namedRefreshService{{name: "lhasa"}}},
	}
	for _, group := range values {
		if err := group.Run(context.Background()); !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("Run() error = %v", err)
		}
	}
}

func TestNewRefreshServiceDisabled(t *testing.T) {
	service, err := newRefreshService(config.Config{}, nil, nil, nil)
	if err != nil || service != nil {
		t.Fatalf("newRefreshService() = %v, error=%v", service, err)
	}
}

func TestLHASARefreshTaskAcceptsFallbackAndReturnsFailures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fallback := hazard.Snapshot{ID: "fallback", Source: provenance.Provenance{
		QualityFlags: []string{weatherFallbackFlag},
	}}
	refresher := &lhasaRefresherStub{snapshot: fallback}
	if err := lhasaRefreshTask(refresher, logger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refresher.calls != 1 {
		t.Fatalf("Refresh() calls = %d", refresher.calls)
	}
	sentinel := errors.New("lhasa unavailable")
	refresher.err = sentinel
	if err := lhasaRefreshTask(refresher, logger).Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v", err)
	}
}

type lhasaRefresherStub struct {
	snapshot hazard.Snapshot
	err      error
	calls    int
}

func (s *lhasaRefresherStub) Refresh(context.Context) (hazard.Snapshot, []hazard.RiskZone, error) {
	s.calls++
	return s.snapshot, nil, s.err
}

func receiveRefreshError(t *testing.T, values <-chan error) error {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("等待刷新服务组停止超时")
		return nil
	}
}
