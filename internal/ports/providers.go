package ports

import (
	"context"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// ArtifactDiscovery 发现供应商当前最新的数据制品。
type ArtifactDiscovery interface {
	// DiscoverLatest 返回最新制品的远端描述。
	DiscoverLatest(ctx context.Context) (provenance.Artifact, error)
}

// ArtifactFetcher 下载并校验外部数据制品。
type ArtifactFetcher interface {
	// Fetch 将远端制品下载到受控的本地缓存目录。
	Fetch(ctx context.Context, artifact provenance.Artifact) (provenance.Artifact, error)
}

// RasterProcessor 把风险栅格转换为领域快照和风险区。
type RasterProcessor interface {
	// ModelName 返回处理结果使用的稳定模型名称。
	ModelName() string
	// Version 返回处理算法和固定参数的稳定版本。
	Version() string
	// Process 执行固定参数的裁剪、分级和矢量化。
	Process(ctx context.Context, artifact provenance.Artifact) (hazard.Snapshot, []hazard.RiskZone, error)
}

// WeatherReader 读取指定坐标的数值天气模型结果。
type WeatherReader interface {
	// Forecast 返回指定时间窗口的逐小时天气序列。
	Forecast(ctx context.Context, points []spatial.Point, pastHours, forecastHours int) ([]hazard.WeatherSnapshot, error)
}

// PlaceFinder 搜索避险相关的候选设施。
type PlaceFinder interface {
	// FindNearby 按设施类型和半径查询候选点。
	FindNearby(ctx context.Context, center spatial.Point, kind evacuation.FacilityType, radiusM int) ([]evacuation.Facility, error)
}

// RoutePlanner 规划尚未经过风险区过滤的候选路线。
type RoutePlanner interface {
	// Plan 返回指定交通方式的多个候选路线。
	Plan(ctx context.Context, origin, destination spatial.Point, mode evacuation.TravelMode) ([]evacuation.Route, error)
}

// EvidenceSearcher 搜索最新公开灾害证据。
type EvidenceSearcher interface {
	// Search 返回去重且保留来源的搜索结果。
	Search(ctx context.Context, query string, limit int) ([]report.Evidence, error)
}

// NarrativeGenerator 根据不可变的结构化结论生成中文说明。
type NarrativeGenerator interface {
	// Generate 不得修改输入中的风险、路线、金额或搜救评分。
	Generate(ctx context.Context, input report.NarrativeInput) (report.Narrative, error)
}

// Clock 为确定性测试提供当前时间。
type Clock interface {
	// Now 返回 UTC 当前时间。
	Now() time.Time
}
