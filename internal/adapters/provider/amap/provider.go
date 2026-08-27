// Package amap 提供高德地图 Web 服务的受控适配器。
package amap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/mapcoord"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	// DefaultBaseURL 是高德 Web 服务 API 的公共地址。
	DefaultBaseURL = "https://restapi.amap.com"
	providerName   = "高德地图开放平台"
	datasetName    = "高德 Web 服务 API"
	maxRadiusM     = 50_000
	maxPageSize    = 25
	maxResponse    = 8 << 20
)

// Config 保存服务端注入的高德配置。APIKey 和 SecurityCode 不会写入日志或来源 URI。
type Config struct {
	BaseURL      string
	APIKey       string
	SecurityCode string
	Timeout      time.Duration
	RadiusM      int
	PageSize     int
}

// Provider 将高德 GCJ-02 Web API 映射为领域层使用的 WGS84 端口。
type Provider struct {
	client       *httpclient.Client
	baseURL      *url.URL
	apiKey       string
	securityCode string
	transformer  ports.CoordinateTransformer
	timeout      time.Duration
	radiusM      int
	pageSize     int
}

var (
	// ErrUnsupportedMode 表示当前高德适配器没有足够输入支持该路线方式。
	ErrUnsupportedMode = errors.New("高德路线方式暂不支持")
)

var _ ports.PlaceFinder = (*Provider)(nil)
var _ ports.RoutePlanner = (*Provider)(nil)
var _ ports.TransitRoutePlanner = (*Provider)(nil)

// New 创建高德 Web 服务适配器。密钥只能通过参数注入，不从请求方输入读取。
func New(client *httpclient.Client, config Config) (*Provider, error) {
	if client == nil {
		client = httpclient.New(httpclient.Options{})
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = DefaultBaseURL
	}
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("%w: 高德服务地址无效", domain.ErrInvalidInput)
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	if config.RadiusM <= 0 || config.RadiusM > maxRadiusM {
		config.RadiusM = 5_000
	}
	if config.PageSize <= 0 || config.PageSize > maxPageSize {
		config.PageSize = maxPageSize
	}
	return &Provider{
		client: client, baseURL: baseURL, apiKey: strings.TrimSpace(config.APIKey),
		securityCode: strings.TrimSpace(config.SecurityCode), transformer: mapcoord.New(), timeout: config.Timeout,
		radiusM: config.RadiusM, pageSize: config.PageSize,
	}, nil
}

// FindNearby 搜索指定坐标周边的避难场所、医院或交通设施。
func (p *Provider) FindNearby(ctx context.Context, center spatial.Point,
	kind evacuation.FacilityType, radiusM int,
) ([]evacuation.Facility, error) {
	if err := validatePoint(center); err != nil {
		return nil, err
	}
	if radiusM <= 0 || radiusM > maxRadiusM {
		return nil, fmt.Errorf("%w: 搜索半径必须在 1 至 %d 米之间", domain.ErrInvalidInput, maxRadiusM)
	}
	keyword, err := facilityKeyword(kind)
	if err != nil {
		return nil, err
	}
	gcj, err := p.toGCJ02(center)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"key": {p.apiKey}, "location": {formatPoint(gcj)},
		"radius": {strconv.Itoa(radiusM)}, "keywords": {keyword},
		"page_size": {strconv.Itoa(p.pageSize)}, "page_num": {"1"},
		"extensions": {"all"},
	}
	body, requestURL, response, err := p.do(ctx, "/v5/place/around", query)
	if err != nil {
		return nil, fmt.Errorf("搜索高德周边设施: %w", err)
	}
	var payload placeResponse
	if err = decodeSuccess(body, &payload); err != nil {
		return nil, fmt.Errorf("解析高德周边设施: %w", err)
	}
	result := make([]evacuation.Facility, 0, len(payload.POIs))
	for _, poi := range payload.POIs {
		facility, parseErr := p.facility(poi, kind, requestURL, response)
		if parseErr != nil {
			return nil, fmt.Errorf("解析高德设施 %q: %w", poi.Name, parseErr)
		}
		result = append(result, facility)
	}
	return result, nil
}

