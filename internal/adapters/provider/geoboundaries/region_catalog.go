package geoboundaries

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
)

type regionCollection struct {
	Type     string          `json:"type"`
	Features []regionFeature `json:"features"`
}

type regionFeature struct {
	Type       string           `json:"type"`
	Properties regionProperties `json:"properties"`
	Geometry   json.RawMessage  `json:"geometry"`
}

type regionProperties struct {
	ShapeID    string `json:"shapeID"`
	ShapeName  string `json:"shapeName"`
	ShapeISO   string `json:"shapeISO"`
	ShapeGroup string `json:"shapeGroup"`
	ShapeType  string `json:"shapeType"`
}

// DecodeRegionCollection 解析并校验真实 ADM1/ADM2 多要素边界集合。
func DecodeRegionCollection(payload []byte, countryISO, level string) ([]exposurecollection.AdministrativeRegion, error) {
	countryISO = strings.TrimSpace(countryISO)
	level = strings.TrimSpace(level)
	if len(payload) == 0 || countryISO == "" || level == "" {
		return nil, fmt.Errorf("%w: 行政区目录输入无效", domain.ErrInvalidInput)
	}
	var collection regionCollection
	if err := json.Unmarshal(payload, &collection); err != nil || collection.Type != "FeatureCollection" ||
		len(collection.Features) == 0 {
		return nil, fmt.Errorf("%w: 行政区目录不是有效多要素集合", domain.ErrProviderUnavailable)
	}
	records := make([]exposurecollection.AdministrativeRegion, 0, len(collection.Features))
	seen := make(map[string]struct{}, len(collection.Features))
	for _, feature := range collection.Features {
		record, err := normalizeRegion(feature, countryISO, level)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[record.Code]; exists {
			return nil, fmt.Errorf("%w: 行政区代码重复", domain.ErrProviderUnavailable)
		}
		seen[record.Code] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

func normalizeRegion(feature regionFeature, countryISO, level string) (exposurecollection.AdministrativeRegion, error) {
	if feature.Type != "Feature" || len(bytes.TrimSpace(feature.Geometry)) == 0 ||
		bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		return exposurecollection.AdministrativeRegion{}, fmt.Errorf("%w: 行政区要素几何缺失", domain.ErrProviderUnavailable)
	}
	var geometry struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil ||
		(geometry.Type != "Polygon" && geometry.Type != "MultiPolygon") {
		return exposurecollection.AdministrativeRegion{}, fmt.Errorf("%w: 行政区要素不是面状几何", domain.ErrProviderUnavailable)
	}
	value := feature.Properties
	code := strings.TrimSpace(value.ShapeISO)
	shapeID := strings.TrimSpace(value.ShapeID)
	if code == "" || code == countryISO {
		if shapeID == "" {
			return exposurecollection.AdministrativeRegion{}, fmt.Errorf("%w: 行政区要素标识缺失", domain.ErrProviderUnavailable)
		}
		code = countryISO + "-" + level + "-" + shapeID
	}
	if code == "" || strings.TrimSpace(value.ShapeName) == "" ||
		value.ShapeGroup != countryISO || value.ShapeType != level {
		return exposurecollection.AdministrativeRegion{}, fmt.Errorf("%w: 行政区属性不匹配", domain.ErrProviderUnavailable)
	}
	boundaryID := shapeID
	if boundaryID == "" {
		boundaryID = code
	} else {
		boundaryID = countryISO + "-" + level + "-" + boundaryID
	}
	return exposurecollection.AdministrativeRegion{Code: code, Name: strings.TrimSpace(value.ShapeName),
		Level: level, BoundaryID: boundaryID, Geometry: append(json.RawMessage(nil), feature.Geometry...)}, nil
}
