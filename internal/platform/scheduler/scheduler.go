package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	// ErrInvalidConfig 表示调度器配置不满足运行边界。
	ErrInvalidConfig = errors.New("调度器配置无效")
	// ErrInvalidTask 表示任务定义缺少名称、执行函数或名称重复。
	ErrInvalidTask = errors.New("调度任务无效")
	// ErrAlreadyRunning 表示同一调度器实例已在运行。
	ErrAlreadyRunning = errors.New("调度器已在运行")
	// ErrStopped 表示调度器已被永久停止。
	ErrStopped = errors.New("调度器已停止")
	// ErrTaskTimeout 表示任务超过单次执行时限。
	ErrTaskTimeout = errors.New("任务执行超时")
	// ErrTaskUnresponsive 表示任务在取消后仍未退出，调度器已停止后续执行。
	ErrTaskUnresponsive = errors.New("任务不响应取消")
)

const defaultCancelGrace = time.Second

// Task 描述一个由调度器周期执行的独立任务。
type Task struct {
	// Name 是用于日志和去重的稳定任务名称。
	Name string
	// Run 执行任务；实现必须响应上下文取消，调度器会等待其返回以避免重叠。
	Run func(context.Context) error
}

// Runner 按固定间隔串行执行任务，保证同一实例内任务永不重叠。
type Runner struct {
	interval    time.Duration
	timeout     time.Duration
	tasks       []Task
	logger      *slog.Logger
	waitFn      func(context.Context, time.Duration) bool
	cancelGrace time.Duration

	mu      sync.Mutex
	running bool
	stopped bool
	cancel  context.CancelFunc
}

// New 创建任务调度器；interval 从一轮结束后开始计算。
func New(interval, timeout time.Duration, logger *slog.Logger, tasks ...Task) (*Runner, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("%w: 调度间隔必须为正数: %s", ErrInvalidConfig, interval)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: 任务超时时间必须为正数: %s", ErrInvalidConfig, timeout)
	}
	if err := validateTasks(tasks); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		interval:    interval,
		timeout:     timeout,
		tasks:       append([]Task(nil), tasks...),
		logger:      logger,
		waitFn:      waitForInterval,
		cancelGrace: defaultCancelGrace,
	}, nil
}

// Run 立即执行首轮任务，并持续运行直到上下文取消或 Stop 被调用。
func (r *Runner) Run(ctx context.Context) error {
	runCtx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer r.finish()

	for {
		if err = r.runRound(runCtx); err != nil {
			return err
		}
		if runCtx.Err() != nil {
			return nil
		}
		if !r.waitFn(runCtx, r.interval) {
			return nil
		}
	}
}

// Stop 永久停止调度器，并取消当前任务的上下文。
func (r *Runner) Stop() {
	r.mu.Lock()
	r.stopped = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) begin(ctx context.Context) (context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return nil, ErrAlreadyRunning
	}
	if r.stopped {
		return nil, ErrStopped
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.running = true
	r.cancel = cancel
	return runCtx, nil
}

func (r *Runner) finish() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.running = false
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) runRound(ctx context.Context) error {
	for _, task := range r.tasks {
		if ctx.Err() != nil {
			return nil
		}
		if err := r.runTask(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) runTask(ctx context.Context, task Task) error {
	taskCtx, cancel := context.WithTimeout(ctx, r.timeout)
	started := time.Now()
	result := make(chan error, 1)
	go func() { result <- task.Run(taskCtx) }()
	err, responsive := waitTask(taskCtx, result, r.cancelGrace)
	timedOut := errors.Is(taskCtx.Err(), context.DeadlineExceeded)
	cancel()
	if !responsive {
		return fmt.Errorf("%w: %s", ErrTaskUnresponsive, task.Name)
	}
	if ctx.Err() != nil {
		return nil
	}
	if timedOut {
		err = errors.Join(ErrTaskTimeout, err)
	}
	if err != nil {
		r.logger.ErrorContext(ctx, "定时任务执行失败",
			"task", task.Name, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return nil
	}
	r.logger.InfoContext(ctx, "定时任务执行完成",
		"task", task.Name, "duration_ms", time.Since(started).Milliseconds())
	return nil
}

func validateTasks(tasks []Task) error {
	if len(tasks) == 0 {
		return fmt.Errorf("%w: 调度器至少需要一个任务", ErrInvalidTask)
	}
	names := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if task.Name == "" || task.Run == nil {
			return fmt.Errorf("%w: 任务名称和执行函数不能为空", ErrInvalidTask)
		}
		if _, exists := names[task.Name]; exists {
			return fmt.Errorf("%w: 任务名称重复: %s", ErrInvalidTask, task.Name)
		}
		names[task.Name] = struct{}{}
	}
	return nil
}

func waitTask(ctx context.Context, result <-chan error, grace time.Duration) (error, bool) {
	select {
	case err := <-result:
		return err, true
	case <-ctx.Done():
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-result:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

func waitForInterval(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