// Plan 代理高德驾车或步行路线，并将路线几何转换为 WGS84。
func (p *Provider) Plan(ctx context.Context, origin, destination spatial.Point,
	mode evacuation.TravelMode,
) ([]evacuation.Route, error) {
	if err := validatePoint(origin); err != nil {
		return nil, fmt.Errorf("起点: %w", err)
	}
	if err := validatePoint(destination); err != nil {
		return nil, fmt.Errorf("终点: %w", err)
	}
	path, err := directionPath(mode)
	if err != nil {
		return nil, err
	}
	originGCJ, err := p.toGCJ02(origin)
	if err != nil {
		return nil, fmt.Errorf("转换起点坐标: %w", err)
	}
	destinationGCJ, err := p.toGCJ02(destination)
	if err != nil {
		return nil, fmt.Errorf("转换终点坐标: %w", err)
	}
	query := url.Values{
		"key": {p.apiKey}, "origin": {formatPoint(originGCJ)},
		"destination": {formatPoint(destinationGCJ)}, "show_fields": {"cost,polyline"},
	}
	body, requestURL, response, err := p.do(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("规划高德路线: %w", err)
	}
	var payload directionResponse
	if err = decodeSuccess(body, &payload); err != nil {
		return nil, fmt.Errorf("解析高德路线: %w", err)
	}
	return p.routes(payload, origin, destination, mode, requestURL, response)
}

// PlanTransit 代理高德 V5 公交路线，并将公交分段几何转换为 WGS84。
// city1 和 city2 必须是高德 citycode；单独保留该端口以避免破坏通用路线契约。
func (p *Provider) PlanTransit(ctx context.Context, origin, destination spatial.Point, city1, city2 string) ([]evacuation.Route, error) {
	if err := validatePoint(origin); err != nil {
		return nil, fmt.Errorf("起点: %w", err)
	}
	if err := validatePoint(destination); err != nil {
		return nil, fmt.Errorf("终点: %w", err)
	}
	city1 = strings.TrimSpace(city1)
	city2 = strings.TrimSpace(city2)
	if err := validateCityCode(city1, "起点城市"); err != nil {
		return nil, err
	}
	if err := validateCityCode(city2, "终点城市"); err != nil {
		return nil, err
	}
	originGCJ, err := p.toGCJ02(origin)
	if err != nil {
		return nil, fmt.Errorf("转换起点坐标: %w", err)
	}
	destinationGCJ, err := p.toGCJ02(destination)
	if err != nil {
		return nil, fmt.Errorf("转换终点坐标: %w", err)
	}
	query := url.Values{
		"key": {p.apiKey}, "origin": {formatPoint(originGCJ)},
		"destination": {formatPoint(destinationGCJ)}, "city1": {city1}, "city2": {city2},
		"strategy": {"0"}, "show_fields": {"cost,polyline"},
	}
	body, requestURL, response, err := p.do(ctx, "/v5/direction/transit/integrated", query)
	if err != nil {
		return nil, fmt.Errorf("规划高德公交路线: %w", err)
	}
	var payload transitResponse
	if err = decodeSuccess(body, &payload); err != nil {
		return nil, fmt.Errorf("解析高德公交路线: %w", err)
	}
	return p.transitRoutes(payload, origin, destination, requestURL, response)
}

func (p *Provider) do(ctx context.Context, path string, query url.Values) (
	[]byte, string, httpclient.Response, error,
) {
	requestURL := *p.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + path
	if p.securityCode != "" {
		query.Set("jscode", p.securityCode)
	}
	requestURL.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	response, err := p.client.Do(requestCtx, httpclient.Request{
		Method: http.MethodGet, URL: requestURL.String(), MaxBodyBytes: maxResponse,
		SensitiveQueryKeys: []string{"key", "jscode"},
	})
	if err != nil {
		return nil, requestURL.String(), httpclient.Response{}, err
	}
	return response.Body, requestURL.String(), response, nil
}

