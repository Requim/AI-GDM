// Package overpass 接入 OpenStreetMap Overpass 道路和应急设施数据。
package overpass

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	defaultEndpoint            = "https://overpass-api.de/api/interpreter"
	defaultFreshFor            = 24 * time.Hour
	defaultMaxBodyBytes        = 6 << 20
	defaultServerMaxSize       = 64 << 20
	defaultMaxElements         = 900
	defaultMaxTotalCoordinates = 250_000
	defaultMaxLatSpan          = 0.25
	defaultMaxLonSpan          = 0.25
	defaultMaxBBoxKM2          = 1_000.0
	maxTagsPerElement          = 128
	maxTagBytes                = 4096
	maxCoordinates             = 10_000
	supportedResponseVersion   = 0.6
	openStreetMapURL           = "https://www.openstreetmap.org"
)

var errNonClosedFacilityWay = errors.New("OSM 设施 way 未闭合")

const nonClosedFacilityLimitation = "Overpass 返回的非闭合或不足四点设施 way 已跳过，未作为设施计数"

// Options 配置 Overpass 固定端点和安全预算。
type Options struct {
	Client              *httpclient.Client
	Endpoint            string
	FreshFor            time.Duration
	MaxBodyBytes        int64
	ServerMaxSizeBytes  int64
	MaxElements         int
	MaxTotalCoordinates int
	MaxLatSpan          float64
	MaxLonSpan          float64
	MaxBBoxKM2          float64
}

// Provider 查询并规范化真实 OSM 道路和设施几何。
type Provider struct {
	client              *httpclient.Client
	endpoint            string
	freshFor            time.Duration
	maxBodyBytes        int64
	serverMaxSizeBytes  int64
	maxElements         int
	maxTotalCoordinates int
	maxLatSpan          float64
	maxLonSpan          float64
	maxBBoxKM2          float64
}

// New 创建 Overpass provider。
func New(options Options) (*Provider, error) {
	applyDefaults(&options)
	endpoint, err := normalizeEndpoint(options.Endpoint)
	if err != nil {
		return nil, err
	}
	if options.Client == nil || options.FreshFor <= 0 || options.FreshFor > 7*24*time.Hour ||
		options.MaxBodyBytes <= 0 || options.MaxBodyBytes > 16<<20 ||
		options.ServerMaxSizeBytes < 1<<20 || options.ServerMaxSizeBytes > 256<<20 ||
		options.MaxElements <= 0 || options.MaxElements > exposurecollection.MaxInfrastructure ||
		options.MaxTotalCoordinates <= 0 || options.MaxTotalCoordinates > exposurecollection.MaxTotalFeaturePoints ||
		options.MaxLatSpan <= 0 || options.MaxLonSpan <= 0 || options.MaxBBoxKM2 <= 0 {
		return nil, fmt.Errorf("%w: Overpass 配置无效", domain.ErrInvalidInput)
	}
	return &Provider{client: options.Client, endpoint: endpoint, freshFor: options.FreshFor,
		maxBodyBytes: options.MaxBodyBytes, serverMaxSizeBytes: options.ServerMaxSizeBytes,
		maxElements: options.MaxElements, maxTotalCoordinates: options.MaxTotalCoordinates,
		maxLatSpan: options.MaxLatSpan, maxLonSpan: options.MaxLonSpan,
		maxBBoxKM2: options.MaxBBoxKM2}, nil
}

