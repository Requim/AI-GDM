package spatialanalysis

import "time"

const (
	// AnalysisVersion 标识当前空间分析结果结构与确定性规则版本。
	AnalysisVersion = "ai-gdm-spatial-analysis-v1"
	// AreaMethod 标识面积由 PostGIS geography 椭球方法计算。
	AreaMethod = "postgis-geography-spheroid-v1"

	// PopulationUnit 是人口暴露量的固定单位。
	PopulationUnit = "people"
	// RoadUnit 是道路暴露量的固定单位。
	RoadUnit = "meters"
	// POIUnit 是 POI 暴露量的固定单位。
	POIUnit = "count"
)

// MetricStatus 表示单项暴露数据的覆盖和可用状态。
type MetricStatus string

const (
	// MetricAvailable 表示数据完整覆盖风险区。
	MetricAvailable MetricStatus = "available"
	// MetricPartial 表示数据只覆盖风险区的一部分。
	MetricPartial MetricStatus = "partial"
	// MetricUnavailable 表示缺少可用于计算的真实数据。
	MetricUnavailable MetricStatus = "unavailable"
)

// AnalysisStatus 表示一次空间分析的整体数据完备程度。
type AnalysisStatus string

const (
	// AnalysisAvailable 表示面积、暴露和行政匹配均完整可用。
	AnalysisAvailable AnalysisStatus = "available"
	// AnalysisPartial 表示面积可用，但至少一个上下文结果不完整。
	AnalysisPartial AnalysisStatus = "partial"
	// AnalysisAreaOnly 表示仅有面积能力，也用于已完成且没有风险区的空分析。
	AnalysisAreaOnly AnalysisStatus = "area_only"
)

// AdminMatchStatus 表示风险区行政区匹配的覆盖状态。
type AdminMatchStatus string

const (
	// AdminMatchAvailable 表示行政边界完整覆盖风险区。
	AdminMatchAvailable AdminMatchStatus = "available"
	// AdminMatchPartial 表示行政边界只覆盖风险区的一部分。
	AdminMatchPartial AdminMatchStatus = "partial"
	// AdminMatchUnavailable 表示缺少可追溯的行政边界数据。
	AdminMatchUnavailable AdminMatchStatus = "unavailable"
)

// PopulationExposureMetric 保存风险区内人口暴露结果。
type PopulationExposureMetric struct {
	Status          MetricStatus `json:"status"`
	Quantity        *float64     `json:"quantity"`
	Unit            string       `json:"unit"`
	CoverageRatio   *float64     `json:"coverageRatio"`
	InputReferences []string     `json:"inputReferences"`
	Limitations     []string     `json:"limitations"`
}

// RoadExposureMetric 保存风险区内道路长度暴露结果。
type RoadExposureMetric struct {
	Status          MetricStatus `json:"status"`
	Quantity        *float64     `json:"quantity"`
	Unit            string       `json:"unit"`
	CoverageRatio   *float64     `json:"coverageRatio"`
	InputReferences []string     `json:"inputReferences"`
	Limitations     []string     `json:"limitations"`
}

// POIExposureMetric 保存风险区内 POI 数量暴露结果。
type POIExposureMetric struct {
	Status          MetricStatus `json:"status"`
	Quantity        *float64     `json:"quantity"`
	Unit            string       `json:"unit"`
	CoverageRatio   *float64     `json:"coverageRatio"`
	InputReferences []string     `json:"inputReferences"`
	Limitations     []string     `json:"limitations"`
}

// AdministrativeMatch 保存风险区与行政边界的匹配结果。
type AdministrativeMatch struct {
	Status          AdminMatchStatus `json:"status"`
	AdminCodes      []string         `json:"adminCodes"`
	CoverageRatio   *float64         `json:"coverageRatio"`
	InputReferences []string         `json:"inputReferences"`
	Limitations     []string         `json:"limitations"`
}

// ZoneArea 保存单个风险区的真实地表面积。
type ZoneArea struct {
	SquareMeters    float64  `json:"squareMeters"`
	InputReferences []string `json:"inputReferences"`
}

// AreaCalculation 保存合并风险区后的总体面积及计算方法。
type AreaCalculation struct {
	Method            string   `json:"method"`
	TotalSquareMeters float64  `json:"totalSquareMeters"`
	InputReferences   []string `json:"inputReferences"`
}

// ZoneResult 保存单个风险区的面积、暴露和行政匹配结果。
type ZoneResult struct {
	ZoneID         string                   `json:"zoneId"`
	Area           ZoneArea                 `json:"area"`
	Population     PopulationExposureMetric `json:"populationExposure"`
	Roads          RoadExposureMetric       `json:"roadExposure"`
	POIs           POIExposureMetric        `json:"poiExposure"`
	Administration AdministrativeMatch      `json:"administrativeMatch"`
	Limitations    []string                 `json:"limitations"`
}

// AnalysisInput 保存构造一次空间分析所需的显式输入。
type AnalysisInput struct {
	SnapshotID        string          `json:"snapshotId"`
	Area              AreaCalculation `json:"area"`
	Zones             []ZoneResult    `json:"zones"`
	CalculatedAt      time.Time       `json:"calculatedAt"`
	InputReferences   []string        `json:"inputReferences"`
	DatasetReferences []string        `json:"datasetReferences"`
	Limitations       []string        `json:"limitations"`
}

// Analysis 保存规范化、可审计且可重放的空间分析结果。
type Analysis struct {
	ID                string          `json:"id"`
	SnapshotID        string          `json:"snapshotId"`
	Version           string          `json:"analysisVersion"`
	Status            AnalysisStatus  `json:"status"`
	Area              AreaCalculation `json:"area"`
	Zones             []ZoneResult    `json:"zones"`
	CalculatedAt      time.Time       `json:"calculatedAt"`
	InputReferences   []string        `json:"inputReferences"`
	DatasetReferences []string        `json:"datasetReferences"`
	Limitations       []string        `json:"limitations"`
}
