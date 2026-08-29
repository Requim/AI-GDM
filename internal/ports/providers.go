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
// 输入中心点和返回设施位置均使用领域层 WGS84；GCJ-02 转换只能由地图适配器内部完成。
type PlaceFinder interface {
	// FindNearby 按设施类型和半径查询候选点。
	FindNearby(ctx context.Context, center spatial.Point, kind evacuation.FacilityType, radiusM int) ([]evacuation.Facility, error)
}

// RoutePlanner 规划尚未经过风险区过滤的候选路线。
// 输入坐标和返回路线几何均使用领域层 WGS84；供应商坐标系不得泄漏到应用层。
type RoutePlanner interface {
	// Plan 返回指定交通方式的多个候选路线。
	Plan(ctx context.Context, origin, destination spatial.Point, mode evacuation.TravelMode) ([]evacuation.Route, error)
}

// TransitRoutePlanner 规划需要起终点城市编码的公交候选路线。
// 与 RoutePlanner 分离，避免在通用路线端口中引入公交专属参数。
type TransitRoutePlanner interface {
	// PlanTransit 使用高德 citycode 规划公交路线，坐标和几何均为 WGS84。
	PlanTransit(ctx context.Context, origin, destination spatial.Point, city1, city2 string) ([]evacuation.Route, error)
}

// EvidenceSearcher 搜索最新公开灾害证据。
type EvidenceSearcher interface {
	// Search 必须响应 ctx；成功返回后，结果及其嵌套切片的所有权移交调用方，适配器不得继续修改。
	Search(ctx context.Context, query string, limit int) ([]report.Evidence, error)
}

// NarrativeGenerator 根据不可变的结构化结论生成中文说明。
type NarrativeGenerator interface {
	// Generate 必须响应 ctx，不得保留并异步修改 input；成功返回后输出所有权移交调用方，适配器不得继续修改。
	// Generate 只能解释输入，不得修改风险、路线、金额或搜救评分。
	Generate(ctx context.Context, input report.NarrativeInput) (report.Narrative, error)
}

// Clock 为确定性测试提供当前时间。
type Clock interface {
	// Now 返回 UTC 当前时间。
	Now() time.Time
}
