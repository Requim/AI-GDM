// Package geoboundaries 接入 geoBoundaries gbOpen 中国 ADM0 边界。
package geoboundaries

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const (
	defaultMetadataURL  = "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/"
	maxMetadataBytes    = 64 << 10
	maxGeometryBytes    = 512 << 10
	maxBoundaryPoints   = 20_000
	minimumBoundaryYear = 1900
	expectedSource      = "geoBoundaries, Wikimedia Commons"
	expectedLicense     = "Public Domain"
	expectedCRS         = "urn:ogc:def:crs:OGC:1.3:CRS84"
)

var (
	boundaryIDPattern   = regexp.MustCompile(`^CHN-ADM0-([0-9]+)$`)
	boundaryYearPattern = regexp.MustCompile(`^[0-9]{4}$`)
	shapeIDSuffix       = regexp.MustCompile(`^[0-9]+$`)
	geometryPath        = regexp.MustCompile(`^/wmgeolab/geoBoundaries/raw/([0-9a-f]{7,40})/(releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified\.geojson)$`)
	metadataKeys        = criticalKeys("boundaryID", "boundaryName", "boundaryISO", "boundaryYearRepresented",
		"boundaryType", "boundarySource", "boundaryLicense", "simplifiedGeometryGeoJSON")
	collectionKeys = criticalKeys("type", "features", "crs", "srs", "srsName", "srid", "epsg",
		"axisOrder", "coordinateOrder", "coordinateSystem", "coordinateReferenceSystem", "spatialReference")
	featureKeys = criticalKeys("type", "properties", "geometry", "crs", "srs", "srsName", "srid", "epsg",
		"axisOrder", "coordinateOrder", "coordinateSystem", "coordinateReferenceSystem", "spatialReference")
	propertyKeys = criticalKeys("shapeID", "shapeName", "shapeISO", "shapeGroup", "shapeType")
	geometryKeys = criticalKeys("type", "coordinates", "crs", "srs", "srsName", "srid", "epsg",
		"axisOrder", "coordinateOrder", "coordinateSystem", "coordinateReferenceSystem", "spatialReference")
	crsKeys                = criticalKeys("type", "properties")
	crsPropertyKeys        = criticalKeys("name")
	coordinateSemanticKeys = []string{"crs", "srs", "srsName", "srid", "epsg", "axisOrder",
		"coordinateOrder", "coordinateSystem", "coordinateReferenceSystem", "spatialReference"}
)

// Options 配置 geoBoundaries 元数据固定端点。
type Options struct {
	Client      *httpclient.Client
	MetadataURL string
}

// Provider 下载并校验版本化中国 ADM0 简化几何。
type Provider struct {
	client      *httpclient.Client
	metadataURL string
}

// New 创建 geoBoundaries provider。
func New(options Options) (*Provider, error) {
	if options.MetadataURL == "" {
		options.MetadataURL = defaultMetadataURL
	}
	if options.Client == nil || options.MetadataURL != defaultMetadataURL {
		return nil, fmt.Errorf("%w: geoBoundaries 配置无效", domain.ErrInvalidInput)
	}
	return &Provider{client: options.Client, metadataURL: options.MetadataURL}, nil
}

// Boundary 获取元数据和固定提交几何，运行时计算 SHA-256。
func (p *Provider) Boundary(ctx context.Context) (exposurecollection.AdministrativeBoundary, error) {
	metadataResponse, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodGet,
		URL: p.metadataURL, MaxBodyBytes: maxMetadataBytes,
		RedirectPolicy: httpclient.RedirectSameOriginHTTPS})
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, fmt.Errorf("读取 geoBoundaries 元数据: %w", err)
	}
	metadata, err := decodeMetadata(metadataResponse.Body)
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, err
	}
	metadataDigest := sha256.Sum256(metadataResponse.Body)
	metadataDigestHex := hex.EncodeToString(metadataDigest[:])
	downloadURL, err := fixedMediaURL(metadata.SimplifiedGeometry)
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, err
	}
	geometryResponse, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodGet,
		URL: downloadURL, MaxBodyBytes: maxGeometryBytes,
		RedirectPolicy: httpclient.RedirectDeny})
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, fmt.Errorf("下载 geoBoundaries 固定几何: %w", err)
	}
	geometry, err := decodeGeometry(geometryResponse.Body, metadata.BoundaryID)
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, err
	}
	digest := sha256.Sum256(geometryResponse.Body)
	digestHex := hex.EncodeToString(digest[:])
	return exposurecollection.AdministrativeBoundary{BoundaryID: metadata.BoundaryID,
		RegionCode: "CN", BoundaryType: metadata.BoundaryType, BoundaryYear: metadata.BoundaryYear,
		Source: metadata.Source, License: metadata.License, Digest: digestHex,
		Reference: metadata.SimplifiedGeometry, Geometry: geometry.Value,
		CollectedAt: geometryResponse.FetchedAt.UTC().Truncate(time.Microsecond),
		InputReferences: []string{p.metadataURL,
			boundaryBindingReference(downloadURL, metadata, geometry.ShapeID,
				metadataDigestHex, digestHex)}}, nil
}

