package openmeteo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	// DefaultBaseURL 是 Open-Meteo 通用天气预报接口。
	DefaultBaseURL             = "https://api.open-meteo.com/v1/forecast"
	defaultMaxPointsPerRequest = 25
	maxPointsPerForecast       = 100
	maxResponseBytes           = 16 << 20
)

var hourlyVariables = []string{
	"precipitation",
	"rain",
	"showers",
	"soil_moisture_0_to_1cm",
	"soil_moisture_1_to_3cm",
	"soil_moisture_3_to_9cm",
	"soil_moisture_9_to_27cm",
	"soil_moisture_27_to_81cm",
}

// Config 配置 Open-Meteo 地址、可选商业 API 密钥和每批坐标上限。
type Config struct {
	BaseURL             string
	APIKey              string
	MaxPointsPerRequest int
}

// Provider 读取 Open-Meteo 逐小时数值天气模型结果。
type Provider struct {
	client              *httpclient.Client
	baseURL             string
	apiKey              string
	maxPointsPerRequest int
	now                 func() time.Time
}

var _ ports.WeatherReader = (*Provider)(nil)

// New 创建 Open-Meteo 天气供应商适配器。
func New(client *httpclient.Client, config Config) *Provider {
	if client == nil {
		client = httpclient.New(httpclient.Options{})
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = DefaultBaseURL
	}
	return &Provider{
		client: client, baseURL: strings.TrimSpace(config.BaseURL),
		apiKey:              strings.TrimSpace(config.APIKey),
		maxPointsPerRequest: normalizeMaxPoints(config.MaxPointsPerRequest),
		now:                 func() time.Time { return time.Now().UTC() },
	}
}

func normalizeMaxPoints(value int) int {
	if value <= 0 || value > defaultMaxPointsPerRequest {
		return defaultMaxPointsPerRequest
	}
	return value
}

// Forecast 返回每个请求坐标对应的逐小时天气序列。
func (p *Provider) Forecast(
	ctx context.Context,
	points []spatial.Point,
	pastHours, forecastHours int,
) ([]hazard.WeatherSnapshot, error) {
	if err := validateQuery(points, pastHours, forecastHours); err != nil {
		return nil, err
	}
	result := make([]hazard.WeatherSnapshot, 0, len(points))
	for start := 0; start < len(points); start += p.maxPointsPerRequest {
		end := min(start+p.maxPointsPerRequest, len(points))
		batch, err := p.forecastBatch(ctx, points[start:end], pastHours, forecastHours)
		if err != nil {
			return nil, fmt.Errorf("读取 Open-Meteo 第 %d 批: %w", start/p.maxPointsPerRequest+1, err)
		}
		result = append(result, batch...)
	}
	return result, nil
}

func validateQuery(points []spatial.Point, pastHours, forecastHours int) error {
	if len(points) == 0 {
		return fmt.Errorf("%w: 天气查询坐标不能为空", domain.ErrInvalidInput)
	}
	if len(points) > maxPointsPerForecast {
		return fmt.Errorf("%w: 单次天气查询最多支持 %d 个坐标", domain.ErrInvalidInput, maxPointsPerForecast)
	}
	if pastHours < 0 || forecastHours <= 0 {
		return fmt.Errorf("%w: pastHours 不能为负且 forecastHours 必须为正数", domain.ErrInvalidInput)
	}
	if pastHours > int(^uint(0)>>1)-forecastHours {
		return fmt.Errorf("%w: 天气查询小时数溢出", domain.ErrInvalidInput)
	}
	for index, point := range points {
		if err := validatePoint(point); err != nil {
			return fmt.Errorf("第 %d 个天气查询坐标: %w", index, err)
		}
	}
	return nil
}

func validatePoint(point spatial.Point) error {
	if math.IsNaN(point.Longitude) || math.IsInf(point.Longitude, 0) ||
		math.IsNaN(point.Latitude) || math.IsInf(point.Latitude, 0) {
		return fmt.Errorf("%w: 坐标必须是有限数值", domain.ErrInvalidInput)
	}
	return point.Validate()
}

func (p *Provider) forecastBatch(
	ctx context.Context,
	points []spatial.Point,
	pastHours, forecastHours int,
) ([]hazard.WeatherSnapshot, error) {
	requestURL, err := p.buildURL(points, pastHours, forecastHours)
	if err != nil {
		return nil, err
	}
	response, err := p.client.Do(ctx, httpclient.Request{
		Method: http.MethodGet, URL: requestURL, MaxBodyBytes: maxResponseBytes,
		SensitiveQueryKeys: []string{"apikey"},
	})
	if err != nil {
		return nil, fmt.Errorf("请求 Open-Meteo: %w", err)
	}
	values, err := decodeResponses(response.Body, len(points))
	if err != nil {
		return nil, err
	}
	return p.snapshots(points, values, requestURL, response, pastHours+forecastHours)
}

func (p *Provider) snapshots(
	points []spatial.Point,
	values []apiResponse,
	requestURL string,
	response httpclient.Response,
	expectedHours int,
) ([]hazard.WeatherSnapshot, error) {
	digest := sha256.Sum256(response.Body)
	sourceURI := httpclient.RedactURL(requestURL, "apikey")
	result := make([]hazard.WeatherSnapshot, 0, len(points))
	for index, value := range values {
		snapshot, err := value.snapshot(snapshotInput{
			location: points[index], expectedHours: expectedHours,
			sourceURI: sourceURI, responseSHA: hex.EncodeToString(digest[:]),
			fetchedAt: response.FetchedAt, requestID: response.RequestID, now: p.now(),
		})
		if err != nil {
			return nil, fmt.Errorf("解析 Open-Meteo 第 %d 个坐标: %w", index, err)
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func (p *Provider) buildURL(points []spatial.Point, pastHours, forecastHours int) (string, error) {
	parsed, err := url.Parse(p.baseURL)
	if err != nil {
		return "", fmt.Errorf("%w: Open-Meteo 地址无效: %v", domain.ErrInvalidInput, err)
	}
	query := parsed.Query()
	query.Set("latitude", joinCoordinates(points, func(point spatial.Point) float64 { return point.Latitude }))
	query.Set("longitude", joinCoordinates(points, func(point spatial.Point) float64 { return point.Longitude }))
	query.Set("hourly", strings.Join(hourlyVariables, ","))
	query.Set("timezone", "GMT")
	query.Set("timeformat", "iso8601")
	query.Set("precipitation_unit", "mm")
	if pastHours > 0 {
		query.Set("past_hours", strconv.Itoa(pastHours))
	}
	query.Set("forecast_hours", strconv.Itoa(forecastHours))
	if p.apiKey != "" {
		query.Set("apikey", p.apiKey)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func joinCoordinates(points []spatial.Point, value func(spatial.Point) float64) string {
	values := make([]string, len(points))
	for index, point := range points {
		values[index] = strconv.FormatFloat(value(point), 'f', -1, 64)
	}
	return strings.Join(values, ",")
}
