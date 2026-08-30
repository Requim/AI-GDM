// Package dashboard 汇总控制台所需的只读状态，不触发外部供应商调用。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Capabilities 描述组合根已配置的运行能力，不包含任何密钥。
type Capabilities struct {
	Environment   string
	Version       string
	Database      bool
	Cache         bool
	Refresh       bool
	Map           bool
	Search        bool
	LLM           bool
	LLMProvider   string
	LLMModel      string
	WeatherPoints []spatial.Point
}

// Service 读取最后成功数据和当前进程观测，生成控制台状态。
type Service struct {
	risk         ports.LatestRiskReader
	weather      ports.WeatherSnapshotReader
	capabilities Capabilities
	observations ports.ComponentStatusReader
	clock        ports.Clock
}

// New 创建只读控制台服务。
func New(risk ports.LatestRiskReader, weather ports.WeatherSnapshotReader,
	capabilities Capabilities, observations ports.ComponentStatusReader, clock ports.Clock,
) (*Service, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: 控制台时钟为空", domain.ErrInvalidInput)
	}
	capabilities.WeatherPoints = append([]spatial.Point(nil), capabilities.WeatherPoints...)
	return &Service{risk: risk, weather: weather, capabilities: capabilities,
		observations: observations, clock: clock}, nil
}

// Overview 返回当前配置、业务观测和最后成功持久化数据，不执行网络刷新。
func (s *Service) Overview(ctx context.Context) Overview {
	now := s.clock.Now().UTC()
	statuses := observationIndex(s.observations)
	sources := []SourceStatus{
		s.riskStatus(ctx, now, statuses["lhasa"]),
		s.weatherStatus(ctx, now, statuses["weather"]),
		observedStatus("amap", "疏散地图与路线", "高德地图", "调度", s.capabilities.Map,
			"按需调用，不代表道路已由交管部门确认开放", statuses["amap"]),
		observedStatus("bocha", "实时公开信息搜索", "博查搜索", "证据", s.capabilities.Search,
			"按需检索，结果需人工核验", statuses["bocha"]),
		observedStatus("llm", "智能研判说明", s.capabilities.LLMProvider, "AI", s.capabilities.LLM,
			llmDetail(s.capabilities), statuses["llm"]),
		liveEventStatus(),
		resourceStatus("postgres", "空间数据库", "PostgreSQL / PostGIS", "基础设施", s.capabilities.Database),
		resourceStatus("redis", "缓存与限流存储", "Redis", "基础设施", s.capabilities.Cache),
	}
	return Overview{GeneratedAt: now, Environment: s.capabilities.Environment,
		Version: s.capabilities.Version, Sources: sources, Summary: summarize(sources)}
}

func (s *Service) riskStatus(ctx context.Context, now time.Time, observed componentObservation) SourceStatus {
	base := SourceStatus{ID: "lhasa", Name: "滑坡风险分析", Provider: "NASA Earthdata LHASA", Category: "风险"}
	if !s.capabilities.Database || s.risk == nil {
		return applyDataObservation(unavailable(base, "PostGIS 未配置，无法读取最后成功风险分析"), observed)
	}
	snapshot, zones, err := s.risk.LatestRisk(ctx, hazard.TypeLandslide)
	if err != nil {
		return applyDataObservation(readFailure(base, err, "尚无已持久化的完整风险分析"), observed)
	}
	base.UpdatedAt = latestTime(snapshot.RunAt, sourceTime(snapshot.Source))
	base.ValidTo = latestTime(snapshot.ValidTo, snapshot.Source.ValidTo)
	base.Detail = fmt.Sprintf("已保存 %d 个风险区；后台刷新%s", len(zones), enabledText(s.capabilities.Refresh))
	base.State = StateAvailable
	if snapshot.Status == hazard.SnapshotStale || snapshot.Source.IsStale(now) || expired(now, base.ValidTo) {
		base.State, base.Detail = StateStale, base.Detail+"；数据已超过有效期"
	}
	return applyDataObservation(base, observed)
}

func (s *Service) weatherStatus(ctx context.Context, now time.Time, observed componentObservation) SourceStatus {
	base := SourceStatus{ID: "weather", Name: "降雨与土壤湿度", Provider: "Open-Meteo", Category: "气象"}
	if !s.capabilities.Database || s.weather == nil {
		return applyDataObservation(unavailable(base, "PostGIS 未配置，无法读取最后成功气象批次"), observed)
	}
	values, err := s.weather.Latest(ctx, s.capabilities.WeatherPoints)
	if err != nil {
		return applyDataObservation(readFailure(base, err, "尚无当前监测点集的完整气象批次"), observed)
	}
	base.State, base.Detail = StateAvailable, fmt.Sprintf("%d 个监测点；后台刷新%s", len(values), enabledText(s.capabilities.Refresh))
	for _, value := range values {
		base.UpdatedAt = latestTime(base.UpdatedAt, sourceTime(value.Source))
		base.ValidTo = latestTime(base.ValidTo, value.Source.ValidTo)
		if value.Source.IsStale(now) {
			base.State = StateStale
		}
	}
	if expired(now, base.ValidTo) {
		base.State = StateStale
	}
	if base.State == StateStale {
		base.Detail += "；当前展示最后成功但已过期的数据"
	}
	return applyDataObservation(base, observed)
}

