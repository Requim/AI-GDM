// Package memory 提供不依赖外部数据库的历史回放目录适配器。
package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/domain/survival"
)

// SurvivalCatalog 保存公开事件元数据和不含个人信息的回放场景。
// 事件数据是可审计的目录快照，场景只用于规则回放，不代表真实人员记录。
type SurvivalCatalog struct {
	mu        sync.RWMutex
	events    []survival.HistoricalEvent
	scenarios map[string]survival.Scenario
}

var _ interface {
	ListEvents(context.Context) ([]survival.HistoricalEvent, error)
} = (*SurvivalCatalog)(nil)

var _ interface {
	GetScenario(context.Context, string) (survival.Scenario, error)
} = (*SurvivalCatalog)(nil)

// NewSurvivalCatalog 创建一份带统一抓取时间的公开历史目录。
func NewSurvivalCatalog(fetchedAt time.Time) (*SurvivalCatalog, error) {
	if fetchedAt.IsZero() {
		return nil, fmt.Errorf("%w: 历史目录抓取时间为空", domain.ErrInvalidInput)
	}
	if _, offset := fetchedAt.Zone(); offset != 0 {
		return nil, fmt.Errorf("%w: 历史目录抓取时间必须使用 UTC", domain.ErrInvalidInput)
	}
	events := defaultEvents(fetchedAt)
	for index, event := range events {
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("校验历史事件 %d: %w", index, err)
		}
	}
	scenarios := defaultScenarios()
	for id, scenario := range scenarios {
		if err := scenario.Validate(); err != nil {
			return nil, fmt.Errorf("校验回放场景 %s: %w", id, err)
		}
	}
	return &SurvivalCatalog{events: events, scenarios: scenarios}, nil
}

// NewDefaultSurvivalCatalog 使用当前 UTC 生成目录快照。
func NewDefaultSurvivalCatalog() (*SurvivalCatalog, error) {
	return NewSurvivalCatalog(time.Now().UTC())
}

// ListEvents 返回按事件日期倒序排列的匿名历史事件。
func (c *SurvivalCatalog) ListEvents(ctx context.Context) ([]survival.HistoricalEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("读取历史事件目录: %w", err)
	}
	if c == nil {
		return nil, fmt.Errorf("%w: 历史事件目录为空", domain.ErrInvalidInput)
	}
	c.mu.RLock()
	values := append([]survival.HistoricalEvent(nil), c.events...)
	c.mu.RUnlock()
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].EventDate.Equal(values[j].EventDate) {
			return values[i].ID < values[j].ID
		}
		return values[i].EventDate.After(values[j].EventDate)
	})
	return values, nil
}

// GetScenario 返回指定的匿名回放场景。
func (c *SurvivalCatalog) GetScenario(ctx context.Context, id string) (survival.Scenario, error) {
	if err := ctx.Err(); err != nil {
		return survival.Scenario{}, fmt.Errorf("读取回放场景 %s: %w", id, err)
	}
	if c == nil {
		return survival.Scenario{}, fmt.Errorf("%w: 历史事件目录为空", domain.ErrInvalidInput)
	}
	c.mu.RLock()
	scenario, ok := c.scenarios[id]
	c.mu.RUnlock()
	if !ok {
		return survival.Scenario{}, fmt.Errorf("%w: 回放场景 %s 不存在", domain.ErrNotFound, id)
	}
	return scenario, nil
}

