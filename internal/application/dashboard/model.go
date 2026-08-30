package dashboard

import "time"

// SourceState 表示控制台可展示的数据源运行状态。
type SourceState string

const (
	StateAvailable   SourceState = "available"
	StateDegraded    SourceState = "degraded"
	StateStale       SourceState = "stale"
	StateWaiting     SourceState = "waiting"
	StateConfigured  SourceState = "configured"
	StateUnknown     SourceState = "unknown"
	StateDisabled    SourceState = "disabled"
	StateUnavailable SourceState = "unavailable"
)

// SourceStatus 保存一个数据源的配置、时效和最后成功时间。
type SourceStatus struct {
	ID            string
	Name          string
	Provider      string
	Category      string
	State         SourceState
	UpdatedAt     time.Time
	ValidTo       time.Time
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	Detail        string
}

// Summary 汇总当前数据源状态数量。
type Summary struct {
	Available   int
	Attention   int
	Unavailable int
}

// Overview 是监控控制台的一致性只读快照。
type Overview struct {
	GeneratedAt time.Time
	Environment string
	Version     string
	Sources     []SourceStatus
	Summary     Summary
}

func summarize(sources []SourceStatus) Summary {
	var result Summary
	for _, source := range sources {
		switch source.State {
		case StateAvailable:
			result.Available++
		case StateDegraded, StateStale, StateWaiting, StateConfigured, StateUnknown:
			result.Attention++
		default:
			result.Unavailable++
		}
	}
	return result
}