// Infrastructure 返回真实 OSM 标识、几何、版本时间和来源引用。
func (p *Provider) Infrastructure(ctx context.Context,
	query exposurecollection.InfrastructureQuery,
) (exposurecollection.InfrastructureResult, error) {
	if err := validateBounds(query.Bounds); err != nil {
		return exposurecollection.InfrastructureResult{}, err
	}
	if !p.withinQueryBudget(query.Bounds) {
		return exposurecollection.InfrastructureResult{}, providerError("风险区联合外包框过大，拒绝向公共 Overpass 发起不完整查询")
	}
	body := []byte(url.Values{"data": {buildQuery(query.Bounds, p.serverMaxSizeBytes)}}.Encode())
	headers := make(http.Header)
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	headers.Set("Accept", "application/json")
	response, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodPost, URL: p.endpoint,
		Headers: headers, Body: body, MaxBodyBytes: p.maxBodyBytes,
		MaxAttempts: 1, RedirectPolicy: httpclient.RedirectDeny})
	if err != nil {
		return exposurecollection.InfrastructureResult{}, fmt.Errorf("查询 Overpass: %w", err)
	}
	value, err := decodeResponse(response.Body, p.maxElements, p.maxTotalCoordinates)
	if err != nil {
		return exposurecollection.InfrastructureResult{}, err
	}
	if value.OSM3S.Timestamp.After(response.FetchedAt.Add(5 * time.Minute)) {
		return exposurecollection.InfrastructureResult{}, providerError("OSM 数据时间晚于采集时间")
	}
	features, limitations, err := convertElements(value.Elements)
	if err != nil {
		return exposurecollection.InfrastructureResult{}, err
	}
	versionReference := "urn:openstreetmap:osm-base:" + value.OSM3S.Timestamp.Format(time.RFC3339)
	return exposurecollection.InfrastructureResult{OSMBaseTimestamp: value.OSM3S.Timestamp,
		CollectedAt: response.FetchedAt.UTC().Truncate(time.Microsecond), ValidFrom: value.OSM3S.Timestamp,
		ValidTo:         value.OSM3S.Timestamp.Add(p.freshFor),
		InputReferences: []string{openStreetMapURL, versionReference},
		Limitations:     limitations, Features: features}, nil
}

func (p *Provider) withinQueryBudget(value exposurecollection.Bounds) bool {
	latSpan, lonSpan := value.North-value.South, value.East-value.West
	centerLat := (value.North + value.South) / 2 * math.Pi / 180
	areaKM2 := latSpan * 111.32 * lonSpan * 111.32 * math.Cos(centerLat)
	return latSpan <= p.maxLatSpan && lonSpan <= p.maxLonSpan && areaKM2 > 0 && areaKM2 <= p.maxBBoxKM2
}

func buildQuery(bounds exposurecollection.Bounds, maxBytes int64) string {
	bbox := fmt.Sprintf("%.7f,%.7f,%.7f,%.7f", bounds.South, bounds.West, bounds.North, bounds.East)
	return fmt.Sprintf(`[out:json][timeout:25][maxsize:%d];(
way["highway"](%s);
node["amenity"~"^(hospital|clinic)$"](%s);
way["amenity"~"^(hospital|clinic)$"](%s);
node["emergency"="assembly_point"](%s);
way["emergency"="assembly_point"](%s);
node["social_facility"="shelter"](%s);
way["social_facility"="shelter"](%s);
);out geom meta;`, maxBytes, bbox, bbox, bbox, bbox, bbox, bbox, bbox)
}

type responseEnvelope struct {
	Version   float64      `json:"version"`
	Generator string       `json:"generator"`
	OSM3S     osmMetadata  `json:"osm3s"`
	Remark    osmRemark    `json:"remark"`
	Elements  []osmElement `json:"elements"`
}

type responseEnvelopeWire struct {
	Version   float64         `json:"version"`
	Generator string          `json:"generator"`
	OSM3S     osmMetadata     `json:"osm3s"`
	Remark    osmRemark       `json:"remark"`
	Elements  json.RawMessage `json:"elements"`
}

func (v *responseEnvelope) UnmarshalJSON(payload []byte) error {
	var wire responseEnvelopeWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if len(wire.Elements) == 0 || bytes.Equal(bytes.TrimSpace(wire.Elements), []byte("null")) {
		return fmt.Errorf("Overpass elements 缺失或为 null")
	}
	var elements []osmElement
	if err := json.Unmarshal(wire.Elements, &elements); err != nil {
		return err
	}
	*v = responseEnvelope{Version: wire.Version, Generator: wire.Generator,
		OSM3S: wire.OSM3S, Remark: wire.Remark, Elements: elements}
	return nil
}

type osmRemark struct {
	Value   string
	Present bool
	Null    bool
}

func (v *osmRemark) UnmarshalJSON(payload []byte) error {
	*v = osmRemark{Present: true}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		v.Null = true
		return nil
	}
	return json.Unmarshal(payload, &v.Value)
}

