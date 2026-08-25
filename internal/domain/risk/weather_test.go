package risk

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestWeatherContextNeverChangesDecisionOrPrimaryConfidence(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	withoutWeather := validInput(now)
	baseline, err := landslideEngine().Evaluate(withoutWeather)
	if err != nil {
		t.Fatal(err)
	}
	withWeather := validInput(now)
	withWeather.WeatherContexts = []WeatherContext{validWeatherContext(now, 104.1, 30.6, "风险区包含关系")}
	got, err := landslideEngine().Evaluate(withWeather)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Decision, baseline.Decision) ||
		!reflect.DeepEqual(got.Confidence, baseline.Confidence) {
		t.Fatalf("气象上下文改变了等级或主模型置信度: %+v / %+v", got, baseline)
	}
	if got.ContextStatus != ContextCurrent {
		t.Fatalf("ContextStatus = %s", got.ContextStatus)
	}
	assertPrecipitationFactor(t, got.Factors, "observed_precipitation", 3, 2)
	assertPrecipitationFactor(t, got.Factors, "forecast_precipitation", 7, 2)
	assertSoilMoistureFactor(t, got.Factors, []float64{0.12, 0.22, 0.32, 0.42, 0.52})
}

func TestWeatherContextFallbackAndExpiration(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	context := validWeatherContext(now, 104.1, 30.6, "最近监测点")
	context.Snapshot.Source.Stale = true
	context.Snapshot.Source.QualityFlags = []string{fallbackQualityFlag}
	input.WeatherContexts = []WeatherContext{context}
	fallback, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.ContextStatus != ContextFallback || fallback.DataStatus != DataCurrent ||
		fallback.Decision == nil || fallback.Confidence.Level != ConfidenceMedium {
		t.Fatalf("fallback weather = %+v", fallback)
	}
	input.EvaluatedAt = context.Snapshot.Source.ValidTo.Add(time.Nanosecond)
	expired, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if expired.ContextStatus != ContextUnavailable || expired.Decision == nil ||
		!hasFactor(expired.Factors, "weather_context_unavailable") ||
		hasFactor(expired.Factors, "forecast_precipitation") {
		t.Fatalf("expired weather = %+v", expired)
	}
}

func TestWeatherContextMixedAvailabilityIsPartial(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	input := validInput(now)
	current := validWeatherContext(now, 104.1, 30.6, "风险区包含关系")
	expired := validWeatherContext(now.Add(-4*time.Hour), 102.7, 25.0, "最近监测点")
	expired.Snapshot.Source.SHA256 = strings.Repeat("c", 64)
	input.WeatherContexts = []WeatherContext{expired, current}
	got, err := landslideEngine().Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextStatus != ContextPartial || got.Decision == nil ||
		!hasFactor(got.Factors, "weather_context_unavailable") ||
		!hasFactor(got.Factors, "observed_precipitation") {
		t.Fatalf("mixed weather = %+v", got)
	}
}

func TestWeatherContextOrderingIsDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	firstContext := validWeatherContext(now, 104.1, 30.6, "风险区包含关系")
	secondContext := validWeatherContext(now, 102.7, 25.0, "风险区包含关系")
	secondContext.Snapshot.Source.SHA256 = strings.Repeat("c", 64)
	firstInput := validInput(now)
	firstInput.WeatherContexts = []WeatherContext{firstContext, secondContext}
	secondInput := validInput(now)
	secondInput.WeatherContexts = []WeatherContext{secondContext, firstContext}
	first, err := landslideEngine().Evaluate(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := landslideEngine().Evaluate(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("气象上下文顺序改变了结果\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestWeatherSelectionMethodParticipatesInAssessmentIdentity(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	firstInput := validInput(now)
	firstInput.WeatherContexts = []WeatherContext{validWeatherContext(now, 104.1, 30.6, "风险区包含关系")}
	secondInput := validInput(now)
	secondInput.WeatherContexts = []WeatherContext{validWeatherContext(now, 104.1, 30.6, "最近监测点")}
	first, err := landslideEngine().Evaluate(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := landslideEngine().Evaluate(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("不同空间选择方法生成了相同评估标识")
	}
}

func TestWeatherContextRejectsDuplicateOrInvalidValues(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Input)
	}{
		{name: "duplicate-location", mutate: func(input *Input) {
			context := validWeatherContext(now, 104.1, 30.6, "风险区包含关系")
			input.WeatherContexts = []WeatherContext{context, context}
		}},
		{name: "missing-selection-method", mutate: func(input *Input) {
			context := validWeatherContext(now, 104.1, 30.6, "")
			input.WeatherContexts = []WeatherContext{context}
		}},
		{name: "invalid-soil-layer", mutate: func(input *Input) {
			context := validWeatherContext(now, 104.1, 30.6, "风险区包含关系")
			context.Snapshot.Hourly[0].SoilMoistureByLayer[0] = 1.1
			input.WeatherContexts = []WeatherContext{context}
		}},
		{name: "fallback-marker-mismatch", mutate: func(input *Input) {
			context := validWeatherContext(now, 104.1, 30.6, "风险区包含关系")
			context.Snapshot.Source.Stale = true
			input.WeatherContexts = []WeatherContext{context}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(now)
			test.mutate(&input)
			if _, err := landslideEngine().Evaluate(input); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Evaluate() error = %v", err)
			}
		})
	}
}

