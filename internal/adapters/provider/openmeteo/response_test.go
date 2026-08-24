package openmeteo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

type responseMutation struct {
	name   string
	mutate func(*apiResponse)
}

var invalidResponseMutations = []responseMutation{
	{name: "非 GMT 时区", mutate: mutateTimezone},
	{name: "降水单位错误", mutate: mutatePrecipitationUnit},
	{name: "土壤湿度单位错误", mutate: mutateSoilMoistureUnit},
	{name: "数组长度不一致", mutate: mutateArrayLength},
	{name: "时间窗口长度错误", mutate: mutateTimeLength},
	{name: "负降水", mutate: mutateNegativePrecipitation},
	{name: "土壤湿度超范围", mutate: mutateSoilMoistureRange},
	{name: "空数值", mutate: mutateNullNumber},
	{name: "小时不连续", mutate: mutateTimeGap},
	{name: "模型网格坐标缺失", mutate: mutateMissingGrid},
	{name: "模型网格坐标越界", mutate: mutateInvalidGrid},
}

func TestForecastRejectsInvalidProviderContract(t *testing.T) {
	for _, test := range invalidResponseMutations {
		t.Run(test.name, func(t *testing.T) {
			response := validResponse(30, 110, 2)
			test.mutate(&response)
			err := forecastFixture(t, mustJSON(t, response), makePoints(1))
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("Forecast() error = %v", err)
			}
		})
	}
}

func TestForecastRejectsInvalidJSONAndBusinessError(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "空响应", body: nil},
		{name: "非法 JSON", body: []byte(`{"latitude":`)},
		{name: "业务错误", body: []byte(`{"error":true,"reason":"quota exceeded"}`)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := forecastFixture(t, test.body, makePoints(1))
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("Forecast() error = %v", err)
			}
		})
	}
}

func TestForecastRejectsResponseLocationCountMismatch(t *testing.T) {
	twoResponses := []apiResponse{
		validResponse(30, 110, 2),
		validResponse(31, 111, 2),
	}
	cases := []struct {
		name   string
		body   []byte
		points []spatial.Point
	}{
		{name: "单点收到两个对象", body: mustJSON(t, twoResponses), points: makePoints(1)},
		{name: "多点收到单对象", body: mustJSON(t, twoResponses[0]), points: makePoints(2)},
		{name: "收到空数组", body: []byte("[]"), points: makePoints(1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := forecastFixture(t, test.body, test.points)
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("Forecast() error = %v", err)
			}
		})
	}
}

func forecastFixture(t *testing.T, body []byte, points []spatial.Point) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, body)
	}))
	defer server.Close()
	provider := newTestProvider(server.URL, "", fixtureStart)
	_, err := provider.Forecast(context.Background(), points, 0, 2)
	return err
}

func mutateTimezone(response *apiResponse) {
	offset := 8 * 60 * 60
	response.UTCOffsetSeconds = &offset
	response.Timezone = "Asia/Shanghai"
	response.TimezoneAbbreviation = "GMT+8"
}

func mutatePrecipitationUnit(response *apiResponse) {
	response.HourlyUnits.Precipitation = "inch"
}

func mutateSoilMoistureUnit(response *apiResponse) {
	response.HourlyUnits.SoilMoisture27To81CM = "percent"
}

func mutateArrayLength(response *apiResponse) {
	response.Hourly.Rain = response.Hourly.Rain[:1]
}

func mutateTimeLength(response *apiResponse) {
	response.Hourly.Time = response.Hourly.Time[:1]
}

func mutateNegativePrecipitation(response *apiResponse) {
	value := -0.1
	response.Hourly.Precipitation[0] = &value
}

func mutateSoilMoistureRange(response *apiResponse) {
	value := 1.01
	response.Hourly.SoilMoisture0To1CM[0] = &value
}

func mutateNullNumber(response *apiResponse) {
	response.Hourly.Showers[0] = nil
}

func mutateTimeGap(response *apiResponse) {
	response.Hourly.Time[1] = "2026-08-24T03:00"
}

func mutateMissingGrid(response *apiResponse) {
	response.Longitude = nil
}

func mutateInvalidGrid(response *apiResponse) {
	longitude := 181.0
	response.Longitude = &longitude
}
