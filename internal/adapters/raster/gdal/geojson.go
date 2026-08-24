package gdal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type feature struct {
	Geometry spatial.Geometry `json:"geometry"`
	Property struct {
		Level int     `json:"level"`
		Min   float64 `json:"min"`
		Mean  float64 `json:"mean"`
		Max   float64 `json:"max"`
	} `json:"properties"`
}

func readFeatures(path string, maxBytes int64, maxCount int) ([]feature, error) {
	if err := validateFileSize(path, maxBytes); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 GDAL GeoJSON: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("读取 GDAL GeoJSON: %w", err)
	}
	return decodeFeatures(payload, maxCount)
}

func decodeFeatures(payload []byte, maxCount int) ([]feature, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var collection featureCollection
	if err := decoder.Decode(&collection); err != nil {
		return nil, fmt.Errorf("%w: 解析 GDAL GeoJSON: %v", domain.ErrInvalidInput, err)
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) > maxCount {
		return nil, fmt.Errorf("%w: GDAL GeoJSON 类型或要素数量无效", domain.ErrInvalidInput)
	}
	for index := range collection.Features {
		if err := validateFeature(collection.Features[index]); err != nil {
			return nil, fmt.Errorf("第 %d 个风险区: %w", index, err)
		}
	}
	return collection.Features, nil
}

func validateFeature(value feature) error {
	if value.Property.Level < 1 || value.Property.Level > 3 {
		return fmt.Errorf("%w: 风险分类值无效", domain.ErrInvalidInput)
	}
	if !validStatistics(value.Property.Level, value.Property.Min, value.Property.Mean, value.Property.Max) {
		return fmt.Errorf("%w: 分区概率统计无效", domain.ErrInvalidInput)
	}
	return validateGeometry(value.Geometry)
}

func riskLevel(value int) hazard.RiskLevel {
	return []hazard.RiskLevel{hazard.RiskModerate, hazard.RiskHigh, hazard.RiskVeryHigh}[value-1]
}

func validStatistics(level int, minimum, mean, maximum float64) bool {
	if minimum < 0 || maximum > 1 || minimum > mean || mean > maximum {
		return false
	}
	thresholds := [][2]float64{{0.1, 0.5}, {0.5, 0.9}, {0.9, 1}}
	value := thresholds[level-1]
	return minimum > value[0] && maximum <= value[1]
}

func validateGeometry(value spatial.Geometry) error {
	if value.Type == "Polygon" {
		var coordinates [][][]float64
		if err := json.Unmarshal(value.Coordinates, &coordinates); err != nil {
			return fmt.Errorf("%w: Polygon 坐标无效", domain.ErrInvalidInput)
		}
		return validatePolygon(coordinates)
	}
	if value.Type == "MultiPolygon" {
		var coordinates [][][][]float64
		if err := json.Unmarshal(value.Coordinates, &coordinates); err != nil || len(coordinates) == 0 {
			return fmt.Errorf("%w: MultiPolygon 坐标无效", domain.ErrInvalidInput)
		}
		for _, polygon := range coordinates {
			if err := validatePolygon(polygon); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: 风险区必须是 Polygon 或 MultiPolygon", domain.ErrInvalidInput)
}

func validatePolygon(value [][][]float64) error {
	if len(value) == 0 {
		return fmt.Errorf("%w: Polygon 不含环", domain.ErrInvalidInput)
	}
	for _, ring := range value {
		if err := validateRing(ring); err != nil {
			return err
		}
	}
	return nil
}

func validateRing(value [][]float64) error {
	if len(value) < 4 || !samePoint(value[0], value[len(value)-1]) {
		return fmt.Errorf("%w: Polygon 环未闭合或点数不足", domain.ErrInvalidInput)
	}
	area := 0.0
	for index := 0; index < len(value)-1; index++ {
		if !validCoordinate(value[index]) {
			return fmt.Errorf("%w: Polygon 坐标超出 WGS84 范围", domain.ErrInvalidInput)
		}
		area += value[index][0]*value[index+1][1] - value[index+1][0]*value[index][1]
	}
	if math.Abs(area) < 1e-15 {
		return fmt.Errorf("%w: Polygon 环面积为零", domain.ErrInvalidInput)
	}
	return nil
}

func validCoordinate(value []float64) bool {
	if len(value) < 2 || math.IsNaN(value[0]) || math.IsNaN(value[1]) || math.IsInf(value[0], 0) || math.IsInf(value[1], 0) {
		return false
	}
	return value[0] >= -180 && value[0] <= 180 && value[1] >= -90 && value[1] <= 90
}

func samePoint(left, right []float64) bool {
	return len(left) >= 2 && len(right) >= 2 && left[0] == right[0] && left[1] == right[1]
}

func zoneID(snapshotID string, value feature) string {
	payload := snapshotID + "|" + strconv.Itoa(value.Property.Level) + "|" + value.Geometry.Type + "|" + string(value.Geometry.Coordinates)
	digest := sha256.Sum256([]byte(payload))
	return snapshotID + "-zone-" + fmt.Sprintf("%x", digest[:8])
}
