package hazard

import (
	"fmt"
	"math"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// Type 标识灾种。
type Type string

const (
	TypeLandslide  Type = "landslide"
	TypeDebrisFlow Type = "debris_flow"
	TypeEarthquake Type = "earthquake"
	TypeFlood      Type = "flood"
)

// RiskLevel 表示本系统的辅助研判等级，不是官方预警等级。
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskModerate RiskLevel = "moderate"
	RiskHigh     RiskLevel = "high"
	RiskVeryHigh RiskLevel = "very_high"
)

// SnapshotStatus 表示风险快照的处理状态。
type SnapshotStatus string

const (
	SnapshotPending   SnapshotStatus = "pending"
	SnapshotAvailable SnapshotStatus = "available"
	SnapshotStale     SnapshotStatus = "stale"
	SnapshotFailed    SnapshotStatus = "failed"
)

// RiskThreshold 将模型概率映射为辅助研判等级。
type RiskThreshold struct {
	Level       RiskLevel `json:"level"`
	Minimum     float64   `json:"minimum"`
	Maximum     float64   `json:"maximum"`
	Description string    `json:"description"`
}

// Snapshot 保存一次灾害模型运行及其限制。
type Snapshot struct {
	ID                   string                `json:"id"`
	HazardType           Type                  `json:"hazardType"`
	ModelName            string                `json:"modelName"`
	ModelVersion         string                `json:"modelVersion"`
	RunAt                time.Time             `json:"runAt"`
	ValidFrom            time.Time             `json:"validFrom"`
	ValidTo              time.Time             `json:"validTo"`
	RasterReference      string                `json:"rasterReference"`
	ProbabilitySemantics string                `json:"probabilitySemantics"`
	Thresholds           []RiskThreshold       `json:"thresholds"`
	Status               SnapshotStatus        `json:"status"`
	Source               provenance.Provenance `json:"source"`
	Limitations          []string              `json:"limitations"`
}

// RiskZone 表示由模型概率阈值生成的辅助研判区域。
type RiskZone struct {
	ID              string           `json:"id"`
	SnapshotID      string           `json:"snapshotId"`
	Geometry        spatial.Geometry `json:"geometry"`
	Minimum         float64          `json:"probabilityMinimum"`
	Mean            float64          `json:"probabilityMean"`
	Maximum         float64          `json:"probabilityMaximum"`
	Level           RiskLevel        `json:"riskLevel"`
	AreaSquareM     float64          `json:"areaSquareMeters"`
	AreaCalculated  bool             `json:"areaCalculated"`
	AdminCodes      []string         `json:"adminCodes,omitempty"`
	InputReferences []string         `json:"inputReferences"`
	Limitations     []string         `json:"limitations"`
}

// WeatherPoint 保存某一时刻的数值天气模型结果。
type WeatherPoint struct {
	Time                time.Time `json:"time"`
	PrecipitationMM     float64   `json:"precipitationMm"`
	RainMM              float64   `json:"rainMm"`
	ShowersMM           float64   `json:"showersMm"`
	SoilMoistureByLayer []float64 `json:"soilMoistureByLayer"`
}

// WeatherSnapshot 保存某坐标的天气时间序列。
type WeatherSnapshot struct {
	Location spatial.Point         `json:"location"`
	Hourly   []WeatherPoint        `json:"hourly"`
	Source   provenance.Provenance `json:"source"`
}

// ValidateThresholds 校验概率阈值有序、连续且位于零到一之间。
func ValidateThresholds(values []RiskThreshold) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: 风险阈值不能为空", domain.ErrInvalidInput)
	}
	if !sameProbability(values[0].Minimum, 0) || !sameProbability(values[len(values)-1].Maximum, 1) {
		return fmt.Errorf("%w: 概率阈值必须完整覆盖零到一", domain.ErrInvalidInput)
	}
	previous := 0.0
	for index, value := range values {
		if value.Minimum < 0 || value.Maximum > 1 || value.Minimum >= value.Maximum {
			return fmt.Errorf("%w: 第 %d 个概率阈值无效", domain.ErrInvalidInput, index)
		}
		if index > 0 && !sameProbability(value.Minimum, previous) {
			return fmt.Errorf("%w: 概率阈值不连续", domain.ErrInvalidInput)
		}
		previous = value.Maximum
	}
	return nil
}

func sameProbability(left, right float64) bool {
	return math.Abs(left-right) < 1e-9
}
