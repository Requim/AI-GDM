package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/ports"
)

func TestNewRejectsInvalidFixedComponents(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
	}{
		{name: "empty"},
		{name: "blank", ids: []string{""}},
		{name: "uppercase", ids: []string{"NASA"}},
		{name: "space", ids: []string{"open meteo"}},
		{name: "label injection", ids: []string{"x\"} 1"}},
		{name: "too long", ids: []string{"a" + strings.Repeat("b", maxComponentIDBytes)}},
		{name: "duplicate", ids: []string{"amap", "amap"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.ids); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error=%v", err)
			}
		})
	}
}

func TestRecordObservationTracksBusinessOutcomes(t *testing.T) {
	registry := mustRegistry(t, "provider")
	base := time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
	record(registry, "provider", ports.ObservationSuccess, base, time.Second, "")
	record(registry, "provider", ports.ObservationDegraded, base.Add(time.Minute), 2*time.Second, "fallback")
	record(registry, "provider", ports.ObservationFailure, base.Add(2*time.Minute), 3*time.Second, "timeout")
	status := registry.Snapshot()[0]
	if status.SuccessTotal != 1 || status.DegradedTotal != 1 || status.FailureTotal != 1 {
		t.Fatalf("累计结果错误: %+v", status)
	}
	if status.ConsecutiveFailures != 2 || status.LastOutcome != ports.ObservationFailure {
		t.Fatalf("连续失败状态错误: %+v", status)
	}
	if !status.LastSuccessAt.Equal(base) || !status.LastFailureAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("最后成功或失败时间错误: %+v", status)
	}
	record(registry, "provider", ports.ObservationSuccess, base.Add(3*time.Minute), time.Second, "")
	status = registry.Snapshot()[0]
	if status.ConsecutiveFailures != 0 || status.LastOutcome != ports.ObservationSuccess || status.SuccessTotal != 2 {
		t.Fatalf("成功未清零连续失败: %+v", status)
	}
}

func TestRecordObservationFailsClosed(t *testing.T) {
	registry := mustRegistry(t, "known")
	validTime := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	values := []ports.Observation{
		{ComponentID: "unknown", Outcome: ports.ObservationSuccess, ObservedAt: validTime},
		{ComponentID: "known", Outcome: "other", ObservedAt: validTime},
		{ComponentID: "known", Outcome: ports.ObservationSuccess},
		{ComponentID: "known", Outcome: ports.ObservationSuccess, ObservedAt: validTime.In(time.FixedZone("UTC+8", 8*60*60))},
		{ComponentID: "known", Outcome: ports.ObservationSuccess, ObservedAt: validTime, Duration: -1},
		{ComponentID: "known", Outcome: ports.ObservationSuccess, ObservedAt: validTime, ErrorClass: "timeout"},
		{ComponentID: "known", Outcome: ports.ObservationFailure, ObservedAt: validTime},
		{ComponentID: "known", Outcome: ports.ObservationFailure, ObservedAt: validTime, ErrorClass: "free text"},
		{ComponentID: "known", Outcome: ports.ObservationFailure, ObservedAt: validTime,
			ErrorClass: "a" + strings.Repeat("b", maxErrorClassBytes)},
	}
	for _, value := range values {
		registry.RecordObservation(value)
	}
	status := registry.Snapshot()[0]
	if status.SuccessTotal != 0 || status.DegradedTotal != 0 || status.FailureTotal != 0 {
		t.Fatalf("非法观测改变了状态: %+v", status)
	}
	if strings.Contains(registry.RenderMetrics(), "unknown") {
		t.Fatal("未知组件创建了动态指标标签")
	}
}

