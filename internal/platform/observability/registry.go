// Package observability 提供当前进程内固定组件的有界观测注册表。
package observability

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	maxComponentIDBytes = 64
	maxErrorClassBytes  = 64
)

// ErrInvalidConfig 表示固定组件清单不满足有界标签约束。
var ErrInvalidConfig = errors.New("观测注册表配置无效")

var metricOutcomes = [...]ports.ObservationOutcome{
	ports.ObservationSuccess,
	ports.ObservationDegraded,
	ports.ObservationFailure,
}

type componentEntry struct {
	status          ports.ComponentStatus
	durationSeconds [len(metricOutcomes)]float64
}

// Registry 汇总当前进程内固定组件的业务级观测。
type Registry struct {
	mu         sync.RWMutex
	components map[string]*componentEntry
	orderedIDs []string
}

// New 创建只接受固定组件标识的观测注册表。
func New(componentIDs []string) (*Registry, error) {
	ordered, err := validatedComponentIDs(componentIDs)
	if err != nil {
		return nil, err
	}
	components := make(map[string]*componentEntry, len(ordered))
	for _, id := range ordered {
		components[id] = &componentEntry{status: ports.ComponentStatus{ComponentID: id}}
	}
	return &Registry{components: components, orderedIDs: ordered}, nil
}

// RecordObservation 记录合法观测；未知组件或非法输入会被静默丢弃。
func (r *Registry) RecordObservation(observation ports.Observation) {
	index, valid := validObservation(observation)
	if r == nil || !valid {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.components[observation.ComponentID]
	if !exists {
		return
	}
	entry.durationSeconds[index] += observation.Duration.Seconds()
	incrementTotal(&entry.status, observation.Outcome)
	updateStatus(&entry.status, observation)
}

// Snapshot 返回按组件标识稳定排序的独立状态副本。
func (r *Registry) Snapshot() []ports.ComponentStatus {
	if r == nil {
		return []ports.ComponentStatus{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ports.ComponentStatus, 0, len(r.orderedIDs))
	for _, id := range r.orderedIDs {
		result = append(result, r.components[id].status)
	}
	return result
}

// RenderMetrics 返回 Prometheus 0.0.4 文本，仅包含固定组件和结果标签。
func (r *Registry) RenderMetrics() string {
	rows := r.metricRows()
	var output strings.Builder
	writeMetricHeader(&output)
	for _, row := range rows {
		writeObservationMetrics(&output, row)
	}
	for _, row := range rows {
		writeStatusMetrics(&output, row)
	}
	return output.String()
}

// MetricsHandler 返回禁用缓存的 Prometheus 文本处理器。
func (r *Registry) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(r.RenderMetrics()))
	})
}

type metricRow struct {
	status          ports.ComponentStatus
	durationSeconds [len(metricOutcomes)]float64
}