func (p *Provider) facility(value place, kind evacuation.FacilityType,
	requestURL string, response httpclient.Response,
) (evacuation.Facility, error) {
	point, err := p.parseGCJPoint(value.Location)
	if err != nil {
		return evacuation.Facility{}, err
	}
	distance, err := parseNumber(value.Distance)
	if err != nil {
		return evacuation.Facility{}, fmt.Errorf("距离无效: %w", err)
	}
	return evacuation.Facility{
		ID: value.ID, Name: value.Name, Type: kind, Location: point,
		Address: value.Address, DistanceMeters: distance,
		Source: p.provenance(requestURL, response, "POI 周边搜索"),
	}, nil
}

func (p *Provider) routes(payload directionResponse, origin, destination spatial.Point,
	mode evacuation.TravelMode, requestURL string, response httpclient.Response,
) ([]evacuation.Route, error) {
	if len(payload.Route.Paths) == 0 {
		return nil, fmt.Errorf("%w: 高德未返回可用路线", domain.ErrProviderUnavailable)
	}
	result := make([]evacuation.Route, 0, len(payload.Route.Paths))
	for index, path := range payload.Route.Paths {
		route, err := p.route(path, origin, destination, mode, index, requestURL, response)
		if err != nil {
			return nil, fmt.Errorf("解析高德第 %d 条路线: %w", index+1, err)
		}
		result = append(result, route)
	}
	return result, nil
}

func (p *Provider) route(path directionPathValue, origin, destination spatial.Point,
	mode evacuation.TravelMode, index int, requestURL string,
	response httpclient.Response,
) (evacuation.Route, error) {
	distance, err := parsePositiveNumber(path.Distance)
	if err != nil {
		return evacuation.Route{}, fmt.Errorf("距离无效: %w", err)
	}
	duration, err := parsePositiveInt(path.Duration)
	if err != nil {
		return evacuation.Route{}, fmt.Errorf("时长无效: %w", err)
	}
	geometry, err := p.parsePolyline(path.Steps)
	if err != nil {
		return evacuation.Route{}, err
	}
	steps := make([]evacuation.RouteStep, 0, len(path.Steps))
	for _, step := range path.Steps {
		stepDistance, stepErr := parsePositiveNumber(step.Distance)
		if stepErr != nil {
			return evacuation.Route{}, fmt.Errorf("步骤距离无效: %w", stepErr)
		}
		steps = append(steps, evacuation.RouteStep{
			Instruction: step.Instruction, RoadName: step.RoadName, DistanceM: stepDistance,
		})
	}
	return evacuation.Route{
		ID: fmt.Sprintf("amap-%s-%d", mode, index+1), Origin: origin, Destination: destination,
		Mode: mode, DistanceMeters: distance, DurationSeconds: duration,
		Geometry: geometry, Steps: steps, Source: p.provenance(requestURL, response, "路线规划"),
		Limitations: []string{"路线来自高德实时路网，仅作为疏散候选路线，尚未经过风险区过滤和交管部门确认"},
	}, nil
}

func (p *Provider) transitRoutes(payload transitResponse, origin, destination spatial.Point,
	requestURL string, response httpclient.Response,
) ([]evacuation.Route, error) {
	plans := payload.Route.Transits
	if len(plans) == 0 {
		plans = payload.Route.Paths
	}
	if len(plans) == 0 {
		return nil, fmt.Errorf("%w: 高德未返回可用公交方案", domain.ErrProviderUnavailable)
	}
	result := make([]evacuation.Route, 0, len(plans))
	for index, plan := range plans {
		route, err := p.transitRoute(plan, origin, destination, index, requestURL, response)
		if err != nil {
			return nil, fmt.Errorf("解析高德第 %d 条公交方案: %w", index+1, err)
		}
		result = append(result, route)
	}
	return result, nil
}

