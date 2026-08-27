// Package mapcoord 提供地图供应商边界的 WGS84/GCJ-02 坐标转换。
package mapcoord

import (
	"fmt"
	"math"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const (
	semiMajorAxis  = 6378245.0
	eccentricity   = 0.00669342162296594323
	minChinaLon    = 72.004
	maxChinaLon    = 137.8347
	minChinaLat    = 0.8293
	maxChinaLat    = 55.8271
	maxBatchSize   = 10000
	inverseLoops   = 12
	inverseEpsilon = 1e-7
)

var (
	// ErrUnsupportedCoordinateSystem 表示调用方传入了未支持的参考系。
	ErrUnsupportedCoordinateSystem = fmt.Errorf("%w: 不支持的坐标参考系", domain.ErrInvalidInput)
)

// Transformer 是无状态、线程安全的 GCJ-02 转换器。
type Transformer struct{}

// New 返回坐标转换器。转换器不持有网络、数据库或供应商客户端。
func New() Transformer { return Transformer{} }

// Convert 转换单个坐标。中国境外点保持原值并标记为未转换。
func (Transformer) Convert(point spatial.Point, from, to spatial.CoordinateSystem) (
	spatial.CoordinateConversion, error,
) {
	if err := point.Validate(); err != nil {
		return spatial.CoordinateConversion{}, err
	}
	if !supported(from) || !supported(to) {
		return spatial.CoordinateConversion{}, ErrUnsupportedCoordinateSystem
	}
	if from == to {
		return conversion(point, from, to, false, nil), nil
	}
	if !inChina(point) {
		return conversion(point, from, to, false, []string{
			"坐标位于中国境外，未执行 GCJ-02 偏移；领域和数据库继续使用原坐标",
		}), nil
	}

	converted := point
	switch {
	case from == spatial.CoordinateWGS84 && to == spatial.CoordinateGCJ02:
		converted = wgs84ToGCJ02(point)
	case from == spatial.CoordinateGCJ02 && to == spatial.CoordinateWGS84:
		converted = gcj02ToWGS84(point)
	default:
		return spatial.CoordinateConversion{}, ErrUnsupportedCoordinateSystem
	}
	if err := converted.Validate(); err != nil {
		return spatial.CoordinateConversion{}, fmt.Errorf("转换后坐标无效: %w", err)
	}
	return conversion(converted, from, to, true, []string{
		"GCJ-02 是受限的经验偏移坐标系，反向转换为近似值，不应作为高精度测量结果",
	}), nil
}

// ConvertBatch 按输入顺序转换坐标，并在校验或转换失败时返回空结果。
func (t Transformer) ConvertBatch(points []spatial.Point, from, to spatial.CoordinateSystem) (
	[]spatial.CoordinateConversion, error,
) {
	if len(points) > maxBatchSize {
		return nil, fmt.Errorf("%w: 单批坐标最多 %d 个", domain.ErrInvalidInput, maxBatchSize)
	}
	result := make([]spatial.CoordinateConversion, len(points))
	for index, point := range points {
		value, err := t.Convert(point, from, to)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个坐标转换失败: %w", index, err)
		}
		result[index] = value
	}
	return result, nil
}

func conversion(point spatial.Point, from, to spatial.CoordinateSystem,
	transformed bool, limitations []string,
) spatial.CoordinateConversion {
	return spatial.CoordinateConversion{
		Point: point, From: from, To: to, Transformed: transformed,
		Limitations: limitations,
	}
}

func supported(value spatial.CoordinateSystem) bool {
	return value == spatial.CoordinateWGS84 || value == spatial.CoordinateGCJ02
}

func inChina(point spatial.Point) bool {
	return point.Longitude >= minChinaLon && point.Longitude <= maxChinaLon &&
		point.Latitude >= minChinaLat && point.Latitude <= maxChinaLat
}

func wgs84ToGCJ02(point spatial.Point) spatial.Point {
	dLat := transformLat(point.Longitude-105, point.Latitude-35)
	dLon := transformLon(point.Longitude-105, point.Latitude-35)
	radLat := point.Latitude / 180 * math.Pi
	magic := 1 - eccentricity*math.Sin(radLat)*math.Sin(radLat)
	sqrtMagic := math.Sqrt(magic)
	dLat = dLat * 180 / ((semiMajorAxis * (1 - eccentricity)) / (magic * sqrtMagic) * math.Pi)
	dLon = dLon * 180 / (semiMajorAxis / sqrtMagic * math.Cos(radLat) * math.Pi)
	return spatial.Point{Longitude: point.Longitude + dLon, Latitude: point.Latitude + dLat}
}

func gcj02ToWGS84(point spatial.Point) spatial.Point {
	guess := point
	for index := 0; index < inverseLoops; index++ {
		converted := wgs84ToGCJ02(guess)
		dLon := converted.Longitude - point.Longitude
		dLat := converted.Latitude - point.Latitude
		guess.Longitude -= dLon
		guess.Latitude -= dLat
		if math.Abs(dLon) < inverseEpsilon && math.Abs(dLat) < inverseEpsilon {
			break
		}
	}
	return guess
}

func transformLat(x, y float64) float64 {
	value := -100 + 2*x + 3*y + 0.2*y*y + 0.1*x*y + 0.2*math.Sqrt(math.Abs(x))
	value += (20*math.Sin(6*x*math.Pi) + 20*math.Sin(2*x*math.Pi)) * 2 / 3
	value += (20*math.Sin(y*math.Pi) + 40*math.Sin(y/3*math.Pi)) * 2 / 3
	value += (160*math.Sin(y/12*math.Pi) + 320*math.Sin(y*math.Pi/30)) * 2 / 3
	return value
}

func transformLon(x, y float64) float64 {
	value := 300 + x + 2*y + 0.1*x*x + 0.1*x*y + 0.1*math.Sqrt(math.Abs(x))
	value += (20*math.Sin(6*x*math.Pi) + 20*math.Sin(2*x*math.Pi)) * 2 / 3
	value += (20*math.Sin(x*math.Pi) + 40*math.Sin(x/3*math.Pi)) * 2 / 3
	value += (150*math.Sin(x/12*math.Pi) + 300*math.Sin(x/30*math.Pi)) * 2 / 3
	return value
}
