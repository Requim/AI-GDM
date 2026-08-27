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

// ContainsPoint 判断 WGS84 点是否落在 Polygon 或 MultiPolygon 内，边界按命中处理。
// 洞环内部视为非风险区，但洞环边界仍按风险区边界处理。
func (g Geometry) ContainsPoint(point Point) (bool, error) {
	if err := point.Validate(); err != nil {
		return false, err
	}
	if err := g.ValidateArea(); err != nil {
		return false, err
	}
	switch g.Type {
	case "Polygon":
		var coordinates [][][]float64
		if err := decodePolygonCoordinates(g.Coordinates, &coordinates); err != nil {
			return false, err
		}
		return polygonContainsPoint(coordinates, point)
	case "MultiPolygon":
		var coordinates [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &coordinates); err != nil {
			return false, fmt.Errorf("%w: MultiPolygon 坐标无效: %v", domain.ErrInvalidInput, err)
		}
		if len(coordinates) == 0 {
			return false, fmt.Errorf("%w: MultiPolygon 不含多边形", domain.ErrInvalidInput)
		}
		inside := false
		for _, polygon := range coordinates {
			matched, err := polygonContainsPoint(polygon, point)
			if err != nil {
				return false, err
			}
			inside = inside || matched
		}
		return inside, nil
	default:
		return false, fmt.Errorf("%w: 仅支持 Polygon 或 MultiPolygon 点包含判断", domain.ErrInvalidInput)
	}
}

// ValidateArea 校验可用于风险区点包含判断的 Polygon 或 MultiPolygon。
// 与通用 Geometry.Validate 不同，它会解析并校验每一个环，避免空候选时漏过坏几何。
func (g Geometry) ValidateArea() error {
	if err := g.Validate(); err != nil {
		return err
	}
	switch g.Type {
	case "Polygon":
		var coordinates [][][]float64
		if err := decodePolygonCoordinates(g.Coordinates, &coordinates); err != nil {
			return err
		}
	case "MultiPolygon":
		var coordinates [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &coordinates); err != nil {
			return fmt.Errorf("%w: MultiPolygon 坐标无效: %v", domain.ErrInvalidInput, err)
		}
		if len(coordinates) == 0 {
			return fmt.Errorf("%w: MultiPolygon 不含多边形", domain.ErrInvalidInput)
		}
		for _, polygon := range coordinates {
			if err := validatePolygonCoordinates(polygon); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: 仅支持 Polygon 或 MultiPolygon 面几何", domain.ErrInvalidInput)
	}
	return nil
}

func decodePolygonCoordinates(raw json.RawMessage, value *[][][]float64) error {
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%w: Polygon 坐标无效: %v", domain.ErrInvalidInput, err)
	}
	if err := validatePolygonCoordinates(*value); err != nil {
		return err
	}
	return nil
}

func validatePolygonCoordinates(value [][][]float64) error {
	if len(value) == 0 {
		return fmt.Errorf("%w: Polygon 不含环", domain.ErrInvalidInput)
	}
	for _, ring := range value {
		if err := validateRingCoordinates(ring); err != nil {
			return err
		}
	}
	return nil
}

func validateRingCoordinates(ring [][]float64) error {
	if len(ring) < 4 || !sameCoordinate(ring[0], ring[len(ring)-1]) {
		return fmt.Errorf("%w: Polygon 环未闭合或点数不足", domain.ErrInvalidInput)
	}
	area := 0.0
	for index := 0; index < len(ring)-1; index++ {
		if len(ring[index]) < 2 || !validCoordinate(ring[index]) {
			return fmt.Errorf("%w: Polygon 坐标超出 WGS84 范围", domain.ErrInvalidInput)
		}
		area += ring[index][0]*ring[index+1][1] - ring[index+1][0]*ring[index][1]
	}
	if !validCoordinate(ring[len(ring)-1]) || math.Abs(area) < 1e-15 {
		return fmt.Errorf("%w: Polygon 环无效或面积为零", domain.ErrInvalidInput)
	}
	return nil
}

func validCoordinate(value []float64) bool {
	return len(value) >= 2 && finiteCoordinate(value[0]) && finiteCoordinate(value[1]) &&
		value[0] >= -180 && value[0] <= 180 && value[1] >= -90 && value[1] <= 90
}

func finiteCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sameCoordinate(left, right []float64) bool {
	return len(left) >= 2 && len(right) >= 2 && left[0] == right[0] && left[1] == right[1]
}

func polygonContainsPoint(polygon [][][]float64, point Point) (bool, error) {
	if err := validatePolygonCoordinates(polygon); err != nil {
		return false, err
	}
	outer, boundary := ringContainsPoint(polygon[0], point)
	if boundary {
		return true, nil
	}
	if !outer {
		return false, nil
	}
	for _, hole := range polygon[1:] {
		inside, boundary := ringContainsPoint(hole, point)
		if boundary {
			return true, nil
		}
		if inside {
			return false, nil
		}
	}
	return true, nil
}

func ringContainsPoint(ring [][]float64, point Point) (inside, boundary bool) {
	for index := 0; index < len(ring)-1; index++ {
		if pointOnSegment(point, ring[index], ring[index+1]) {
			return true, true
		}
	}
	for index, next := 0, len(ring)-1; index < len(ring); next, index = index, index+1 {
		left, right := ring[index], ring[next]
		crosses := (left[1] > point.Latitude) != (right[1] > point.Latitude)
		if crosses && point.Longitude < (right[0]-left[0])*(point.Latitude-left[1])/(right[1]-left[1])+left[0] {
			inside = !inside
		}
	}
	return inside, false
}

func pointOnSegment(point Point, left, right []float64) bool {
	cross := (point.Longitude-left[0])*(right[1]-left[1]) -
		(point.Latitude-left[1])*(right[0]-left[0])
	if math.Abs(cross) > 1e-12 {
		return false
	}
	return point.Longitude >= math.Min(left[0], right[0])-1e-12 &&
		point.Longitude <= math.Max(left[0], right[0])+1e-12 &&
		point.Latitude >= math.Min(left[1], right[1])-1e-12 &&
		point.Latitude <= math.Max(left[1], right[1])+1e-12
}
