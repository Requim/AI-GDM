package openmeteo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const (
	hourlyTimeLayout = "2006-01-02T15:04"
	soilMoistureUnit = "m³/m³"
)

var providerLimitations = []string{
	"Open-Meteo 提供数值天气模型结果，不是地面传感器实测或官方预警",
	"模型网格中心可能偏离请求坐标，局地强降水和土壤状态可能存在误差",
	"bbox 仅记录接口返回的模型网格中心，Open-Meteo 未在响应中提供网格边界",
	"免费接口不承诺 SLA，结果仅用于 AI-GDM 辅助研判",
}

type apiResponse struct {
	Latitude             *float64       `json:"latitude"`
	Longitude            *float64       `json:"longitude"`
	UTCOffsetSeconds     *int           `json:"utc_offset_seconds"`
	Timezone             string         `json:"timezone"`
	TimezoneAbbreviation string         `json:"timezone_abbreviation"`
	HourlyUnits          apiHourlyUnits `json:"hourly_units"`
	Hourly               apiHourly      `json:"hourly"`
}

type apiHourlyUnits struct {
	Time                 string `json:"time"`
	Precipitation        string `json:"precipitation"`
	Rain                 string `json:"rain"`
	Showers              string `json:"showers"`
	SoilMoisture0To1CM   string `json:"soil_moisture_0_to_1cm"`
	SoilMoisture1To3CM   string `json:"soil_moisture_1_to_3cm"`
	SoilMoisture3To9CM   string `json:"soil_moisture_3_to_9cm"`
	SoilMoisture9To27CM  string `json:"soil_moisture_9_to_27cm"`
	SoilMoisture27To81CM string `json:"soil_moisture_27_to_81cm"`
}

type apiHourly struct {
	Time                 []string   `json:"time"`
	Precipitation        []*float64 `json:"precipitation"`
	Rain                 []*float64 `json:"rain"`
	Showers              []*float64 `json:"showers"`
	SoilMoisture0To1CM   []*float64 `json:"soil_moisture_0_to_1cm"`
	SoilMoisture1To3CM   []*float64 `json:"soil_moisture_1_to_3cm"`
	SoilMoisture3To9CM   []*float64 `json:"soil_moisture_3_to_9cm"`
	SoilMoisture9To27CM  []*float64 `json:"soil_moisture_9_to_27cm"`
	SoilMoisture27To81CM []*float64 `json:"soil_moisture_27_to_81cm"`
}

type apiError struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

type numericSeries struct {
	name    string
	values  []*float64
	maximum float64
}

type snapshotInput struct {
	location      spatial.Point
	expectedHours int
	sourceURI     string
	responseSHA   string
	fetchedAt     time.Time
	requestID     string
	now           time.Time
}

func decodeResponses(content []byte, expected int) ([]apiResponse, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, responseError("为空")
	}
	var (
		values []apiResponse
		err    error
	)
	switch trimmed[0] {
	case '{':
		values, err = decodeObject(trimmed)
	case '[':
		err = json.Unmarshal(trimmed, &values)
	default:
		err = fmt.Errorf("首字符不是 JSON 对象或数组")
	}
	if err != nil {
		return nil, responseError("不是有效 JSON: %v", err)
	}
	if len(values) != expected {
		return nil, responseError("位置数量为 %d，预期 %d", len(values), expected)
	}
	return values, nil
}

func decodeObject(content []byte) ([]apiResponse, error) {
	var errorValue apiError
	if err := json.Unmarshal(content, &errorValue); err != nil {
		return nil, err
	}
	if errorValue.Error {
		if errorValue.Reason == "" {
			errorValue.Reason = "未提供原因"
		}
		return nil, fmt.Errorf("供应商业务错误: %s", errorValue.Reason)
	}
	var value apiResponse
	if err := json.Unmarshal(content, &value); err != nil {
		return nil, err
	}
	return []apiResponse{value}, nil
}