type componentObservation struct {
	status ports.ComponentStatus
	found  bool
}

func observationIndex(reader ports.ComponentStatusReader) map[string]componentObservation {
	result := make(map[string]componentObservation)
	if reader == nil {
		return result
	}
	for _, status := range reader.Snapshot() {
		result[status.ComponentID] = componentObservation{status: status, found: true}
	}
	return result
}

func applyDataObservation(base SourceStatus, observed componentObservation) SourceStatus {
	base = attachObservation(base, observed)
	if base.State == StateStale || base.State == StateUnavailable {
		return appendObservationDetail(base, observed)
	}
	if base.State == StateWaiting {
		if observed.found && (observed.status.LastOutcome == ports.ObservationDegraded ||
			observed.status.LastOutcome == ports.ObservationFailure) {
			base.State = StateUnavailable
		}
		return appendObservationDetail(base, observed)
	}
	if observed.found && observed.status.LastOutcome != "" &&
		observed.status.LastOutcome != ports.ObservationSuccess {
		base.State = StateDegraded
	}
	return appendObservationDetail(base, observed)
}

func observedStatus(id, name, provider, category string, enabled bool, detail string,
	observed componentObservation,
) SourceStatus {
	base := SourceStatus{ID: id, Name: name, Provider: provider, Category: category, Detail: detail}
	if !enabled {
		base.State, base.Detail = StateDisabled, "未启用；确定性核心能力仍可独立运行"
		return base
	}
	base = attachObservation(base, observed)
	if !observed.found || observed.status.LastOutcome == "" {
		base.State = StateWaiting
	} else {
		base.State = observedState(observed.status.LastOutcome)
	}
	return appendObservationDetail(base, observed)
}

func observedState(outcome ports.ObservationOutcome) SourceState {
	switch outcome {
	case ports.ObservationSuccess:
		return StateAvailable
	case ports.ObservationDegraded:
		return StateDegraded
	default:
		return StateUnavailable
	}
}

func attachObservation(base SourceStatus, observed componentObservation) SourceStatus {
	if !observed.found {
		return base
	}
	base.LastAttemptAt = observed.status.LastAttemptAt.UTC()
	base.LastSuccessAt = observed.status.LastSuccessAt.UTC()
	return base
}

func appendObservationDetail(base SourceStatus, observed componentObservation) SourceStatus {
	if !observed.found || observed.status.LastOutcome == "" {
		base.Detail += "；当前进程尚无业务级调用观测"
		return base
	}
	switch observed.status.LastOutcome {
	case ports.ObservationSuccess:
		base.Detail += "；最近业务调用成功"
	case ports.ObservationDegraded:
		base.Detail += degradedDetail(base)
	case ports.ObservationFailure:
		base.Detail += failureDetail(base, observed.status.ConsecutiveFailures)
	}
	return base
}

func degradedDetail(base SourceStatus) string {
	if !base.UpdatedAt.IsZero() || !base.LastSuccessAt.IsZero() {
		return "；最近业务调用已降级，仍保留最后成功数据"
	}
	return "；最近业务调用已降级，未形成可用结果"
}

func failureDetail(base SourceStatus, failures uint64) string {
	detail := fmt.Sprintf("；最近业务调用失败（连续 %d 次）", failures)
	if !base.UpdatedAt.IsZero() || !base.LastSuccessAt.IsZero() {
		detail += "，仍保留最后成功数据"
	}
	return detail
}

func liveEventStatus() SourceStatus {
	return SourceStatus{ID: "live-events", Name: "实时事件目录", Provider: "未接入", Category: "事件",
		State: StateUnknown, Detail: "未接入经核验的实时事件源，无法判断当前是否存在实时事件"}
}

func resourceStatus(id, name, provider, category string, connected bool) SourceStatus {
	if !connected {
		return SourceStatus{ID: id, Name: name, Provider: provider, Category: category,
			State: StateDisabled, Detail: "未配置连接"}
	}
	return SourceStatus{ID: id, Name: name, Provider: provider, Category: category,
		State: StateAvailable, Detail: "启动时连接检查已通过"}
}

func unavailable(base SourceStatus, detail string) SourceStatus {
	base.State, base.Detail = StateUnavailable, detail
	return base
}

func readFailure(base SourceStatus, err error, waiting string) SourceStatus {
	if errors.Is(err, domain.ErrNotFound) {
		base.State, base.Detail = StateWaiting, waiting
		return base
	}
	return unavailable(base, "读取最后成功数据失败")
}

func sourceTime(source provenance.Provenance) time.Time {
	return latestTime(source.ObservedAt, source.RevisionFirstSeenAt, source.FetchedAt)
}

func latestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.After(result) {
			result = value.UTC()
		}
	}
	return result
}

func expired(now, validTo time.Time) bool {
	return !validTo.IsZero() && now.After(validTo)
}

func enabledText(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "未启用"
}

func llmDetail(capabilities Capabilities) string {
	if !capabilities.LLM {
		return "未启用；确定性分析不受影响"
	}
	return fmt.Sprintf("模型 %s；只生成解释性文字", capabilities.LLMModel)
}