func (r *Registry) metricRows() []metricRow {
	if r == nil {
		return []metricRow{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := make([]metricRow, 0, len(r.orderedIDs))
	for _, id := range r.orderedIDs {
		entry := r.components[id]
		rows = append(rows, metricRow{status: entry.status, durationSeconds: entry.durationSeconds})
	}
	return rows
}

func validatedComponentIDs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: 固定组件清单为空", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(values))
	result := append([]string(nil), values...)
	for _, value := range result {
		if !validBoundedName(value, maxComponentIDBytes) {
			return nil, fmt.Errorf("%w: 组件标识 %q 无效", ErrInvalidConfig, value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%w: 组件标识 %q 重复", ErrInvalidConfig, value)
		}
		seen[value] = struct{}{}
	}
	sort.Strings(result)
	return result, nil
}

func validObservation(value ports.Observation) (int, bool) {
	index, valid := outcomeIndex(value.Outcome)
	if !valid || !validUTC(value.ObservedAt) || value.Duration < 0 {
		return 0, false
	}
	if value.Outcome == ports.ObservationSuccess {
		return index, value.ErrorClass == ""
	}
	return index, validBoundedName(value.ErrorClass, maxErrorClassBytes)
}

func validUTC(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func validBoundedName(value string, limit int) bool {
	if value == "" || len(value) > limit || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !validNameCharacter(value[index]) {
			return false
		}
	}
	return true
}

func validNameCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' ||
		strings.ContainsRune("_.:-", rune(value))
}

func outcomeIndex(value ports.ObservationOutcome) (int, bool) {
	for index, outcome := range metricOutcomes {
		if value == outcome {
			return index, true
		}
	}
	return 0, false
}

func incrementTotal(status *ports.ComponentStatus, outcome ports.ObservationOutcome) {
	switch outcome {
	case ports.ObservationSuccess:
		status.SuccessTotal++
	case ports.ObservationDegraded:
		status.DegradedTotal++
	case ports.ObservationFailure:
		status.FailureTotal++
	}
}

func updateStatus(status *ports.ComponentStatus, observation ports.Observation) {
	status.LastAttemptAt = observation.ObservedAt
	status.LastOutcome = observation.Outcome
	if observation.Outcome == ports.ObservationSuccess {
		status.LastSuccessAt = observation.ObservedAt
		status.ConsecutiveFailures = 0
		return
	}
	status.LastFailureAt = observation.ObservedAt
	status.ConsecutiveFailures++
}

func writeMetricHeader(output *strings.Builder) {
	output.WriteString("# HELP ai_gdm_component_observations_total Business-level component observations.\n")
	output.WriteString("# TYPE ai_gdm_component_observations_total counter\n")
	output.WriteString("# HELP ai_gdm_component_observation_duration_seconds Business-level component duration.\n")
	output.WriteString("# TYPE ai_gdm_component_observation_duration_seconds summary\n")
	output.WriteString("# HELP ai_gdm_component_consecutive_failures Consecutive degraded or failed observations.\n")
	output.WriteString("# TYPE ai_gdm_component_consecutive_failures gauge\n")
	output.WriteString("# HELP ai_gdm_component_last_attempt_timestamp_seconds Last business-level attempt.\n")
	output.WriteString("# TYPE ai_gdm_component_last_attempt_timestamp_seconds gauge\n")
	output.WriteString("# HELP ai_gdm_component_last_success_timestamp_seconds Last business-level success.\n")
	output.WriteString("# TYPE ai_gdm_component_last_success_timestamp_seconds gauge\n")
	output.WriteString("# HELP ai_gdm_component_last_failure_timestamp_seconds Last degraded or failed observation.\n")
	output.WriteString("# TYPE ai_gdm_component_last_failure_timestamp_seconds gauge\n")
	output.WriteString("# HELP ai_gdm_component_last_outcome Last business-level component outcome.\n")
	output.WriteString("# TYPE ai_gdm_component_last_outcome gauge\n")
}

func writeObservationMetrics(output *strings.Builder, row metricRow) {
	for index, outcome := range metricOutcomes {
		labels := metricLabels(row.status.ComponentID, outcome)
		count := outcomeTotal(row.status, outcome)
		fmt.Fprintf(output, "ai_gdm_component_observations_total%s %d\n", labels, count)
		fmt.Fprintf(output, "ai_gdm_component_observation_duration_seconds_sum%s %s\n",
			labels, formatFloat(row.durationSeconds[index]))
		fmt.Fprintf(output, "ai_gdm_component_observation_duration_seconds_count%s %d\n", labels, count)
	}
}

func writeStatusMetrics(output *strings.Builder, row metricRow) {
	component := "{component=" + strconv.Quote(row.status.ComponentID) + "}"
	fmt.Fprintf(output, "ai_gdm_component_consecutive_failures%s %d\n",
		component, row.status.ConsecutiveFailures)
	writeTimestamp(output, "ai_gdm_component_last_attempt_timestamp_seconds", component, row.status.LastAttemptAt)
	writeTimestamp(output, "ai_gdm_component_last_success_timestamp_seconds", component, row.status.LastSuccessAt)
	writeTimestamp(output, "ai_gdm_component_last_failure_timestamp_seconds", component, row.status.LastFailureAt)
	for _, outcome := range metricOutcomes {
		value := 0
		if row.status.LastOutcome == outcome {
			value = 1
		}
		fmt.Fprintf(output, "ai_gdm_component_last_outcome%s %d\n",
			metricLabels(row.status.ComponentID, outcome), value)
	}
}

func writeTimestamp(output *strings.Builder, name, labels string, value time.Time) {
	seconds := 0.0
	if !value.IsZero() {
		seconds = float64(value.Unix()) + float64(value.Nanosecond())/1e9
	}
	fmt.Fprintf(output, "%s%s %s\n", name, labels, formatDecimal(seconds))
}

func outcomeTotal(status ports.ComponentStatus, outcome ports.ObservationOutcome) uint64 {
	switch outcome {
	case ports.ObservationSuccess:
		return status.SuccessTotal
	case ports.ObservationDegraded:
		return status.DegradedTotal
	default:
		return status.FailureTotal
	}
}

func metricLabels(component string, outcome ports.ObservationOutcome) string {
	return fmt.Sprintf("{component=%s,outcome=%s}",
		strconv.Quote(component), strconv.Quote(string(outcome)))
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func formatDecimal(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