func (p *Provider) transitRoute(plan transitPlan, origin, destination spatial.Point,
	index int, requestURL string, response httpclient.Response,
) (evacuation.Route, error) {
	distance, err := parsePositiveNumber(string(plan.Distance))
	if err != nil {
		return evacuation.Route{}, fmt.Errorf("距离无效: %w", err)
	}
	durationText := string(plan.Duration)
	if strings.TrimSpace(durationText) == "" {
		durationText = string(plan.Cost.Duration)
	}
	duration, err := parsePositiveInt(durationText)
	if err != nil {
		return evacuation.Route{}, fmt.Errorf("时长无效: %w", err)
	}
	geometry, steps, err := p.transitGeometry(plan)
	if err != nil {
		return evacuation.Route{}, err
	}
	limitations := []string{
		"公交方案来自高德实时公共交通数据，仅作为疏散候选路线，尚未经过风险区过滤和交管部门确认",
	}
	if string(plan.NightFlag) == "1" {
		limitations = append(limitations, "该方案包含夜班公共交通")
	}
	return evacuation.Route{
		ID: fmt.Sprintf("amap-transit-%d", index+1), Origin: origin, Destination: destination,
		Mode: evacuation.TravelTransit, DistanceMeters: distance, DurationSeconds: duration,
		Geometry: geometry, Steps: steps, Source: p.provenance(requestURL, response, "公交路线规划"),
		Limitations: limitations,
	}, nil
}

func (p *Provider) transitGeometry(plan transitPlan) (spatial.Geometry, []evacuation.RouteStep, error) {
	coordinates := make([][2]float64, 0)
	steps := make([]evacuation.RouteStep, 0)
	for _, segment := range plan.Segments {
		if err := p.appendTransitSegment(segment, &coordinates, &steps); err != nil {
			return spatial.Geometry{}, nil, err
		}
	}
	if len(coordinates) == 0 {
		if err := p.appendTransitPolyline(&coordinates, plan.Polyline); err != nil {
			return spatial.Geometry{}, nil, err
		}
	}
	if len(coordinates) < 2 {
		return spatial.Geometry{}, nil, fmt.Errorf("%w: 公交方案缺少折线几何", domain.ErrProviderUnavailable)
	}
	content, err := json.Marshal(coordinates)
	if err != nil {
		return spatial.Geometry{}, nil, fmt.Errorf("%w: 公交路线几何编码失败", domain.ErrProviderUnavailable)
	}
	return spatial.Geometry{Type: "LineString", Coordinates: content}, steps, nil
}

func (p *Provider) appendTransitSegment(segment transitSegment, coordinates *[][2]float64,
	steps *[]evacuation.RouteStep,
) error {
	before := len(*coordinates)
	if segment.Walking != nil {
		if err := p.appendTransitPart(segment.Walking.Polyline, segment.Walking.Steps, coordinates, steps); err != nil {
			return err
		}
	}
	if segment.Bus != nil {
		if err := p.appendTransitPart(segment.Bus.Polyline, segment.Bus.Steps, coordinates, steps); err != nil {
			return err
		}
		if err := p.appendTransitBuslines(segment.Bus.Buslines, coordinates, steps); err != nil {
			return err
		}
	}
	if segment.Railway != nil {
		if err := p.appendTransitPart(segment.Railway.Polyline, segment.Railway.Steps, coordinates, steps); err != nil {
			return err
		}
	}
	if segment.Taxi != nil {
		if err := p.appendTransitPart(segment.Taxi.Polyline, segment.Taxi.Steps, coordinates, steps); err != nil {
			return err
		}
	}
	if len(*coordinates) == before {
		return p.appendTransitPolyline(coordinates, segment.Polyline)
	}
	return nil
}

func (p *Provider) appendTransitPart(polyline stringValue, values []transitStep,
	coordinates *[][2]float64, steps *[]evacuation.RouteStep,
) error {
	if err := p.appendTransitPolyline(coordinates, polyline); err != nil {
		return err
	}
	return p.appendTransitSteps(values, coordinates, steps)
}