func (r apiResponse) snapshot(input snapshotInput) (hazard.WeatherSnapshot, error) {
	grid, times, err := r.validate(input.expectedHours)
	if err != nil {
		return hazard.WeatherSnapshot{}, err
	}
	hourly := r.Hourly.weatherPoints(times)
	return hazard.WeatherSnapshot{
		Location: input.location,
		Hourly:   hourly,
		Source:   r.provenance(input, grid, times),
	}, nil
}

func (r apiResponse) validate(expectedHours int) (spatial.Point, []time.Time, error) {
	grid, err := r.gridPoint()
	if err != nil {
		return spatial.Point{}, nil, err
	}
	if r.UTCOffsetSeconds == nil || *r.UTCOffsetSeconds != 0 ||
		r.Timezone != "GMT" || r.TimezoneAbbreviation != "GMT" {
		return spatial.Point{}, nil, responseError("时区不是 GMT/UTC+0")
	}
	if err = r.HourlyUnits.validate(); err != nil {
		return spatial.Point{}, nil, err
	}
	times, err := r.Hourly.validate(expectedHours)
	if err != nil {
		return spatial.Point{}, nil, err
	}
	return grid, times, nil
}

func (r apiResponse) gridPoint() (spatial.Point, error) {
	if r.Latitude == nil || r.Longitude == nil {
		return spatial.Point{}, responseError("缺少模型网格坐标")
	}
	point := spatial.Point{Longitude: *r.Longitude, Latitude: *r.Latitude}
	if err := validatePoint(point); err != nil {
		return spatial.Point{}, responseError("模型网格坐标无效: %v", err)
	}
	return point, nil
}

func (u apiHourlyUnits) validate() error {
	if u.Time != "iso8601" {
		return responseError("time 单位为 %q，预期 iso8601", u.Time)
	}
	for name, unit := range map[string]string{
		"precipitation": u.Precipitation,
		"rain":          u.Rain,
		"showers":       u.Showers,
	} {
		if unit != "mm" {
			return responseError("%s 单位为 %q，预期 mm", name, unit)
		}
	}
	for name, unit := range u.soilMoistureUnits() {
		if unit != soilMoistureUnit {
			return responseError("%s 单位为 %q，预期 %s", name, unit, soilMoistureUnit)
		}
	}
	return nil
}

func (u apiHourlyUnits) soilMoistureUnits() map[string]string {
	return map[string]string{
		"soil_moisture_0_to_1cm":   u.SoilMoisture0To1CM,
		"soil_moisture_1_to_3cm":   u.SoilMoisture1To3CM,
		"soil_moisture_3_to_9cm":   u.SoilMoisture3To9CM,
		"soil_moisture_9_to_27cm":  u.SoilMoisture9To27CM,
		"soil_moisture_27_to_81cm": u.SoilMoisture27To81CM,
	}
}

func (h apiHourly) validate(expectedHours int) ([]time.Time, error) {
	if len(h.Time) != expectedHours {
		return nil, responseError("time 长度为 %d，预期 %d", len(h.Time), expectedHours)
	}
	for _, series := range h.numericSeries() {
		if err := series.validate(expectedHours); err != nil {
			return nil, err
		}
	}
	return parseHourlyTimes(h.Time)
}

func (h apiHourly) numericSeries() []numericSeries {
	return []numericSeries{
		{name: "precipitation", values: h.Precipitation, maximum: math.Inf(1)},
		{name: "rain", values: h.Rain, maximum: math.Inf(1)},
		{name: "showers", values: h.Showers, maximum: math.Inf(1)},
		{name: "soil_moisture_0_to_1cm", values: h.SoilMoisture0To1CM, maximum: 1},
		{name: "soil_moisture_1_to_3cm", values: h.SoilMoisture1To3CM, maximum: 1},
		{name: "soil_moisture_3_to_9cm", values: h.SoilMoisture3To9CM, maximum: 1},
		{name: "soil_moisture_9_to_27cm", values: h.SoilMoisture9To27CM, maximum: 1},
		{name: "soil_moisture_27_to_81cm", values: h.SoilMoisture27To81CM, maximum: 1},
	}
}