func defaultEvents(fetchedAt time.Time) []survival.HistoricalEvent {
	return []survival.HistoricalEvent{
		{
			ID: "case-oso-2014", DatasetEventID: "catalog:usgs:oso:2014-03-22",
			EventDate:     time.Date(2014, 3, 22, 0, 0, 0, 0, time.UTC),
			TimePrecision: "day", Category: "landslide", Trigger: "降雨与地形条件",
			Location:         spatial.Point{Longitude: -121.930, Latitude: 48.275},
			LocationAccuracy: "approximate event centroid", Country: "United States",
			AdminArea: "Washington, Snohomish County", Fatalities: 43, Injuries: 0,
			Source: historicalSource(fetchedAt,
				"https://www.usgs.gov/news/featured-story/five-years-later-oso-sr-530-landslide-washington",
				"USGS Five Years Later - The Oso (SR 530) Landslide in Washington"),
			Limitations: []string{"historical_replay", "伤情未按统一口径披露", "位置为事件中心近似值"},
		},
		{
			ID: "case-montecito-2018", DatasetEventID: "catalog:usgs:montecito:2018-01-09",
			EventDate:     time.Date(2018, 1, 9, 0, 0, 0, 0, time.UTC),
			TimePrecision: "day", Category: "debris_flow", Trigger: "强降雨与火烧迹地",
			Location:         spatial.Point{Longitude: -119.632, Latitude: 34.445},
			LocationAccuracy: "approximate event centroid", Country: "United States",
			AdminArea: "California, Santa Barbara County", Fatalities: 23, Injuries: 167,
			Source: historicalSource(fetchedAt,
				"https://www.usgs.gov/publications/inundation-flow-dynamics-and-damage-9-january-2018-montecito-debris-flow-event",
				"USGS Inundation, flow dynamics, and damage in the 9 January 2018 Montecito Debris-Flow Event"),
			Limitations: []string{"historical_replay", "伤情为公开来源中的至少数量", "位置为事件中心近似值"},
		},
		{
			ID: "case-mud-creek-2017", DatasetEventID: "catalog:usgs:mud-creek:2017-05-20",
			EventDate:     time.Date(2017, 5, 20, 0, 0, 0, 0, time.UTC),
			TimePrecision: "day", Category: "landslide", Trigger: "持续降雨与海岸侵蚀",
			Location:         spatial.Point{Longitude: -121.652, Latitude: 36.080},
			LocationAccuracy: "approximate event centroid", Country: "United States",
			AdminArea: "California, Monterey County", Fatalities: 0, Injuries: 0,
			Source: historicalSource(fetchedAt,
				"https://www.usgs.gov/special-topics/big-sur-landslides/science/mud-creek-landslide-may-20-2017",
				"USGS The Mud Creek Landslide May 20 2017"),
			Limitations: []string{"historical_replay", "零伤亡表示公开页面未报告伤亡，不等于现场绝对无伤", "位置为事件中心近似值"},
		},
	}
}

// ScenarioForEvent 返回与公开历史事件关联的匿名回放场景。
func (c *SurvivalCatalog) ScenarioForEvent(ctx context.Context, eventID string) (survival.Scenario, error) {
	if err := ctx.Err(); err != nil {
		return survival.Scenario{}, fmt.Errorf("读取历史事件 %s 回放场景: %w", eventID, err)
	}
	if c == nil {
		return survival.Scenario{}, fmt.Errorf("%w: 历史事件目录为空", domain.ErrInvalidInput)
	}
	value := "replay-" + strings.TrimPrefix(eventID, "case-")
	return c.GetScenario(ctx, value)
}

func historicalSource(fetchedAt time.Time, uri, citation string) provenance.Provenance {
	return provenance.Provenance{
		Provider: "USGS", Dataset: "公开滑坡灾害科学页面", SourceURI: uri,
		Citation: citation, DataKind: provenance.DataKindHistorical,
		FetchedAt: fetchedAt, RevisionFirstSeenAt: fetchedAt,
		TemporalResolution: "event-level", CRS: string(spatial.CoordinateWGS84),
	}
}

func defaultScenarios() map[string]survival.Scenario {
	return map[string]survival.Scenario{
		"replay-oso-2014": {
			ID: "replay-oso-2014", AsOf: time.Date(2014, 3, 22, 2, 0, 0, 0, time.UTC),
			ElapsedMinutes: 90, InputCompleteness: 0.85, Synthetic: true,
			Environment: map[string]string{"air_pocket": "yes", "water_available": "unknown", "hazard_stable": "no"},
			Entrapment:  map[string]string{"communication": "unknown", "injury": "unknown"},
		},
		"replay-montecito-2018": {
			ID: "replay-montecito-2018", AsOf: time.Date(2018, 1, 9, 5, 0, 0, 0, time.UTC),
			ElapsedMinutes: 300, InputCompleteness: 0.70, Synthetic: true,
			Environment: map[string]string{"air_pocket": "unknown", "water_available": "no", "hazard_stable": "no"},
			Entrapment:  map[string]string{"communication": "unknown", "injury": "severe"},
		},
		"replay-mud-creek-2017": {
			ID: "replay-mud-creek-2017", AsOf: time.Date(2017, 5, 20, 4, 0, 0, 0, time.UTC),
			ElapsedMinutes: 30, InputCompleteness: 0.90, Synthetic: true,
			Environment: map[string]string{"air_pocket": "yes", "water_available": "yes", "hazard_stable": "yes"},
			Entrapment:  map[string]string{"communication": "yes", "injury": "unknown"},
		},
	}
}