func (p *Provider) appendTransitBuslines(values []transitBusline, coordinates *[][2]float64,
	steps *[]evacuation.RouteStep,
) error {
	for _, value := range values {
		if err := p.appendTransitPolyline(coordinates, value.Polyline); err != nil {
			return err
		}
		name := strings.TrimSpace(string(value.Name))
		if name == "" {
			continue
		}
		distance, err := optionalDistance(value.Distance)
		if err != nil {
			return fmt.Errorf("公交分段距离无效: %w", err)
		}
		*steps = append(*steps, evacuation.RouteStep{
			Instruction: "乘坐 " + name, RoadName: name, DistanceM: distance,
		})
	}
	return nil
}

func (p *Provider) appendTransitSteps(values []transitStep, coordinates *[][2]float64,
	steps *[]evacuation.RouteStep,
) error {
	for _, value := range values {
		if err := p.appendTransitPolyline(coordinates, value.Polyline); err != nil {
			return err
		}
		instruction := strings.TrimSpace(string(value.Instruction))
		distanceValue := value.Distance
		if strings.TrimSpace(string(distanceValue)) == "" {
			distanceValue = value.StepDistance
		}
		distance, err := optionalDistance(distanceValue)
		if err != nil {
			return fmt.Errorf("公交步骤距离无效: %w", err)
		}
		if instruction == "" && distance == 0 {
			continue
		}
		*steps = append(*steps, evacuation.RouteStep{
			Instruction: instruction, RoadName: strings.TrimSpace(string(value.RoadName)), DistanceM: distance,
		})
	}
	return nil
}

func (p *Provider) appendTransitPolyline(coordinates *[][2]float64, value stringValue) error {
	content := strings.TrimSpace(string(value))
	if content == "" {
		return nil
	}
	added := 0
	for _, item := range strings.Split(content, ";") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		point, err := p.parseGCJPoint(item)
		if err != nil {
			return fmt.Errorf("%w: 公交路线几何无效", domain.ErrProviderUnavailable)
		}
		coordinate := [2]float64{point.Longitude, point.Latitude}
		if len(*coordinates) == 0 || (*coordinates)[len(*coordinates)-1] != coordinate {
			*coordinates = append(*coordinates, coordinate)
		}
		added++
	}
	if added == 0 {
		return fmt.Errorf("%w: 公交路线几何为空", domain.ErrProviderUnavailable)
	}
	return nil
}

func (p *Provider) provenance(requestURL string, response httpclient.Response,
	description string,
) provenance.Provenance {
	digest := sha256.Sum256(response.Body)
	return provenance.Provenance{
		Provider: providerName, Dataset: datasetName, SourceURI: httpclient.RedactURL(requestURL, "key", "jscode"),
		Citation: description, DataKind: provenance.DataKindObservation,
		FetchedAt: response.FetchedAt.UTC(), ProviderRequestID: response.RequestID,
		SHA256: hex.EncodeToString(digest[:]), CRS: "WGS84",
	}
}

func (p *Provider) toGCJ02(point spatial.Point) (spatial.Point, error) {
	conversion, err := p.transformer.Convert(point, spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
	if err != nil {
		return spatial.Point{}, fmt.Errorf("%w: WGS84 转 GCJ-02 失败", domain.ErrInvalidInput)
	}
	return conversion.Point, nil
}

func validatePoint(point spatial.Point) error {
	if math.IsNaN(point.Longitude) || math.IsInf(point.Longitude, 0) ||
		math.IsNaN(point.Latitude) || math.IsInf(point.Latitude, 0) {
		return fmt.Errorf("%w: 坐标必须是有限数值", domain.ErrInvalidInput)
	}
	return point.Validate()
}

func validateCityCode(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s citycode 不能为空", domain.ErrInvalidInput, field)
	}
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return fmt.Errorf("%w: %s citycode 长度无效", domain.ErrInvalidInput, field)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return fmt.Errorf("%w: %s 仅支持高德数字 citycode", domain.ErrInvalidInput, field)
		}
	}
	return nil
}

