// Package exposurecollection 编排真实人口、道路和应急设施暴露采集。
package exposurecollection

import (
	"context"
	"encoding/json"
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const (
	MaxRiskZones          = 500
	MaxUnionGeometryBytes = 1 << 20
	MaxInfrastructure     = 900
	MaxProviderReferences = 64
	MaxFeatureGeometry    = 256 << 10
	MaxFeaturePoints      = 10_000
	MaxTotalFeaturePoints = 250_000
)

// Bounds 是 WGS84 风险区联合几何的外包矩形。
type Bounds struct {
	South float64
	West  float64
	North float64
	East  float64
}

// GeometryStats 是在物化 GeoJSON 前由 PostGIS 返回的安全预算。
type GeometryStats struct {
	ZoneCount          int
	UnionGeometryBytes int64
	MaxZonePoints      int64
	TotalZonePoints    int64
}

// GeometryInput 是一次真实暴露采集所需的无损空间输入。
type GeometryInput struct {
	Snapshot      hazard.Snapshot
	Zones         []applicationloss.LossRiskZone
	Analysis      applicationloss.LossSpatialProjection
	UnionGeometry json.RawMessage
	Bounds        Bounds
	Stats         GeometryStats
}

// AdministrativeBoundary 保存 geoBoundaries ADM0 的版本化真实边界。
type AdministrativeBoundary struct {
	BoundaryID      string
	RegionCode      string
	BoundaryType    string
	BoundaryYear    string
	Source          string
	License         string
	Digest          string
	Reference       string
	Geometry        json.RawMessage
	CollectedAt     time.Time
	InputReferences []string
}

// AdministrativeProjection 是风险区与真实 ADM0 边界精确相交后的投影。
type AdministrativeProjection struct {
	AnalysisID            string
	SnapshotID            string
	RegionCode            string
	BoundaryID            string
	BoundaryDigest        string
	BoundaryReference     string
	BoundaryGeometry      json.RawMessage
	UnionGeometry         json.RawMessage
	Bounds                Bounds
	TotalAreaSquareMeters float64
	Zones                 []applicationloss.LossRiskZone
}

// PopulationQuery 描述 WorldPop 人口汇总请求。
type PopulationQuery struct {
	Geometry                json.RawMessage
	ExpectedAreaSquareMeter float64
	Year                    int
}

// PopulationResult 保存 WorldPop 异步任务的真实结果和来源。
type PopulationResult struct {
	TaskID          string
	Total           float64
	AreaKM2         float64
	DataYear        int
	DataSource      string
	DatasetIdentity string
	CollectedAt     time.Time
	ValidFrom       time.Time
	ValidTo         time.Time
	InputReferences []string
	Limitations     []string
}

// InfrastructureQuery 描述一次受限 Overpass 查询。
type InfrastructureQuery struct {
	Bounds Bounds
}

// RawInfrastructureFeature 是带真实 OSM 标识和 WGS84 几何的供应商记录。
type RawInfrastructureFeature struct {
	FeatureID       string
	Kind            applicationloss.LossFeatureKind
	Geometry        json.RawMessage
	InputReferences []string
}

// InfrastructureResult 保存 OSM 数据版本和受限 feature 集合。
type InfrastructureResult struct {
	OSMBaseTimestamp time.Time
	CollectedAt      time.Time
	ValidFrom        time.Time
	ValidTo          time.Time
	InputReferences  []string
	Limitations      []string
	Features         []RawInfrastructureFeature
}

// GeometryProjectionLimits 限制 PostGIS 解析供应商几何的数量和复杂度。
type GeometryProjectionLimits struct {
	MaxFeatures      int
	MaxGeometryBytes int64
	MaxPointsPerItem int64
	MaxTotalPoints   int64
}

// ExposureProjection 是带共同有效窗口的内容寻址损失输入。
type ExposureProjection struct {
	Input     applicationloss.LossInputProjection
	ValidFrom time.Time
	ValidTo   time.Time
}

// GeometryInputReader 在物化联合几何前执行数量和字节预检。
type GeometryInputReader interface {
	ReadExposureGeometry(context.Context, string, string) (GeometryInput, error)
}

// AdministrativeBoundaryProvider 返回带版本和校验和的真实行政边界。
type AdministrativeBoundaryProvider interface {
	Boundary(context.Context) (AdministrativeBoundary, error)
}

// AdministrativeProjector 将原风险区精确裁剪到真实行政边界内。
type AdministrativeProjector interface {
	ProjectAdministration(context.Context, GeometryInput, AdministrativeBoundary,
		GeometryProjectionLimits) (AdministrativeProjection, error)
}

// PopulationProvider 返回指定联合面内的真实人口汇总。
type PopulationProvider interface {
	Population(context.Context, PopulationQuery) (PopulationResult, error)
}

// InfrastructureProvider 返回指定范围内的真实 OSM 道路和设施几何。
type InfrastructureProvider interface {
	Infrastructure(context.Context, InfrastructureQuery) (InfrastructureResult, error)
}

// InfrastructureProjector 通过 PostGIS 与风险区精确相交并全局去重。
type InfrastructureProjector interface {
	ProjectInfrastructure(context.Context, AdministrativeProjection, []RawInfrastructureFeature,
		GeometryProjectionLimits) ([]applicationloss.LossExposureFeature, error)
}

// ProjectionWriter 原子追加完整暴露投影，禁止修改已完成记录。
type ProjectionWriter interface {
	SaveExposureProjection(context.Context, ExposureProjection) error
}
