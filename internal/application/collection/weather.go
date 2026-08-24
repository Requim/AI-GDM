package collection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

const fallbackQualityFlag = "fallback_last_success"

// WeatherCollector 获取、校验并保存完整的监测点气象批次。
type WeatherCollector struct {
	provider ports.WeatherReader
	writer   ports.WeatherSnapshotWriter
	reader   ports.WeatherSnapshotReader
	clock    ports.Clock
	maxAge   time.Duration
}

// NewWeatherCollector 创建支持最后成功批次回退的气象采集用例。
func NewWeatherCollector(provider ports.WeatherReader, writer ports.WeatherSnapshotWriter,
	reader ports.WeatherSnapshotReader, clock ports.Clock, maxAge time.Duration,
) (*WeatherCollector, error) {
	if provider == nil || writer == nil || reader == nil || clock == nil || maxAge <= 0 {
		return nil, fmt.Errorf("%w: 气象采集依赖或回退时效无效", domain.ErrInvalidInput)
	}
	return &WeatherCollector{provider: provider, writer: writer, reader: reader, clock: clock, maxAge: maxAge}, nil
}

// Collect 刷新指定点集；失败时只返回同点集且未超过时效的最后成功批次。
func (c *WeatherCollector) Collect(ctx context.Context, points []spatial.Point,
	pastHours, forecastHours int,
) ([]hazard.WeatherSnapshot, error) {
	if err := validateWeatherRequest(points, pastHours, forecastHours); err != nil {
		return nil, err
	}
	expectedHours := pastHours + forecastHours
	snapshots, err := c.provider.Forecast(ctx, points, pastHours, forecastHours)
	if err != nil {
		return c.fallback(ctx, points, expectedHours, fmt.Errorf("获取实时气象: %w", err))
	}
	if err = validateWeatherBatch(points, snapshots, expectedHours); err != nil {
		return c.fallback(ctx, points, expectedHours, fmt.Errorf("校验实时气象: %w", err))
	}
	if err = c.writer.SaveBatch(ctx, snapshots); err != nil {
		return c.fallback(ctx, points, expectedHours, fmt.Errorf("保存实时气象: %w", err))
	}
	return snapshots, nil
}

func (c *WeatherCollector) fallback(ctx context.Context, points []spatial.Point,
	expectedHours int, cause error,
) ([]hazard.WeatherSnapshot, error) {
	snapshots, err := c.reader.Latest(ctx, points)
	if err != nil {
		return nil, unavailableWeather(cause, fmt.Errorf("读取最后成功气象: %w", err))
	}
	if err = validateWeatherBatch(points, snapshots, expectedHours); err != nil {
		return nil, unavailableWeather(cause, fmt.Errorf("校验最后成功气象: %w", err))
	}
	now := c.clock.Now().UTC()
	fetchedAt, err := oldestFetchedAt(snapshots)
	if err != nil || fetchedAt.After(now.Add(5*time.Minute)) || now.Sub(fetchedAt) > c.maxAge {
		if err == nil {
			err = fmt.Errorf("最后成功气象时间 %s 超出 %s 回退边界", fetchedAt, c.maxAge)
		}
		return nil, unavailableWeather(cause, err)
	}
	return markWeatherFallback(snapshots), nil
}