// RiskBoundary 返回风险栅格处理使用的版本化中国 ADM0 边界。
func (p *Provider) RiskBoundary(ctx context.Context) (hazard.ProcessingBoundary, error) {
	value, err := p.Boundary(ctx)
	if err != nil {
		return hazard.ProcessingBoundary{}, err
	}
	var geometry spatial.Geometry
	if err = json.Unmarshal(value.Geometry, &geometry); err != nil {
		return hazard.ProcessingBoundary{}, providerError("geoBoundaries 风险边界几何无法解码")
	}
	geometryDigest, err := hazard.BoundaryGeometryDigest(geometry)
	if err != nil {
		return hazard.ProcessingBoundary{}, providerError("geoBoundaries 风险边界几何摘要无法计算")
	}
	boundary := hazard.ProcessingBoundary{
		Coverage: hazard.Coverage{
			Mode: hazard.CoverageAdministrativeBoundary, RegionCode: value.RegionCode,
			BoundaryID: value.BoundaryID, BoundaryType: value.BoundaryType,
			BoundaryVersion: value.BoundaryYear, Source: value.Source, License: value.License,
			Reference: value.Reference, SHA256: value.Digest,
			GeometrySHA256: geometryDigest, CollectedAt: value.CollectedAt,
		},
		Geometry: geometry, InputReferences: append([]string(nil), value.InputReferences...),
	}
	if err = boundary.Validate(); err != nil {
		return hazard.ProcessingBoundary{}, fmt.Errorf("校验 geoBoundaries 风险边界: %w", err)
	}
	return boundary, nil
}

type metadata struct {
	BoundaryID         string `json:"boundaryID"`
	BoundaryName       string `json:"boundaryName"`
	BoundaryISO        string `json:"boundaryISO"`
	BoundaryYear       string `json:"boundaryYearRepresented"`
	BoundaryType       string `json:"boundaryType"`
	Source             string `json:"boundarySource"`
	License            string `json:"boundaryLicense"`
	SimplifiedGeometry string `json:"simplifiedGeometryGeoJSON"`
}

func decodeMetadata(payload []byte) (metadata, error) {
	if _, err := scanCriticalObject(payload, "metadata", metadataKeys, true); err != nil {
		return metadata{}, err
	}
	var value metadata
	if err := json.Unmarshal(payload, &value); err != nil || !validMetadata(value) {
		return metadata{}, providerError("geoBoundaries 元数据不符合 CHN ADM0 契约")
	}
	if _, err := fixedMediaURL(value.SimplifiedGeometry); err != nil {
		return metadata{}, err
	}
	return value, nil
}

func validMetadata(value metadata) bool {
	_, validID := boundaryIDSuffix(value.BoundaryID)
	return validID && value.BoundaryName == "China" && value.BoundaryISO == "CHN" &&
		value.BoundaryType == "ADM0" && validBoundaryYear(value.BoundaryYear) &&
		value.Source == expectedSource && value.License == expectedLicense
}

func validBoundaryYear(value string) bool {
	if !boundaryYearPattern.MatchString(value) {
		return false
	}
	year, err := strconv.Atoi(value)
	return err == nil && year >= minimumBoundaryYear && year <= time.Now().UTC().Year()
}

func boundaryIDSuffix(value string) (string, bool) {
	matches := boundaryIDPattern.FindStringSubmatch(value)
	if len(matches) != 2 || strings.Trim(matches[1], "0") == "" {
		return "", false
	}
	return matches[1], true
}

func fixedMediaURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "github.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", providerError("geoBoundaries 几何地址主机无效")
	}
	matches := geometryPath.FindStringSubmatch(parsed.EscapedPath())
	if len(matches) != 3 {
		return "", providerError("geoBoundaries 几何地址不是固定提交路径")
	}
	return "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/" +
		matches[1] + "/" + matches[2], nil
}

