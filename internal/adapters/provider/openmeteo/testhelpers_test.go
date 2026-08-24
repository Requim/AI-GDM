package openmeteo

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
)

var fixtureStart = time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)

func validResponse(latitude, longitude float64, hours int) apiResponse {
	offset := 0
	return apiResponse{
		Latitude: &latitude, Longitude: &longitude, UTCOffsetSeconds: &offset,
		Timezone: "GMT", TimezoneAbbreviation: "GMT", HourlyUnits: validUnits(),
		Hourly: apiHourly{
			Time: hourlyTimes(hours), Precipitation: testValues(hours, 0.4, 0.1),
			Rain: testValues(hours, 0.3, 0.1), Showers: testValues(hours, 0.1, 0),
			SoilMoisture0To1CM:   testValues(hours, 0.20, 0.01),
			SoilMoisture1To3CM:   testValues(hours, 0.25, 0.01),
			SoilMoisture3To9CM:   testValues(hours, 0.30, 0.01),
			SoilMoisture9To27CM:  testValues(hours, 0.35, 0.01),
			SoilMoisture27To81CM: testValues(hours, 0.40, 0.01),
		},
	}
}

func validUnits() apiHourlyUnits {
	return apiHourlyUnits{
		Time: "iso8601", Precipitation: "mm", Rain: "mm", Showers: "mm",
		SoilMoisture0To1CM: soilMoistureUnit, SoilMoisture1To3CM: soilMoistureUnit,
		SoilMoisture3To9CM: soilMoistureUnit, SoilMoisture9To27CM: soilMoistureUnit,
		SoilMoisture27To81CM: soilMoistureUnit,
	}
}

func hourlyTimes(hours int) []string {
	result := make([]string, hours)
	for index := range result {
		result[index] = fixtureStart.Add(time.Duration(index) * time.Hour).Format(hourlyTimeLayout)
	}
	return result
}

func testValues(hours int, initial, step float64) []*float64 {
	result := make([]*float64, hours)
	for index := range result {
		value := initial + float64(index)*step
		result[index] = &value
	}
	return result
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func newTestProvider(baseURL, apiKey string, now time.Time) *Provider {
	return newConfiguredTestProvider(Config{BaseURL: baseURL, APIKey: apiKey}, now)
}

func newConfiguredTestProvider(config Config, now time.Time) *Provider {
	client := httpclient.New(httpclient.Options{
		AllowHTTP: true,
		Now:       func() time.Time { return now },
	})
	provider := New(client, config)
	provider.now = func() time.Time { return now }
	return provider
}

func assertQueryContract(
	t *testing.T,
	query url.Values,
	pastHours, forecastHours int,
	apiKey string,
) {
	t.Helper()
	expected := map[string]string{
		"hourly": strings.Join(hourlyVariables, ","), "timezone": "GMT",
		"timeformat": "iso8601", "precipitation_unit": "mm",
		"forecast_hours": strconv.Itoa(forecastHours), "apikey": apiKey,
	}
	for key, value := range expected {
		if query.Get(key) != value {
			t.Errorf("%s = %q，预期 %q", key, query.Get(key), value)
		}
	}
	if pastHours > 0 && query.Get("past_hours") != strconv.Itoa(pastHours) {
		t.Errorf("past_hours = %q", query.Get("past_hours"))
	}
	if pastHours == 0 && query.Has("past_hours") {
		t.Errorf("past_hours = %q，预期省略", query.Get("past_hours"))
	}
}

func writeJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
