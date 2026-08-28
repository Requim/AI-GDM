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

// Service 读取最后成功数据并生成控制台状态。
type Service struct {
	risk         ports.LatestRiskReader
	weather      ports.WeatherSnapshotReader
	capabilities Capabilities
	clock        ports.Clock
}

// New 创建只读控制台服务。
func New(risk ports.LatestRiskReader, weather ports.WeatherSnapshotReader,
	capabilities Capabilities, clock ports.Clock,
) (*Service, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: 控制台时钟为空", domain.ErrInvalidInput)
	}
	capabilities.WeatherPoints = append([]spatial.Point(nil), capabilities.WeatherPoints...)
	return &Service{risk: risk, weather: weather, capabilities: capabilities, clock: clock}, nil
}

// Overview 返回当前配置和最后成功持久化数据，不执行网络刷新。
func (s *Service) Overview(ctx context.Context) Overview {
	now := s.clock.Now().UTC()
	sources := []SourceStatus{
		s.riskStatus(ctx, now),
		s.weatherStatus(ctx, now),
		configuredStatus("amap", "疏散地图与路线", "高德地图", "调度", s.capabilities.Map, "按需调用，不代表道路已由交管部门确认开放"),
		configuredStatus("bocha", "实时公开信息搜索", "博查搜索", "证据", s.capabilities.Search, "按需检索，结果需人工核验"),
		configuredStatus("llm", "智能研判说明", s.capabilities.LLMProvider, "AI", s.capabilities.LLM, llmDetail(s.capabilities)),
		resourceStatus("postgres", "空间数据库", "PostgreSQL / PostGIS", "基础设施", s.capabilities.Database),
		resourceStatus("redis", "缓存与限流存储", "Redis", "基础设施", s.capabilities.Cache),
	}
	return Overview{GeneratedAt: now, Environment: s.capabilities.Environment,
		Version: s.capabilities.Version, Sources: sources, Summary: summarize(sources)}
}

func (s *Service) riskStatus(ctx context.Context, now time.Time) SourceStatus {
	base := SourceStatus{ID: "lhasa", Name: "滑坡风险分析", Provider: "NASA Earthdata LHASA", Category: "风险"}
	if !s.capabilities.Database || s.risk == nil {
		return unavailable(base, "PostGIS 未配置，无法读取最后成功风险分析")
	}
	snapshot, zones, err := s.risk.LatestRisk(ctx, hazard.TypeLandslide)
	if err != nil {
		return readFailure(base, err, "尚无已持久化的完整风险分析")
	}
	base.UpdatedAt = latestTime(snapshot.RunAt, sourceTime(snapshot.Source))
	base.ValidTo = latestTime(snapshot.ValidTo, snapshot.Source.ValidTo)
	base.Detail = fmt.Sprintf("已保存 %d 个风险区；后台刷新%s", len(zones), enabledText(s.capabilities.Refresh))
	base.State = StateAvailable
	if snapshot.Status == hazard.SnapshotStale || snapshot.Source.IsStale(now) || expired(now, base.ValidTo) {
		base.State, base.Detail = StateStale, base.Detail+"；数据已超过有效期"
	}
	return base
}

func (s *Service) weatherStatus(ctx context.Context, now time.Time) SourceStatus {
	base := SourceStatus{ID: "weather", Name: "降雨与土壤湿度", Provider: "Open-Meteo", Category: "气象"}
	if !s.capabilities.Database || s.weather == nil {
		return unavailable(base, "PostGIS 未配置，无法读取最后成功气象批次")
	}
	values, err := s.weather.Latest(ctx, s.capabilities.WeatherPoints)
	if err != nil {
		return readFailure(base, err, "尚无当前监测点集的完整气象批次")
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
	return base
}

func configuredStatus(id, name, provider, category string, enabled bool, detail string) SourceStatus {
	state := StateDisabled
	if enabled {
		state = StateConfigured
	} else {
		detail = "未启用；确定性核心能力仍可独立运行"
	}
	return SourceStatus{ID: id, Name: name, Provider: provider, Category: category, State: state, Detail: detail}
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