func validateWeatherRequest(points []spatial.Point, pastHours, forecastHours int) error {
	if len(points) == 0 || pastHours < 0 || forecastHours <= 0 || pastHours > int(^uint(0)>>1)-forecastHours {
		return fmt.Errorf("%w: 气象点集或时间窗口无效", domain.ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		if err := point.Validate(); err != nil || !finite(point.Longitude) || !finite(point.Latitude) {
			return fmt.Errorf("%w: 气象监测点无效", domain.ErrInvalidInput)
		}
		key := fmt.Sprintf("%.6f,%.6f", canonicalWeatherCoordinate(point.Longitude), canonicalWeatherCoordinate(point.Latitude))
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: 气象监测点重复", domain.ErrInvalidInput)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateWeatherBatch(points []spatial.Point, snapshots []hazard.WeatherSnapshot, expectedHours int) error {
	if len(snapshots) != len(points) {
		return fmt.Errorf("%w: 气象批次点数不完整", domain.ErrInsufficientData)
	}
	var timeline []time.Time
	for index, snapshot := range snapshots {
		if !sameWeatherPoint(points[index], snapshot.Location) {
			return fmt.Errorf("%w: 气象批次点位或顺序不一致", domain.ErrInvalidInput)
		}
		if err := validateWeatherSnapshot(snapshot, expectedHours); err != nil {
			return fmt.Errorf("气象点 %d: %w", index, err)
		}
		if index == 0 {
			timeline = weatherTimeline(snapshot.Hourly)
		} else if !sameWeatherTimeline(timeline, snapshot.Hourly) {
			return fmt.Errorf("%w: 各监测点逐小时时间轴不一致", domain.ErrInsufficientData)
		}
	}
	return nil
}

func validateWeatherSnapshot(snapshot hazard.WeatherSnapshot, expectedHours int) error {
	if len(snapshot.Hourly) != expectedHours {
		return fmt.Errorf("%w: 逐小时数据为 %d 条，预期 %d 条", domain.ErrInsufficientData, len(snapshot.Hourly), expectedHours)
	}
	if err := snapshot.Source.Validate(); err != nil {
		return err
	}
	var previous time.Time
	for _, point := range snapshot.Hourly {
		if err := validateHourlyWeather(point, previous); err != nil {
			return err
		}
		previous = point.Time
	}
	return nil
}

func weatherTimeline(values []hazard.WeatherPoint) []time.Time {
	result := make([]time.Time, len(values))
	for index, value := range values {
		result[index] = value.Time
	}
	return result
}

func sameWeatherTimeline(expected []time.Time, values []hazard.WeatherPoint) bool {
	if len(expected) != len(values) {
		return false
	}
	for index, value := range values {
		if !value.Time.Equal(expected[index]) {
			return false
		}
	}
	return true
}

func validateHourlyWeather(point hazard.WeatherPoint, previous time.Time) error {
	_, offset := point.Time.Zone()
	if point.Time.IsZero() || offset != 0 || (!previous.IsZero() && !point.Time.After(previous)) {
		return fmt.Errorf("%w: 气象时间必须是递增的 UTC 时间", domain.ErrInvalidInput)
	}
	values := []float64{point.PrecipitationMM, point.RainMM, point.ShowersMM}
	for _, value := range values {
		if !finite(value) || value < 0 {
			return fmt.Errorf("%w: 降水值无效", domain.ErrInvalidInput)
		}
	}
	if len(point.SoilMoistureByLayer) != 5 {
		return fmt.Errorf("%w: 土壤湿度必须包含五层", domain.ErrInvalidInput)
	}
	for _, value := range point.SoilMoistureByLayer {
		if !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: 土壤湿度超出零到一范围", domain.ErrInvalidInput)
		}
	}
	return nil
}

func oldestFetchedAt(snapshots []hazard.WeatherSnapshot) (time.Time, error) {
	var oldest time.Time
	for _, snapshot := range snapshots {
		fetchedAt := snapshot.Source.FetchedAt.UTC()
		if fetchedAt.IsZero() {
			return time.Time{}, fmt.Errorf("%w: 气象来源缺少抓取时间", domain.ErrInsufficientData)
		}
		if oldest.IsZero() || fetchedAt.Before(oldest) {
			oldest = fetchedAt
		}
	}
	return oldest, nil
}

func markWeatherFallback(values []hazard.WeatherSnapshot) []hazard.WeatherSnapshot {
	result := make([]hazard.WeatherSnapshot, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Hourly = append([]hazard.WeatherPoint(nil), value.Hourly...)
		for hourlyIndex := range result[index].Hourly {
			layers := value.Hourly[hourlyIndex].SoilMoistureByLayer
			result[index].Hourly[hourlyIndex].SoilMoistureByLayer = append([]float64(nil), layers...)
		}
		result[index].Source.Stale = true
		result[index].Source.QualityFlags = appendUnique(value.Source.QualityFlags, fallbackQualityFlag)
		result[index].Source.Limitations = appendUnique(value.Source.Limitations, "实时采集失败，使用同点集最后成功批次")
	}
	return result
}

func appendUnique(values []string, value string) []string {
	result := append([]string(nil), values...)
	for _, existing := range result {
		if existing == value {
			return result
		}
	}
	return append(result, value)
}

func unavailableWeather(causes ...error) error {
	values := append([]error{domain.ErrInsufficientData}, causes...)
	return fmt.Errorf("实时气象不可用: %w", errors.Join(values...))
}

func sameWeatherPoint(left, right spatial.Point) bool {
	return math.Abs(left.Longitude-right.Longitude) <= 0.0000005 &&
		math.Abs(left.Latitude-right.Latitude) <= 0.0000005
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func canonicalWeatherCoordinate(value float64) float64 {
	value = math.Round(value*1_000_000) / 1_000_000
	if value == 0 {
		return 0
	}
	return value
}