func facilityKeyword(kind evacuation.FacilityType) (string, error) {
	keywords := map[evacuation.FacilityType]string{
		evacuation.FacilityShelter:   "应急避难场所",
		evacuation.FacilityHospital:  "医院",
		evacuation.FacilityTransport: "交通设施",
	}
	keyword, ok := keywords[kind]
	if !ok {
		return "", fmt.Errorf("%w: 不支持的设施类型 %q", domain.ErrInvalidInput, kind)
	}
	return keyword, nil
}

func directionPath(mode evacuation.TravelMode) (string, error) {
	switch mode {
	case evacuation.TravelDriving:
		return "/v5/direction/driving", nil
	case evacuation.TravelWalking:
		return "/v5/direction/walking", nil
	case evacuation.TravelTransit:
		return "", fmt.Errorf("%w: %w: 公交路线还需要城市参数", domain.ErrInvalidInput, ErrUnsupportedMode)
	default:
		return "", fmt.Errorf("%w: 不支持的交通方式 %q", domain.ErrInvalidInput, mode)
	}
}

func formatPoint(point spatial.Point) string {
	return strconv.FormatFloat(point.Longitude, 'f', 6, 64) + "," +
		strconv.FormatFloat(point.Latitude, 'f', 6, 64)
}

func (p *Provider) parseGCJPoint(value string) (spatial.Point, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return spatial.Point{}, fmt.Errorf("%w: 高德坐标格式无效", domain.ErrProviderUnavailable)
	}
	longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return spatial.Point{}, fmt.Errorf("%w: 高德经度无效", domain.ErrProviderUnavailable)
	}
	latitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return spatial.Point{}, fmt.Errorf("%w: 高德纬度无效", domain.ErrProviderUnavailable)
	}
	conversion, err := p.transformer.Convert(
		spatial.Point{Longitude: longitude, Latitude: latitude},
		spatial.CoordinateGCJ02, spatial.CoordinateWGS84,
	)
	if err != nil {
		return spatial.Point{}, fmt.Errorf("%w: 高德坐标转换失败", domain.ErrProviderUnavailable)
	}
	point := conversion.Point
	if err := validatePoint(point); err != nil {
		return spatial.Point{}, fmt.Errorf("%w: 高德坐标超出范围", domain.ErrProviderUnavailable)
	}
	return point, nil
}

func parseNumber(value string) (float64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("%w: 数值为空", domain.ErrProviderUnavailable)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, fmt.Errorf("%w: 数值格式无效", domain.ErrProviderUnavailable)
	}
	return parsed, nil
}

func parsePositiveNumber(value string) (float64, error) {
	parsed, err := parseNumber(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%w: 数值必须为正数", domain.ErrProviderUnavailable)
	}
	return parsed, nil
}

func optionalDistance(value stringValue) (float64, error) {
	if strings.TrimSpace(string(value)) == "" {
		return 0, nil
	}
	return parseNumber(string(value))
}

func parsePositiveInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%w: 整数必须为正数", domain.ErrProviderUnavailable)
	}
	return parsed, nil
}

func (p *Provider) parsePolyline(steps []directionStep) (spatial.Geometry, error) {
	coordinates := make([][2]float64, 0)
	for _, step := range steps {
		for _, value := range strings.Split(step.Polyline, ";") {
			point, err := p.parseGCJPoint(value)
			if err != nil {
				return spatial.Geometry{}, fmt.Errorf("%w: 路线几何无效", domain.ErrProviderUnavailable)
			}
			coordinate := [2]float64{point.Longitude, point.Latitude}
			if len(coordinates) == 0 || coordinates[len(coordinates)-1] != coordinate {
				coordinates = append(coordinates, coordinate)
			}
		}
	}
	if len(coordinates) < 2 {
		return spatial.Geometry{}, fmt.Errorf("%w: 高德路线缺少折线几何", domain.ErrProviderUnavailable)
	}
	content, err := json.Marshal(coordinates)
	if err != nil {
		return spatial.Geometry{}, fmt.Errorf("%w: 路线几何编码失败", domain.ErrProviderUnavailable)
	}
	return spatial.Geometry{Type: "LineString", Coordinates: content}, nil
}

