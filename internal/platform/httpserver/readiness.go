package httpserver

import "sync/atomic"

// Readiness 保存进程是否可以接收业务流量。
type Readiness struct {
	ready atomic.Bool
}

// Set 更新就绪状态。
func (r *Readiness) Set(value bool) {
	r.ready.Store(value)
}

// IsReady 返回当前就绪状态。
func (r *Readiness) IsReady() bool {
	return r.ready.Load()
}