type featureCollection struct {
	Type     string            `json:"type"`
	Features []json.RawMessage `json:"features"`
	CRS      json.RawMessage   `json:"crs"`
}

type boundaryFeature struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type boundaryProperties struct {
	ShapeID    string `json:"shapeID"`
	ShapeName  string `json:"shapeName"`
	ShapeISO   string `json:"shapeISO"`
	ShapeGroup string `json:"shapeGroup"`
	ShapeType  string `json:"shapeType"`
}

type decodedGeometry struct {
	Value   json.RawMessage
	ShapeID string
}

func decodeGeometry(payload []byte, boundaryID string) (decodedGeometry, error) {
	suffix, validID := boundaryIDSuffix(boundaryID)
	if !validID {
		return decodedGeometry{}, providerError("geoBoundaries boundaryID 无法绑定几何")
	}
	fields, err := scanCriticalObject(payload, "collection", collectionKeys, false)
	if err != nil {
		return decodedGeometry{}, err
	}
	if err = rejectCoordinateSemantics(fields, true); err != nil {
		return decodedGeometry{}, err
	}
	var collection featureCollection
	if err := json.Unmarshal(payload, &collection); err != nil || collection.Type != "FeatureCollection" ||
		len(collection.Features) != 1 {
		return decodedGeometry{}, providerError("geoBoundaries GeoJSON 不是单一 FeatureCollection")
	}
	if err := validateCRS(collection.CRS); err != nil {
		return decodedGeometry{}, err
	}
	return decodeFeature(collection.Features[0], suffix)
}

func decodeFeature(payload json.RawMessage, suffix string) (decodedGeometry, error) {
	fields, err := scanCriticalObject(payload, "feature", featureKeys, false)
	if err != nil {
		return decodedGeometry{}, err
	}
	if err = rejectCoordinateSemantics(fields, false); err != nil {
		return decodedGeometry{}, err
	}
	var feature boundaryFeature
	if err := json.Unmarshal(payload, &feature); err != nil || feature.Type != "Feature" {
		return decodedGeometry{}, providerError("geoBoundaries GeoJSON 要素无效")
	}
	properties, err := decodeProperties(feature.Properties, suffix)
	if err != nil {
		return decodedGeometry{}, err
	}
	if err := validateMultiPolygon(feature.Geometry); err != nil {
		return decodedGeometry{}, err
	}
	return decodedGeometry{Value: append(json.RawMessage(nil), feature.Geometry...),
		ShapeID: properties.ShapeID}, nil
}

func decodeProperties(payload json.RawMessage, suffix string) (boundaryProperties, error) {
	if _, err := scanCriticalObject(payload, "properties", propertyKeys, false); err != nil {
		return boundaryProperties{}, err
	}
	var value boundaryProperties
	if err := json.Unmarshal(payload, &value); err != nil || !validShapeID(value.ShapeID, suffix) ||
		value.ShapeName != "China" || value.ShapeISO != "CHN" || value.ShapeGroup != "CHN" ||
		value.ShapeType != "ADM0" {
		return boundaryProperties{}, providerError("geoBoundaries GeoJSON 属性不匹配 CHN ADM0")
	}
	return value, nil
}

func validShapeID(value, suffix string) bool {
	prefix := suffix + "B"
	return strings.HasPrefix(value, prefix) && shapeIDSuffix.MatchString(strings.TrimPrefix(value, prefix))
}

func boundaryBindingReference(downloadURL string, value metadata, shapeID,
	metadataDigest, geometryDigest string,
) string {
	query := url.Values{"boundaryID": {value.BoundaryID}, "boundaryYear": {value.BoundaryYear},
		"source": {value.Source}, "license": {value.License}, "shapeID": {shapeID},
		"metadataSha256": {metadataDigest}, "geometrySha256": {geometryDigest}}
	return downloadURL + "?" + query.Encode()
}

func validateCRS(payload json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	fields, err := scanCriticalObject(payload, "crs", crsKeys, false)
	if err != nil {
		return err
	}
	var crsType string
	if err = json.Unmarshal(fields["type"], &crsType); err != nil || crsType != "name" {
		return providerError("geoBoundaries CRS 不是固定 CRS84 对象")
	}
	properties, err := scanCriticalObject(fields["properties"], "crs properties", crsPropertyKeys, false)
	if err != nil {
		return err
	}
	var name string
	if err = json.Unmarshal(properties["name"], &name); err != nil || name != expectedCRS {
		return providerError("geoBoundaries CRS 不是固定 CRS84 对象")
	}
	return nil
}

