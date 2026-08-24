package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunServicesStopsServerWhenRefreshFails(t *testing.T) {
	sentinel := errors.New("refresh failed")
	serverStopped := make(chan struct{})
	server := serviceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		close(serverStopped)
		return nil
	})
	refresh := serviceFunc(func(context.Context) error { return sentinel })

	err := runServices(context.Background(), server, refresh)
	if !errors.Is(err, sentinel) {
		t.Fatalf("runServices() error = %v", err)
	}
	waitServiceSignal(t, serverStopped)
}

func TestRunServicesStopsRefreshWhenServerFails(t *testing.T) {
	sentinel := errors.New("listen failed")
	refreshStopped := make(chan struct{})
	server := serviceFunc(func(context.Context) error { return sentinel })
	refresh := serviceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		close(refreshStopped)
		return nil
	})

	err := runServices(context.Background(), server, refresh)
	if !errors.Is(err, sentinel) {
		t.Fatalf("runServices() error = %v", err)
	}
	waitServiceSignal(t, refreshStopped)
}

func TestRunServicesWithoutRefresh(t *testing.T) {
	called := false
	server := serviceFunc(func(context.Context) error {
		called = true
		return nil
	})
	if err := runServices(context.Background(), server, nil); err != nil || !called {
		t.Fatalf("runServices() error = %v, called=%v", err, called)
	}
}

type serviceFunc func(context.Context) error

func (f serviceFunc) Run(ctx context.Context) error { return f(ctx) }

func waitServiceSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("等待服务停止超时")
	}
}
