package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

type precipitationWindow struct {
	count int
	total float64
	start time.Time
	end   time.Time
}

func buildWeatherFactors(input Input) (ContextStatus, []Factor, []string) {
	if len(input.WeatherContexts) == 0 {
		return ContextAbsent, nil, nil
	}
	contexts := append([]WeatherContext(nil), input.WeatherContexts...)
	sort.Slice(contexts, func(left, right int) bool {
		return weatherContextID(contexts[left]) < weatherContextID(contexts[right])
	})
	factors, limitations := make([]Factor, 0), make([]string, 0)
	fallbackCount, unavailableCount := 0, 0
	for _, context := range contexts {
		dataStatus := weatherDataStatus(context.Snapshot, input.EvaluatedAt)
		if dataStatus == DataExpired {
			unavailableCount++
			factors = append(factors, unavailableWeatherFactor(context))
			limitations = append(limitations, "存在超出有效期的气象上下文，未使用其数值")
			continue
		}
		if dataStatus == DataFallback {
			fallbackCount++
		}
		factors = append(factors, precipitationFactors(context, input.EvaluatedAt, dataStatus)...)
		factors = append(factors, soilMoistureFactor(context, input.EvaluatedAt, dataStatus))
	}
	return aggregateContextStatus(len(contexts), fallbackCount, unavailableCount), factors, limitations
}

func aggregateContextStatus(total, fallback, unavailable int) ContextStatus {
	if unavailable == total {
		return ContextUnavailable
	}
	if unavailable > 0 {
		return ContextPartial
	}
	if fallback > 0 {
		return ContextFallback
	}
	return ContextCurrent
}

func precipitationFactors(context WeatherContext, evaluatedAt time.Time,
	status DataStatus,
) []Factor {
	observed, forecast := precipitationWindows(context.Snapshot.Hourly, evaluatedAt)
	result := make([]Factor, 0, 2)
	if observed.count > 0 {
		result = append(result, precipitationFactor(
			"observed_precipitation", "历史总降水", context, observed, status))
	}
	if forecast.count > 0 {
		result = append(result, precipitationFactor(
			"forecast_precipitation", "预报总降水", context, forecast, status))
	}
	return result
}

func precipitationWindows(values []hazard.WeatherPoint,
	evaluatedAt time.Time,
) (precipitationWindow, precipitationWindow) {
	var observed, forecast precipitationWindow
	for _, value := range values {
		target := &forecast
		if value.Time.Before(evaluatedAt) {
			target = &observed
		}
		target.add(value.Time, value.PrecipitationMM)
	}
	return observed, forecast
}

func (w *precipitationWindow) add(at time.Time, value float64) {
	if w.count == 0 {
		w.start = at
	}
	w.count++
	w.total += value
	w.end = at
}

func precipitationFactor(code, label string, context WeatherContext,
	window precipitationWindow, status DataStatus,
) Factor {
	return Factor{
		Code: code, Role: FactorContextOnly, AffectsLevel: false, DataStatus: status,
		ContextID: weatherContextID(context),
		Description: fmt.Sprintf("%s；由调用方通过%s选定，仅作上下文，不参与等级加权",
			label, context.SelectionMethod),
		Metrics: []Metric{
			{Code: "precipitation_total", Label: label, Value: window.total, Unit: "mm"},
			{Code: "hour_count", Label: "逐小时样本数", Value: float64(window.count), Unit: "count"},
		},
		WindowStart: window.start, WindowEnd: window.end,
		InputReferences: weatherReferences(context.Snapshot),
	}
}

func soilMoistureFactor(context WeatherContext, evaluatedAt time.Time,
	status DataStatus,
) Factor {
	point := soilMoisturePoint(context.Snapshot.Hourly, evaluatedAt)
	labels := []string{"0-1 cm", "1-3 cm", "3-9 cm", "9-27 cm", "27-81 cm"}
	metrics := make([]Metric, len(labels))
	for index, label := range labels {
		metrics[index] = Metric{
			Code: fmt.Sprintf("soil_moisture_layer_%d", index+1), Label: label,
			Value: point.SoilMoistureByLayer[index], Unit: "m3/m3",
		}
	}
	return Factor{
		Code: "soil_moisture_profile", Role: FactorContextOnly, AffectsLevel: false,
		DataStatus: status, ContextID: weatherContextID(context),
		Description: fmt.Sprintf("五层体积含水率原值；由调用方通过%s选定，不求平均且不参与等级加权",
			context.SelectionMethod),
		Metrics: metrics, WindowStart: point.Time, WindowEnd: point.Time,
		InputReferences: weatherReferences(context.Snapshot),
	}
}

func soilMoisturePoint(values []hazard.WeatherPoint, evaluatedAt time.Time) hazard.WeatherPoint {
	selected := values[0]
	for _, value := range values {
		if value.Time.After(evaluatedAt) {
			break
		}
		selected = value
	}
	return selected
}

func unavailableWeatherFactor(context WeatherContext) Factor {
	return Factor{
		Code: "weather_context_unavailable", Role: FactorDataQuality, AffectsLevel: false,
		DataStatus: DataExpired, ContextID: weatherContextID(context),
		Description:     "气象上下文已超出有效期，未使用其降水和土壤湿度数值",
		InputReferences: weatherReferences(context.Snapshot),
	}
}

func weatherDataStatus(snapshot hazard.WeatherSnapshot, evaluatedAt time.Time) DataStatus {
	if evaluatedAt.Before(snapshot.Source.ValidFrom) || evaluatedAt.After(snapshot.Source.ValidTo) {
		return DataExpired
	}
	if snapshot.Source.Stale || hasQualityFlag(snapshot.Source.QualityFlags, fallbackQualityFlag) {
		return DataFallback
	}
	return DataCurrent
}

func weatherContextID(context WeatherContext) string {
	payload := weatherLocationKey(context.Snapshot) + "|" + context.SelectionMethod + "|" +
		context.Snapshot.Source.SHA256
	digest := sha256.Sum256([]byte(payload))
	return "weather-" + hex.EncodeToString(digest[:6])
}