func validateMultiPolygon(payload json.RawMessage) error {
	fields, err := scanCriticalObject(payload, "geometry", geometryKeys, false)
	if err != nil {
		return err
	}
	if err = rejectCoordinateSemantics(fields, false); err != nil {
		return err
	}
	var value struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(payload, &value); err != nil || value.Type != "MultiPolygon" {
		return providerError("geoBoundaries 几何必须是 MultiPolygon")
	}
	var coordinates [][][]coordinate
	if err := json.Unmarshal(value.Coordinates, &coordinates); err != nil || len(coordinates) == 0 {
		return providerError("geoBoundaries MultiPolygon 坐标无效")
	}
	points := 0
	for _, polygon := range coordinates {
		if len(polygon) == 0 {
			return providerError("geoBoundaries MultiPolygon 面不含环")
		}
		for _, ring := range polygon {
			if len(ring) < 4 || !sameCoordinate(ring[0], ring[len(ring)-1]) {
				return providerError("geoBoundaries 面环未闭合")
			}
			for _, coordinate := range ring {
				if !validCoordinate(coordinate) {
					return providerError("geoBoundaries 包含无效坐标")
				}
				points++
			}
		}
	}
	if points == 0 || points > maxBoundaryPoints {
		return providerError("geoBoundaries 顶点数超过安全预算")
	}
	return nil
}

func rejectCoordinateSemantics(fields map[string]json.RawMessage, allowCRS bool) error {
	for _, key := range coordinateSemanticKeys {
		if allowCRS && key == "crs" {
			continue
		}
		if fields[key] != nil {
			return providerError("geoBoundaries GeoJSON 包含非顶层坐标语义声明")
		}
	}
	return nil
}

func scanCriticalObject(payload []byte, scope string, keys map[string]string,
	allowExtensions bool,
) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, providerError("geoBoundaries " + scope + " 对象无效")
	}
	values := make(map[string]json.RawMessage, len(keys))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok {
			return nil, providerError("geoBoundaries " + scope + " 字段无效")
		}
		canonical, critical := canonicalCriticalKey(key, keys)
		if critical && (key != canonical || values[canonical] != nil) {
			return nil, providerError("geoBoundaries " + scope + " 安全字段重复或大小写不规范")
		}
		if !critical && !allowExtensions {
			return nil, providerError("geoBoundaries " + scope + " 包含额外字段")
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return nil, providerError("geoBoundaries " + scope + " 字段无法解码")
		}
		if critical {
			values[canonical] = raw
		}
	}
	if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
		return nil, providerError("geoBoundaries " + scope + " 对象未闭合")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, providerError("geoBoundaries " + scope + " 存在尾随内容")
	}
	return values, nil
}

func criticalKeys(values ...string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[value] = value
	}
	return result
}

// canonicalCriticalKey 使用 Unicode simple fold，与 encoding/json 的字段回退匹配保持一致。
func canonicalCriticalKey(key string, keys map[string]string) (string, bool) {
	if canonical, ok := keys[key]; ok {
		return canonical, true
	}
	for _, canonical := range keys {
		if strings.EqualFold(key, canonical) {
			return canonical, true
		}
	}
	return "", false
}

type coordinate []float64

func (c *coordinate) UnmarshalJSON(payload []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil || len(raw) < 2 {
		return fmt.Errorf("坐标必须至少包含两个数值 ordinate")
	}
	values := make(coordinate, len(raw))
	for index, item := range raw {
		var value *float64
		if err := json.Unmarshal(item, &value); err != nil || value == nil || !finite(*value) {
			return fmt.Errorf("坐标 ordinate %d 不是有限数值", index)
		}
		values[index] = *value
	}
	*c = values
	return nil
}

func validCoordinate(value coordinate) bool {
	return len(value) >= 2 && finite(value[0]) && finite(value[1]) &&
		value[0] >= -180 && value[0] <= 180 && value[1] >= -90 && value[1] <= 90
}

func sameCoordinate(left, right coordinate) bool {
	return len(left) >= 2 && len(right) >= 2 && left[0] == right[0] && left[1] == right[1]
}

func providerError(message string) error {
	return fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, message)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