func validWeatherContext(now time.Time, longitude, latitude float64,
	selection string,
) WeatherContext {
	times := []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour), now, now.Add(time.Hour)}
	precipitation := []float64{1, 2, 3, 4}
	points := make([]hazard.WeatherPoint, len(times))
	for index, value := range times {
		points[index] = hazard.WeatherPoint{
			Time: value, PrecipitationMM: precipitation[index],
			RainMM: 100 + float64(index), ShowersMM: 200 + float64(index),
			SoilMoistureByLayer: []float64{
				0.1 + float64(index)/100, 0.2 + float64(index)/100,
				0.3 + float64(index)/100, 0.4 + float64(index)/100,
				0.5 + float64(index)/100,
			},
		}
	}
	return WeatherContext{
		SelectionMethod: selection,
		Snapshot: hazard.WeatherSnapshot{
			Location: spatial.Point{Longitude: longitude, Latitude: latitude}, Hourly: points,
			Source: provenance.Provenance{
				Provider: "Open-Meteo", Dataset: "Weather Forecast API",
				SourceURI: "https://example.test/weather", DataKind: provenance.DataKindForecast,
				FetchedAt: now.Add(-time.Minute), ValidFrom: times[0], ValidTo: times[len(times)-1].Add(time.Hour),
				SHA256: strings.Repeat("b", 64), TransformVersion: "openmeteo-adapter-v1",
			},
		},
	}
}

func assertPrecipitationFactor(t *testing.T, factors []Factor, code string,
	total, hours float64,
) {
	t.Helper()
	factor := findFactor(t, factors, code)
	if factor.AffectsLevel || factor.Role != FactorContextOnly {
		t.Fatalf("factor %s affects level: %+v", code, factor)
	}
	if metricValue(t, factor.Metrics, "precipitation_total") != total ||
		metricValue(t, factor.Metrics, "hour_count") != hours {
		t.Fatalf("factor %s metrics = %+v", code, factor.Metrics)
	}
}

func assertSoilMoistureFactor(t *testing.T, factors []Factor, expected []float64) {
	t.Helper()
	factor := findFactor(t, factors, "soil_moisture_profile")
	if len(factor.Metrics) != 5 || factor.AffectsLevel {
		t.Fatalf("soil moisture factor = %+v", factor)
	}
	for index, value := range expected {
		if math.Abs(factor.Metrics[index].Value-value) > 1e-9 || factor.Metrics[index].Unit != "m3/m3" {
			t.Fatalf("soil moisture metrics = %+v", factor.Metrics)
		}
	}
}

func findFactor(t *testing.T, factors []Factor, code string) Factor {
	t.Helper()
	for _, factor := range factors {
		if factor.Code == code {
			return factor
		}
	}
	t.Fatalf("factor %s not found", code)
	return Factor{}
}

func metricValue(t *testing.T, values []Metric, code string) float64 {
	t.Helper()
	for _, value := range values {
		if value.Code == code {
			return value.Value
		}
	}
	t.Fatalf("metric %s not found", code)
	return 0
}

func hasFactor(values []Factor, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