func TestSnapshotIsSortedAndIndependent(t *testing.T) {
	ids := []string{"weather", "amap", "lhasa"}
	registry := mustRegistry(t, ids...)
	ids[0] = "changed"
	first := registry.Snapshot()
	if got := []string{first[0].ComponentID, first[1].ComponentID, first[2].ComponentID}; strings.Join(got, ",") != "amap,lhasa,weather" {
		t.Fatalf("快照顺序错误: %v", got)
	}
	first[0].ComponentID = "mutated"
	first[0].SuccessTotal = 99
	second := registry.Snapshot()
	if second[0].ComponentID != "amap" || second[0].SuccessTotal != 0 {
		t.Fatalf("调用方修改污染注册表: %+v", second[0])
	}
}

func TestRegistryConcurrentRecording(t *testing.T) {
	registry := mustRegistry(t, "provider")
	base := time.Date(2026, 8, 30, 5, 0, 0, 0, time.UTC)
	const observations = 600
	var group sync.WaitGroup
	for index := 0; index < observations; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			outcome, class := concurrentOutcome(index)
			record(registry, "provider", outcome, base.Add(time.Duration(index)*time.Nanosecond), time.Millisecond, class)
		}(index)
	}
	group.Wait()
	status := registry.Snapshot()[0]
	if status.SuccessTotal != 200 || status.DegradedTotal != 200 || status.FailureTotal != 200 {
		t.Fatalf("并发累计错误: %+v", status)
	}
}

func TestRenderMetricsUsesOnlyFixedLabels(t *testing.T) {
	registry := mustRegistry(t, "amap", "lhasa")
	observedAt := time.Date(2026, 8, 30, 6, 0, 0, 500_000_000, time.UTC)
	record(registry, "amap", ports.ObservationFailure, observedAt, 1500*time.Millisecond, "secret_error_class")
	metrics := registry.RenderMetrics()
	required := []string{
		`ai_gdm_component_observations_total{component="amap",outcome="failure"} 1`,
		`ai_gdm_component_observations_total{component="lhasa",outcome="success"} 0`,
		`ai_gdm_component_observation_duration_seconds_sum{component="amap",outcome="failure"} 1.5`,
		`ai_gdm_component_last_failure_timestamp_seconds{component="amap"} 1788069600.5`,
	}
	for _, value := range required {
		if !strings.Contains(metrics, value) {
			t.Fatalf("指标缺少 %q:\n%s", value, metrics)
		}
	}
	if strings.Contains(metrics, "secret_error_class") || strings.Contains(metrics, "kind=") {
		t.Fatalf("指标泄露错误分类或非固定标签:\n%s", metrics)
	}
	assertMetricSamples(t, metrics)
}

func TestMetricsHandlerSetsBoundedResponseHeaders(t *testing.T) {
	registry := mustRegistry(t, "provider")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics?token=secret", nil)
	registry.MetricsHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("指标响应错误: status=%d headers=%v", response.Code, response.Header())
	}
	if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("指标处理器回显了请求内容")
	}
}

func mustRegistry(t *testing.T, componentIDs ...string) *Registry {
	t.Helper()
	registry, err := New(componentIDs)
	if err != nil {
		t.Fatalf("New() error=%v", err)
	}
	return registry
}

func record(registry *Registry, component string, outcome ports.ObservationOutcome,
	observedAt time.Time, duration time.Duration, class string,
) {
	registry.RecordObservation(ports.Observation{ComponentID: component, Outcome: outcome,
		ObservedAt: observedAt, Duration: duration, ErrorClass: class})
}

func concurrentOutcome(index int) (ports.ObservationOutcome, string) {
	switch index % 3 {
	case 0:
		return ports.ObservationSuccess, ""
	case 1:
		return ports.ObservationDegraded, "fallback"
	default:
		return ports.ObservationFailure, "timeout"
	}
}

func assertMetricSamples(t *testing.T, metrics string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(metrics), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("指标样本字段无效: %q", line)
		}
		if _, err := strconv.ParseFloat(parts[1], 64); err != nil {
			t.Fatalf("指标值无效: line=%q error=%v", line, err)
		}
	}
}
