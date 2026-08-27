package spatial

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/Requim/AI-GDM/internal/domain"
)

// IntersectsLineString 判断路线折线是否与风险面相交。
// 路线端点落在风险面内、穿过边界或贴着边界均按相交处理。
func (g Geometry) IntersectsLineString(line Geometry) (bool, error) {
	if err := g.ValidateArea(); err != nil {
		return false, err
	}
	coordinates, err := decodeLineCoordinates(line)
	if err != nil {
		return false, err
	}
	switch g.Type {
	case "Polygon":
		var polygon [][][]float64
		if err := decodePolygonCoordinates(g.Coordinates, &polygon); err != nil {
			return false, err
		}
		return polygonIntersectsLine(polygon, coordinates)
	case "MultiPolygon":
		var polygons [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polygons); err != nil {
			return false, fmt.Errorf("%w: MultiPolygon 坐标无效: %v", domain.ErrInvalidInput, err)
		}
		for _, polygon := range polygons {
			matched, err := polygonIntersectsLine(polygon, coordinates)
			if err != nil || matched {
				return matched, err
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: 仅支持 Polygon 或 MultiPolygon 路线相交判断", domain.ErrInvalidInput)
	}
}

// ValidateLineString 校验路线几何为至少含两个 WGS84 坐标点的 LineString。
func (g Geometry) ValidateLineString() error {
	_, err := decodeLineCoordinates(g)
	return err
}

func decodeLineCoordinates(line Geometry) ([][]float64, error) {
	if line.Type != "LineString" {
		return nil, fmt.Errorf("%w: 路线几何必须是 LineString", domain.ErrInvalidInput)
	}
	if err := line.Validate(); err != nil {
		return nil, err
	}
	var coordinates [][]float64
	if err := json.Unmarshal(line.Coordinates, &coordinates); err != nil {
		return nil, fmt.Errorf("%w: LineString 坐标无效: %v", domain.ErrInvalidInput, err)
	}
	if len(coordinates) < 2 {
		return nil, fmt.Errorf("%w: LineString 至少需要两个坐标点", domain.ErrInvalidInput)
	}
	for _, coordinate := range coordinates {
		if !validCoordinate(coordinate) {
			return nil, fmt.Errorf("%w: LineString 坐标超出 WGS84 范围", domain.ErrInvalidInput)
		}
	}
	return coordinates, nil
}

func polygonIntersectsLine(polygon [][][]float64, line [][]float64) (bool, error) {
	if err := validatePolygonCoordinates(polygon); err != nil {
		return false, err
	}
	for index := 0; index < len(line)-1; index++ {
		start := Point{Longitude: line[index][0], Latitude: line[index][1]}
		end := Point{Longitude: line[index+1][0], Latitude: line[index+1][1]}
		insideStart, err := polygonContainsPoint(polygon, start)
		if err != nil {
			return false, err
		}
		insideEnd, err := polygonContainsPoint(polygon, end)
		if err != nil {
			return false, err
		}
		if insideStart || insideEnd || lineTouchesRings(line[index], line[index+1], polygon) {
			return true, nil
		}
	}
	return false, nil
}

func lineTouchesRings(start, end []float64, polygon [][][]float64) bool {
	for _, ring := range polygon {
		for index := 0; index < len(ring)-1; index++ {
			if segmentsIntersect(start, end, ring[index], ring[index+1]) {
				return true
			}
		}
	}
	return false
}

func segmentsIntersect(firstStart, firstEnd, secondStart, secondEnd []float64) bool {
	first := orientation(firstStart, firstEnd, secondStart)
	second := orientation(firstStart, firstEnd, secondEnd)
	third := orientation(secondStart, secondEnd, firstStart)
	fourth := orientation(secondStart, secondEnd, firstEnd)
	if oppositeSigns(first, second) && oppositeSigns(third, fourth) {
		return true
	}
	return (nearZero(first) && pointOnSegmentValues(secondStart, firstStart, firstEnd)) ||
		(nearZero(second) && pointOnSegmentValues(secondEnd, firstStart, firstEnd)) ||
		(nearZero(third) && pointOnSegmentValues(firstStart, secondStart, secondEnd)) ||
		(nearZero(fourth) && pointOnSegmentValues(firstEnd, secondStart, secondEnd))
}

func orientation(start, end, point []float64) float64 {
	return (end[0]-start[0])*(point[1]-start[1]) -
		(end[1]-start[1])*(point[0]-start[0])
}

func oppositeSigns(left, right float64) bool {
	return (left > 1e-12 && right < -1e-12) || (left < -1e-12 && right > 1e-12)
}

func nearZero(value float64) bool {
	return math.Abs(value) <= 1e-12
}

func pointOnSegmentValues(point, start, end []float64) bool {
	return point[0] >= math.Min(start[0], end[0])-1e-12 &&
		point[0] <= math.Max(start[0], end[0])+1e-12 &&
		point[1] >= math.Min(start[1], end[1])-1e-12 &&
		point[1] <= math.Max(start[1], end[1])+1e-12
}
