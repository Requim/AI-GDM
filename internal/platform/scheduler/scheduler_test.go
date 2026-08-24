package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerImmediatelyRunsAndWaitsAfterCompletion(t *testing.T) {
	started := make(chan time.Time, 2)
	finished := make(chan time.Time, 1)
	release := make(chan struct{})
	waited := make(chan time.Duration, 1)
	nextRound := make(chan struct{})
	var calls atomic.Int32
	task := Task{Name: "weather", Run: func(context.Context) error {
		started <- time.Now()
		if calls.Add(1) == 1 {
			<-release
			finished <- time.Now()
		}
		return nil
	}}
	runner := newRunner(t, 30*time.Minute, time.Second, task)
	runner.waitFn = controlledWait(waited, nextRound)
	done := run(t, runner)

	receiveTime(t, started)
	close(release)
	receiveTime(t, finished)
	if interval := receiveDuration(t, waited); interval != 30*time.Minute {
		t.Fatalf("等待间隔 = %s，期望 30m", interval)
	}
	nextRound <- struct{}{}
	receiveTime(t, started)
	runner.Stop()
	waitDone(t, done)
}

func TestRunnerNeverOverlapsAndContinuesAfterError(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	third := make(chan struct{})
	task := Task{Name: "weather", Run: func(context.Context) error {
		current := active.Add(1)
		updateMaximum(&maximum, current)
		defer active.Add(-1)
		call := calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		if call == 1 {
			return errors.New("临时供应商错误")
		}
		if call == 3 {
			close(third)
		}
		return nil
	}}
	runner := newRunner(t, 5*time.Millisecond, time.Second, task)
	done := run(t, runner)
	waitSignal(t, third)
	runner.Stop()
	waitDone(t, done)

	if maximum.Load() != 1 {
		t.Fatalf("任务发生重叠，最大并发数 = %d", maximum.Load())
	}
	if calls.Load() < 3 {
		t.Fatalf("任务报错后未继续，执行次数 = %d", calls.Load())
	}
}

func TestRunnerContinuesWithNextTaskAfterError(t *testing.T) {
	secondCalled := make(chan struct{})
	first := Task{Name: "lhasa", Run: func(context.Context) error {
		return errors.New("临时下载错误")
	}}
	second := Task{Name: "weather", Run: func(context.Context) error {
		close(secondCalled)
		return nil
	}}
	runner, err := New(time.Hour, time.Second, testLogger(), first, second)
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	done := run(t, runner)
	waitSignal(t, secondCalled)
	runner.Stop()
	waitDone(t, done)
}

func TestRunnerProvidesTimeoutContext(t *testing.T) {
	timedOut := make(chan error, 1)
	task := Task{Name: "weather", Run: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 100*time.Millisecond {
			return errors.New("任务上下文缺少正确的截止时间")
		}
		<-ctx.Done()
		timedOut <- ctx.Err()
		return ctx.Err()
	}}
	runner := newRunner(t, time.Second, 40*time.Millisecond, task)
	done := run(t, runner)

	if err := receiveError(t, timedOut); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("任务错误 = %v，期望 DeadlineExceeded", err)
	}
	runner.Stop()
	waitDone(t, done)
}

func TestStopCancelsCurrentTaskAndPreventsRestart(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan error, 1)
	task := Task{Name: "weather", Run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		canceled <- ctx.Err()
		return ctx.Err()
	}}
	runner := newRunner(t, time.Second, time.Second, task)
	done := run(t, runner)
	waitSignal(t, started)
	runner.Stop()

	if err := receiveError(t, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("任务错误 = %v，期望 Canceled", err)
	}
	waitDone(t, done)
	if err := runner.Run(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("停止后 Run 错误 = %v，期望 ErrStopped", err)
	}
}

func TestRunnerStopsAfterTaskIgnoresCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	task := Task{Name: "blocked", Run: func(context.Context) error {
		close(started)
		<-release
		return nil
	}}
	runner := newRunner(t, time.Second, 20*time.Millisecond, task)
	runner.cancelGrace = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background()) }()
	waitSignal(t, started)

	select {
	case err := <-done:
		if !errors.Is(err, ErrTaskUnresponsive) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("不响应取消的任务阻塞了调度器")
	}
	close(release)
}

func TestContextCancellationStopsWaitingRunner(t *testing.T) {
	called := make(chan struct{}, 1)
	task := Task{Name: "weather", Run: func(context.Context) error {
		called <- struct{}{}
		return nil
	}}
	runner := newRunner(t, time.Hour, time.Second, task)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitSignal(t, called)
	cancel()
	waitDone(t, done)
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	valid := Task{Name: "weather", Run: func(context.Context) error { return nil }}
	tests := []struct {
		name     string
		interval time.Duration
		timeout  time.Duration
		tasks    []Task
		want     error
	}{
		{name: "间隔非正数", timeout: time.Second, tasks: []Task{valid}, want: ErrInvalidConfig},
		{name: "超时非正数", interval: time.Second, tasks: []Task{valid}, want: ErrInvalidConfig},
		{name: "没有任务", interval: time.Second, timeout: time.Second, want: ErrInvalidTask},
		{name: "空任务", interval: time.Second, timeout: time.Second, tasks: []Task{{}}, want: ErrInvalidTask},
		{name: "任务重名", interval: time.Second, timeout: time.Second, tasks: []Task{valid, valid}, want: ErrInvalidTask},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.interval, test.timeout, testLogger(), test.tasks...); !errors.Is(err, test.want) {
				t.Fatalf("New error = %v", err)
			}
		})
	}
}

func newRunner(t *testing.T, interval, timeout time.Duration, task Task) *Runner {
	t.Helper()
	runner, err := New(interval, timeout, testLogger(), task)
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	return runner
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func run(t *testing.T, runner *Runner) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background()) }()
	return done
}

func receiveTime(t *testing.T, values <-chan time.Time) time.Time {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("等待时间值超时")
		return time.Time{}
	}
}

func receiveError(t *testing.T, values <-chan error) error {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("等待错误值超时")
		return nil
	}
}

func receiveDuration(t *testing.T, values <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("等待时长值超时")
		return 0
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("等待信号超时")
	}
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待调度器停止超时")
	}
}

func updateMaximum(maximum *atomic.Int32, current int32) {
	for current > maximum.Load() {
		if maximum.CompareAndSwap(maximum.Load(), current) {
			return
		}
	}
}

func controlledWait(waited chan<- time.Duration, next <-chan struct{}) func(context.Context, time.Duration) bool {
	return func(ctx context.Context, interval time.Duration) bool {
		waited <- interval
		select {
		case <-ctx.Done():
			return false
		case <-next:
			return true
		}
	}
}
