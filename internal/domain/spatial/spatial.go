package spatial

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/Requim/AI-GDM/internal/domain"
)

// Point 表示 WGS84 经纬度坐标。
type Point struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// CoordinateSystem 表示地图适配器支持的坐标参考系。
type CoordinateSystem string

const (
	// CoordinateWGS84 是数据库和领域模型统一使用的坐标系。
	CoordinateWGS84 CoordinateSystem = "WGS84"
	// CoordinateGCJ02 是中国互联网地图常用的火星坐标系。
	CoordinateGCJ02 CoordinateSystem = "GCJ-02"
)

// CoordinateConversion 保存一次坐标转换及其可审计限制。
type CoordinateConversion struct {
	Point       Point            `json:"point"`
	From        CoordinateSystem `json:"from"`
	To          CoordinateSystem `json:"to"`
	Transformed bool             `json:"transformed"`
	Limitations []string         `json:"limitations,omitempty"`
}

// Validate 校验 WGS84 经纬度范围。
func (p Point) Validate() error {
	if math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) ||
		math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) {
		return fmt.Errorf("%w: 坐标必须是有限数值", domain.ErrInvalidInput)
	}
	if p.Longitude < -180 || p.Longitude > 180 || p.Latitude < -90 || p.Latitude > 90 {
		return fmt.Errorf("%w: 坐标超出 WGS84 范围", domain.ErrInvalidInput)
	}
	return nil
}

// Geometry 保存标准 GeoJSON Geometry，数据库坐标系固定为 WGS84。
type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// Validate 校验支持的几何类型和坐标 JSON。
func (g Geometry) Validate() error {
	switch g.Type {
	case "Point", "LineString", "Polygon", "MultiPolygon":
	default:
		return fmt.Errorf("%w: 不支持的几何类型 %q", domain.ErrInvalidInput, g.Type)
	}
	if len(g.Coordinates) == 0 || !json.Valid(g.Coordinates) {
		return fmt.Errorf("%w: 几何坐标不是有效 JSON", domain.ErrInvalidInput)
	}
	return nil
}