func (s numericSeries) validate(expectedHours int) error {
	if len(s.values) != expectedHours {
		return responseError("%s 长度为 %d，预期 %d", s.name, len(s.values), expectedHours)
	}
	for index, pointer := range s.values {
		if pointer == nil || math.IsNaN(*pointer) || math.IsInf(*pointer, 0) {
			return responseError("%s[%d] 不是有限数值", s.name, index)
		}
		if *pointer < 0 || *pointer > s.maximum {
			return responseError("%s[%d]=%v 超出允许范围", s.name, index, *pointer)
		}
	}
	return nil
}

func parseHourlyTimes(values []string) ([]time.Time, error) {
	result := make([]time.Time, len(values))
	for index, value := range values {
		parsed, err := time.ParseInLocation(hourlyTimeLayout, value, time.UTC)
		if err != nil {
			return nil, responseError("time[%d]=%q 不是 GMT 小时时间: %v", index, value, err)
		}
		if index > 0 && !parsed.Equal(result[index-1].Add(time.Hour)) {
			return nil, responseError("time[%d] 与前一时间不连续", index)
		}
		result[index] = parsed
	}
	return result, nil
}

func (h apiHourly) weatherPoints(times []time.Time) []hazard.WeatherPoint {
	result := make([]hazard.WeatherPoint, len(times))
	for index, value := range times {
		result[index] = hazard.WeatherPoint{
			Time: value, PrecipitationMM: *h.Precipitation[index],
			RainMM: *h.Rain[index], ShowersMM: *h.Showers[index],
			SoilMoistureByLayer: []float64{
				*h.SoilMoisture0To1CM[index], *h.SoilMoisture1To3CM[index],
				*h.SoilMoisture3To9CM[index], *h.SoilMoisture9To27CM[index],
				*h.SoilMoisture27To81CM[index],
			},
		}
	}
	return result
}

func (r apiResponse) provenance(
	input snapshotInput,
	grid spatial.Point,
	times []time.Time,
) provenance.Provenance {
	validTo := times[len(times)-1].Add(time.Hour)
	return provenance.Provenance{
		Provider: "Open-Meteo", Dataset: "Weather Forecast API", DatasetVersion: "v1",
		SourceURI: input.sourceURI, Citation: "Open-Meteo Weather Forecast API",
		License: "CC BY 4.0", DataKind: provenance.DataKindForecast,
		FetchedAt: input.fetchedAt.UTC(), ValidFrom: times[0], ValidTo: validTo,
		SpatialResolution: "Open-Meteo best_match 模型网格", TemporalResolution: "1 hour",
		CRS: "EPSG:4326", BBox: [4]float64{grid.Longitude, grid.Latitude, grid.Longitude, grid.Latitude},
		SHA256: input.responseSHA, TransformVersion: "openmeteo-adapter-v1",
		ProviderRequestID: input.requestID, Model: "best_match",
		Stale: input.now.UTC().After(validTo), QualityFlags: gridQualityFlags(input.location, grid),
		Limitations: append([]string(nil), providerLimitations...),
	}
}

func gridQualityFlags(requested, grid spatial.Point) []string {
	if math.Abs(requested.Longitude-grid.Longitude) <= 1e-9 &&
		math.Abs(requested.Latitude-grid.Latitude) <= 1e-9 {
		return nil
	}
	return []string{"model_grid_coordinate_adjusted"}
}

func responseError(format string, arguments ...any) error {
	return fmt.Errorf("%w: Open-Meteo 响应%s", domain.ErrProviderUnavailable, fmt.Sprintf(format, arguments...))
}
