package gdal

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

type geometryEnvelope struct {
	west  float64
	south float64
	east  float64
	north float64
}

const boundaryBBoxTolerance = 1e-9

func validateBoundaryForBBox(geometry spatial.Geometry, bbox [4]float64) error {
	envelope, err := boundaryEnvelope(geometry)
	if err != nil {
		return err
	}
	if envelope.west < bbox[0]-boundaryBBoxTolerance ||
		envelope.east > bbox[2]+boundaryBBoxTolerance ||
		envelope.south < bbox[1]-boundaryBBoxTolerance ||
		envelope.north > bbox[3]+boundaryBBoxTolerance {
		return fmt.Errorf("%w: 行政边界包络未被 GDAL 下载范围完整包含", domain.ErrInvalidInput)
	}
	return nil
}

func boundaryEnvelope(geometry spatial.Geometry) (geometryEnvelope, error) {
	polygons, err := boundaryPolygons(geometry)
	if err != nil {
		return geometryEnvelope{}, err
	}
	value := geometryEnvelope{west: 180, south: 90, east: -180, north: -90}
	for _, polygon := range polygons {
		for _, ring := range polygon {
			value.extendRing(ring)
		}
	}
	return value, nil
}

func boundaryPolygons(geometry spatial.Geometry) ([][][][]float64, error) {
	if geometry.Type == "Polygon" {
		var polygon [][][]float64
		if err := json.Unmarshal(geometry.Coordinates, &polygon); err != nil {
			return nil, fmt.Errorf("解析行政边界 Polygon: %w", err)
		}
		return [][][][]float64{polygon}, nil
	}
	var polygons [][][][]float64
	if err := json.Unmarshal(geometry.Coordinates, &polygons); err != nil {
		return nil, fmt.Errorf("解析行政边界 MultiPolygon: %w", err)
	}
	return polygons, nil
}

func (e *geometryEnvelope) extendRing(ring [][]float64) {
	for _, point := range ring {
		e.west = math.Min(e.west, point[0])
		e.south = math.Min(e.south, point[1])
		e.east = math.Max(e.east, point[0])
		e.north = math.Max(e.north, point[1])
	}
}
