package ports

import "time"

// ObservationOutcome 表示一次业务级组件调用的受限结果。
type ObservationOutcome string

const (
	ObservationSuccess  ObservationOutcome = "success"
	ObservationDegraded ObservationOutcome = "degraded"
	ObservationFailure  ObservationOutcome = "failure"
)

// Observation 保存一次不含请求内容和自由错误文本的组件观测。
type Observation struct {
	ComponentID string
	Outcome     ObservationOutcome
	ObservedAt  time.Time
	Duration    time.Duration
	ErrorClass  string
}

// ComponentStatus 保存当前进程内一个固定组件的业务级观测汇总。
type ComponentStatus struct {
	ComponentID         string
	LastAttemptAt       time.Time
	LastSuccessAt       time.Time
	LastFailureAt       time.Time
	LastOutcome         ObservationOutcome
	ConsecutiveFailures uint64
	SuccessTotal        uint64
	DegradedTotal       uint64
	FailureTotal        uint64
}

// ObservationRecorder 接收业务级组件观测；非法观测不得影响业务调用。
type ObservationRecorder interface {
	RecordObservation(Observation)
}

// ComponentStatusReader 返回当前进程内固定组件的观测快照。
type ComponentStatusReader interface {
	Snapshot() []ComponentStatus
}