func decodeSuccess(content []byte, target any) error {
	var envelope apiEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return fmt.Errorf("%w: 高德响应不是有效 JSON", domain.ErrProviderUnavailable)
	}
	if envelope.Status != "1" {
		return fmt.Errorf("%w: 高德接口返回失败", domain.ErrProviderUnavailable)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("%w: 高德响应结构无效", domain.ErrProviderUnavailable)
	}
	return nil
}

type apiEnvelope struct {
	Status string `json:"status"`
}

type placeResponse struct {
	POIs []place `json:"pois"`
}

type place struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Location string `json:"location"`
	Distance string `json:"distance"`
}

type directionResponse struct {
	Route struct {
		Paths []directionPathValue `json:"paths"`
	} `json:"route"`
}

type transitResponse struct {
	Route transitRouteValue `json:"route"`
}

type transitRouteValue struct {
	Transits []transitPlan `json:"transits"`
	Paths    []transitPlan `json:"paths"`
}

type transitPlan struct {
	Distance  stringValue      `json:"distance"`
	Duration  stringValue      `json:"duration"`
	NightFlag stringValue      `json:"nightflag"`
	Polyline  stringValue      `json:"polyline"`
	Cost      transitCost      `json:"cost"`
	Segments  []transitSegment `json:"segments"`
}

type transitCost struct {
	Duration   stringValue `json:"duration"`
	TransitFee stringValue `json:"transit_fee"`
}

type transitSegment struct {
	Polyline stringValue     `json:"polyline"`
	Walking  *transitWalking `json:"walking"`
	Bus      *transitBus     `json:"bus"`
	Railway  *transitRailway `json:"railway"`
	Taxi     *transitTaxi    `json:"taxi"`
}

type transitWalking struct {
	Polyline stringValue   `json:"polyline"`
	Steps    []transitStep `json:"steps"`
}

type transitBus struct {
	Polyline stringValue      `json:"polyline"`
	Steps    []transitStep    `json:"steps"`
	Buslines []transitBusline `json:"buslines"`
}

type transitRailway struct {
	Polyline stringValue   `json:"polyline"`
	Steps    []transitStep `json:"steps"`
}

type transitTaxi struct {
	Polyline stringValue   `json:"polyline"`
	Steps    []transitStep `json:"steps"`
}

type transitStep struct {
	Instruction  stringValue `json:"instruction"`
	RoadName     stringValue `json:"road_name"`
	Distance     stringValue `json:"distance"`
	StepDistance stringValue `json:"step_distance"`
	Polyline     stringValue `json:"polyline"`
}

type transitBusline struct {
	Name     stringValue `json:"name"`
	Distance stringValue `json:"distance"`
	Polyline stringValue `json:"polyline"`
}

type stringValue string

func (value *stringValue) UnmarshalJSON(content []byte) error {
	content = bytes.TrimSpace(content)
	if bytes.Equal(content, []byte("null")) {
		*value = ""
		return nil
	}
	var textValue string
	if err := json.Unmarshal(content, &textValue); err == nil {
		*value = stringValue(textValue)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(content, &number); err != nil {
		return fmt.Errorf("字符串或数字字段无效: %w", err)
	}
	*value = stringValue(number.String())
	return nil
}

type directionPathValue struct {
	Distance string          `json:"distance"`
	Duration string          `json:"duration"`
	Steps    []directionStep `json:"steps"`
}

type directionStep struct {
	Instruction string `json:"instruction"`
	RoadName    string `json:"road_name"`
	Distance    string `json:"distance"`
	Polyline    string `json:"polyline"`
}
