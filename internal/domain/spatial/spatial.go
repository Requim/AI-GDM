package spatial

import (
	"encoding/json"
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain"
)

// Point 表示 WGS84 经纬度坐标。
type Point struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// Validate 校验 WGS84 经纬度范围。
func (p Point) Validate() error {
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