type osmMetadata struct {
	Timestamp time.Time
}

func (m *osmMetadata) UnmarshalJSON(payload []byte) error {
	var value struct {
		Timestamp string `json:"timestamp_osm_base"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, value.Timestamp)
	if err != nil {
		return err
	}
	m.Timestamp = parsed.UTC()
	return nil
}

type osmElement struct {
	Type      string                `json:"type"`
	ID        int64                 `json:"id"`
	Lat       osmCoordinateValue    `json:"lat"`
	Lon       osmCoordinateValue    `json:"lon"`
	Center    osmOptionalCoordinate `json:"center"`
	Tags      map[string]string     `json:"tags"`
	Geometry  []osmCoordinate       `json:"geometry"`
	rawDigest [sha256.Size]byte
}

func (v *osmElement) UnmarshalJSON(payload []byte) error {
	type wireElement osmElement
	var decoded wireElement
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	digest, err := canonicalElementDigest(payload)
	if err != nil {
		return err
	}
	*v = osmElement(decoded)
	v.rawDigest = digest
	return nil
}

type osmCoordinate struct {
	Lat osmCoordinateValue `json:"lat"`
	Lon osmCoordinateValue `json:"lon"`
}

type osmCoordinateValue struct {
	Value   float64
	Present bool
	Null    bool
}

func (v *osmCoordinateValue) UnmarshalJSON(payload []byte) error {
	*v = osmCoordinateValue{Present: true}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		v.Null = true
		return nil
	}
	return json.Unmarshal(payload, &v.Value)
}

type osmOptionalCoordinate struct {
	osmCoordinate
	Present bool
	Null    bool
}

type osmIdentity struct {
	Type string
	ID   int64
}

func (v *osmOptionalCoordinate) UnmarshalJSON(payload []byte) error {
	*v = osmOptionalCoordinate{Present: true}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		v.Null = true
		return nil
	}
	return json.Unmarshal(payload, &v.osmCoordinate)
}

func canonicalElementDigest(payload []byte) ([sha256.Size]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return [sha256.Size]byte{}, fmt.Errorf("OSM 元素包含多余 JSON 值")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

func decodeResponse(payload []byte, maxElements, maxTotalCoordinates int) (responseEnvelope, error) {
	if err := rejectDuplicateResponseKeys(payload); err != nil {
		return responseEnvelope{}, err
	}
	var value responseEnvelope
	if err := json.Unmarshal(payload, &value); err != nil || value.Version != supportedResponseVersion ||
		value.Generator == "" || value.OSM3S.Timestamp.IsZero() || value.Remark.Null ||
		strings.TrimSpace(value.Remark.Value) != "" {
		return responseEnvelope{}, providerError("Overpass 响应无效")
	}
	if len(value.Elements) > maxElements {
		return responseEnvelope{}, providerError("Overpass 元素数量超过安全预算")
	}
	if coordinates := responseCoordinateCount(value.Elements); coordinates > maxTotalCoordinates ||
		(len(value.Elements) > 0 && coordinates <= 0) {
		return responseEnvelope{}, providerError("Overpass 总坐标数超过安全预算")
	}
	return value, nil
}

func rejectDuplicateResponseKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return providerError("Overpass 响应顶层结构无效")
	}
	return scanResponseObject(decoder)
}

func scanResponseObject(decoder *json.Decoder) error {
	seen, seenFolded := make(map[string]struct{}), make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return providerError("Overpass 响应对象字段无效")
		}
		if _, exists := seen[key]; exists {
			return providerError("Overpass 响应字段重复")
		}
		seen[key] = struct{}{}
		canonical, protected := canonicalResponseKey(key)
		if protected {
			if key != canonical {
				return providerError("Overpass 响应安全字段必须使用规范小写拼写")
			}
			if _, exists := seenFolded[canonical]; exists {
				return providerError("Overpass 响应安全字段重复")
			}
			seenFolded[canonical] = struct{}{}
		}
		if err = scanResponseValue(decoder); err != nil {
			return err
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return providerError("Overpass 响应对象未闭合")
	}
	return nil
}

func scanResponseValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return providerError("Overpass 响应字段无法解码")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter == '{' {
		return scanResponseObject(decoder)
	}
	if delimiter != '[' {
		return providerError("Overpass 响应字段结构无效")
	}
	for decoder.More() {
		if err = scanResponseValue(decoder); err != nil {
			return err
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
		return providerError("Overpass 响应数组未闭合")
	}
	return nil
}

func canonicalResponseKey(value string) (string, bool) {
	keys := [...]string{
		"version", "generator", "osm3s", "remark", "elements", "timestamp_osm_base",
		"copyright", "type", "id", "lat", "lon", "center", "tags", "geometry",
		"highway", "amenity", "emergency", "social_facility",
	}
	for _, canonical := range keys {
		if strings.EqualFold(value, canonical) {
			return canonical, true
		}
	}
	return "", false
}

func convertElements(elements []osmElement) ([]exposurecollection.RawInfrastructureFeature, []string, error) {
	values := make([]exposurecollection.RawInfrastructureFeature, 0, len(elements))
	seen := make(map[osmIdentity]osmElement, len(elements))
	skippedFacilities := 0
	for _, element := range elements {
		unique, err := registerElementIdentity(seen, element)
		if err != nil {
			return nil, nil, err
		}
		if !unique {
			continue
		}
		feature, include, err := convertElement(element)
		if errors.Is(err, errNonClosedFacilityWay) {
			skippedFacilities++
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !include {
			return nil, nil, providerError("OSM 元素标签无法分类")
		}
		values = append(values, feature)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].FeatureID < values[right].FeatureID })
	limitations := make([]string, 0, 1)
	if skippedFacilities > 0 {
		limitations = append(limitations, fmt.Sprintf("%s（%d 条）", nonClosedFacilityLimitation, skippedFacilities))
	}
	return values, limitations, nil
}

func registerElementIdentity(seen map[osmIdentity]osmElement, value osmElement) (bool, error) {
	identity := osmIdentity{Type: value.Type, ID: value.ID}
	previous, exists := seen[identity]
	if !exists {
		seen[identity] = value
		return true, nil
	}
	if !reflect.DeepEqual(previous, value) {
		return false, providerError("OSM 原始身份对应的元素内容冲突")
	}
	return false, nil
}

func convertElement(value osmElement) (exposurecollection.RawInfrastructureFeature, bool, error) {
	if value.ID <= 0 || (value.Type != "node" && value.Type != "way") ||
		len(value.Tags) == 0 || len(value.Tags) > maxTagsPerElement || tagBytes(value.Tags) > maxTagBytes {
		return exposurecollection.RawInfrastructureFeature{}, false, providerError("OSM 元素身份或标签无效")
	}
	kind, include := classify(value.Tags)
	if !include {
		return exposurecollection.RawInfrastructureFeature{}, false, providerError("OSM 元素标签无法分类")
	}
	geometry, err := elementGeometry(value, kind)
	if err != nil {
		return exposurecollection.RawInfrastructureFeature{}, false, err
	}
	id := fmt.Sprintf("osm-%s-%s-%d", kind, value.Type, value.ID)
	reference := fmt.Sprintf("%s/%s/%d", openStreetMapURL, value.Type, value.ID)
	return exposurecollection.RawInfrastructureFeature{FeatureID: id, Kind: kind,
		Geometry: geometry, InputReferences: []string{reference}}, true, nil
}

func classify(tags map[string]string) (applicationloss.LossFeatureKind, bool) {
	if strings.TrimSpace(tags["highway"]) != "" {
		return applicationloss.LossFeatureRoad, true
	}
	amenity := tags["amenity"]
	if amenity == "hospital" || amenity == "clinic" || tags["emergency"] == "assembly_point" ||
		tags["social_facility"] == "shelter" {
		return applicationloss.LossFeatureFacility, true
	}
	return "", false
}

func elementGeometry(value osmElement, kind applicationloss.LossFeatureKind) (json.RawMessage, error) {
	if err := validateOptionalCenter(value.Center); err != nil {
		return nil, err
	}
	if value.Type == "node" {
		longitude, latitude, ok := coordinatePair(value.Lon, value.Lat)
		if kind != applicationloss.LossFeatureFacility || !ok {
			return nil, providerError("OSM 节点几何无效")
		}
		return json.Marshal(map[string]any{"type": "Point", "coordinates": []float64{longitude, latitude}})
	}
	if len(value.Geometry) < 2 || len(value.Geometry) > maxCoordinates {
		return nil, providerError("OSM way 坐标数量无效")
	}
	coordinates, err := wayCoordinates(value.Geometry)
	if err != nil {
		return nil, err
	}
	geometryType, geometryCoordinates := "LineString", any(coordinates)
	if kind == applicationloss.LossFeatureFacility {
		if !closed(coordinates) || len(coordinates) < 4 {
			return nil, errNonClosedFacilityWay
		}
		geometryType, geometryCoordinates = "Polygon", [][][]float64{coordinates}
	}
	return json.Marshal(map[string]any{"type": geometryType, "coordinates": geometryCoordinates})
}

func wayCoordinates(values []osmCoordinate) ([][]float64, error) {
	result := make([][]float64, len(values))
	for index, value := range values {
		longitude, latitude, ok := coordinatePair(value.Lon, value.Lat)
		if !ok {
			return nil, providerError("OSM way 包含无效坐标")
		}
		result[index] = []float64{longitude, latitude}
	}
	return result, nil
}

func validateOptionalCenter(value osmOptionalCoordinate) error {
	if !value.Present {
		return nil
	}
	if value.Null {
		return providerError("OSM center 坐标无效")
	}
	if _, _, ok := coordinatePair(value.Lon, value.Lat); !ok {
		return providerError("OSM center 坐标无效")
	}
	return nil
}

func coordinatePair(longitude, latitude osmCoordinateValue) (float64, float64, bool) {
	if !longitude.Present || longitude.Null || !latitude.Present || latitude.Null ||
		!validCoordinate(longitude.Value, latitude.Value) {
		return 0, 0, false
	}
	return longitude.Value, latitude.Value, true
}

func closed(values [][]float64) bool {
	last := len(values) - 1
	return last >= 0 && values[0][0] == values[last][0] && values[0][1] == values[last][1]
}

func validateBounds(value exposurecollection.Bounds) error {
	if !finite(value.South) || !finite(value.West) || !finite(value.North) || !finite(value.East) ||
		value.South < -90 || value.North > 90 || value.West < -180 || value.East > 180 ||
		value.South >= value.North || value.West >= value.East {
		return fmt.Errorf("%w: Overpass WGS84 范围无效", domain.ErrInvalidInput)
	}
	return nil
}

func normalizeEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: Overpass 端点必须是无凭据 HTTPS 地址", domain.ErrInvalidInput)
	}
	return parsed.String(), nil
}

func applyDefaults(options *Options) {
	if options.Endpoint == "" {
		options.Endpoint = defaultEndpoint
	}
	if options.FreshFor == 0 {
		options.FreshFor = defaultFreshFor
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = defaultMaxBodyBytes
	}
	if options.ServerMaxSizeBytes == 0 {
		options.ServerMaxSizeBytes = defaultServerMaxSize
	}
	if options.MaxElements == 0 {
		options.MaxElements = defaultMaxElements
	}
	if options.MaxTotalCoordinates == 0 {
		options.MaxTotalCoordinates = defaultMaxTotalCoordinates
	}
	if options.MaxLatSpan == 0 {
		options.MaxLatSpan = defaultMaxLatSpan
	}
	if options.MaxLonSpan == 0 {
		options.MaxLonSpan = defaultMaxLonSpan
	}
	if options.MaxBBoxKM2 == 0 {
		options.MaxBBoxKM2 = defaultMaxBBoxKM2
	}
}

func responseCoordinateCount(values []osmElement) int {
	total := 0
	for _, value := range values {
		if value.Type == "node" {
			total++
		} else {
			total += len(value.Geometry)
		}
		if value.Center.Present && !value.Center.Null {
			total++
		}
	}
	return total
}

func tagBytes(values map[string]string) int {
	total := 0
	for key, value := range values {
		total += len(key) + len(value)
	}
	return total
}

func validCoordinate(longitude, latitude float64) bool {
	return finite(longitude) && finite(latitude) && longitude >= -180 && longitude <= 180 &&
		latitude >= -90 && latitude <= 90
}

func providerError(message string) error {
	return fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, message)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
