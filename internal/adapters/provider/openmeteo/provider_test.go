package openmeteo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestForecastSinglePointContract(t *testing.T) {
	body := mustJSON(t, validResponse(39.875, 116.375, 2))
	requests := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query()
		w.Header().Set("X-Request-ID", "weather-request-1")
		writeJSON(w, body)
	}))
	defer server.Close()

	now := fixtureStart.Add(30 * time.Minute)
	provider := newTestProvider(server.URL, "fixture-key", now)
	point := spatial.Point{Longitude: 116.4, Latitude: 39.9}
	snapshots, err := provider.Forecast(context.Background(), []spatial.Point{point}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("快照数量 = %d", len(snapshots))
	}
	query := <-requests
	assertQueryContract(t, query, 1, 1, "fixture-key")
	if query.Get("latitude") != "39.9" || query.Get("longitude") != "116.4" {
		t.Fatalf("坐标查询 = %q, %q", query.Get("latitude"), query.Get("longitude"))
	}
	assertSingleSnapshot(t, snapshots[0], point, body)
}

func assertSingleSnapshot(
	t *testing.T,
	snapshot hazard.WeatherSnapshot,
	point spatial.Point,
	body []byte,
) {
	t.Helper()
	if snapshot.Location != point || len(snapshot.Hourly) != 2 {
		t.Fatalf("快照 = %+v", snapshot)
	}
	first := snapshot.Hourly[0]
	if first.PrecipitationMM != 0.4 || first.RainMM != 0.3 || first.ShowersMM != 0.1 {
		t.Errorf("首小时降水 = %+v", first)
	}
	expectedSoil := []float64{0.20, 0.25, 0.30, 0.35, 0.40}
	if !reflect.DeepEqual(first.SoilMoistureByLayer, expectedSoil) {
		t.Errorf("土壤湿度 = %v", first.SoilMoistureByLayer)
	}
	digest := sha256.Sum256(body)
	source := snapshot.Source
	expectedBBox := [4]float64{116.375, 39.875, 116.375, 39.875}
	if source.SHA256 != hex.EncodeToString(digest[:]) || source.BBox != expectedBBox {
		t.Errorf("来源摘要或 bbox = %+v", source)
	}
	if !source.ValidFrom.Equal(fixtureStart) || !source.ValidTo.Equal(fixtureStart.Add(2*time.Hour)) {
		t.Errorf("有效期 = %s 至 %s", source.ValidFrom, source.ValidTo)
	}
	if source.Stale || source.ProviderRequestID != "weather-request-1" {
		t.Errorf("来源状态 = %+v", source)
	}
	if strings.Contains(source.SourceURI, "fixture-key") {
		t.Fatalf("来源 URL 泄露 API 密钥: %s", source.SourceURI)
	}
	parsed, err := url.Parse(source.SourceURI)
	if err != nil || parsed.Query().Get("apikey") != "REDACTED" {
		t.Fatalf("脱敏来源 URL = %q，错误 = %v", source.SourceURI, err)
	}
	if err = source.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestForecastBatchesMultiplePointsAndAcceptsArray(t *testing.T) {
	points := makePoints(26)
	var (
		mu         sync.Mutex
		batchSizes []int
		queries    []url.Values
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := responsesForQuery(r.URL.Query(), 2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		batchSizes = append(batchSizes, len(values))
		queries = append(queries, r.URL.Query())
		mu.Unlock()
		body, _ := json.Marshal(values)
		writeJSON(w, body)
	}))
	defer server.Close()

	provider := newTestProvider(server.URL, "", fixtureStart)
	snapshots, err := provider.Forecast(context.Background(), points, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != len(points) || !reflect.DeepEqual(batchSizes, []int{25, 1}) {
		t.Fatalf("快照数量 = %d，批次 = %v", len(snapshots), batchSizes)
	}
	for _, query := range queries {
		assertQueryContract(t, query, 0, 2, "")
		if query.Has("apikey") {
			t.Errorf("未配置密钥时仍发送 apikey")
		}
	}
	for index, snapshot := range snapshots {
		if snapshot.Location != points[index] || len(snapshot.Hourly) != 2 {
			t.Fatalf("第 %d 个快照 = %+v", index, snapshot)
		}
	}
}

func responsesForQuery(query url.Values, hours int) ([]apiResponse, error) {
	latitudes := strings.Split(query.Get("latitude"), ",")
	longitudes := strings.Split(query.Get("longitude"), ",")
	if len(latitudes) != len(longitudes) {
		return nil, errors.New("经纬度数量不一致")
	}
	result := make([]apiResponse, len(latitudes))
	for index := range latitudes {
		latitude, err := strconv.ParseFloat(latitudes[index], 64)
		if err != nil {
			return nil, err
		}
		longitude, err := strconv.ParseFloat(longitudes[index], 64)
		if err != nil {
			return nil, err
		}
		result[index] = validResponse(latitude+0.001, longitude+0.001, hours)
	}
	return result, nil
}

func TestForecastUsesConfiguredBatchSize(t *testing.T) {
	points := makePoints(5)
	batchSizes := make(chan int, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := responsesForQuery(r.URL.Query(), 2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		batchSizes <- len(values)
		body, _ := json.Marshal(values)
		writeJSON(w, body)
	}))
	defer server.Close()

	provider := newConfiguredTestProvider(Config{
		BaseURL: server.URL, MaxPointsPerRequest: 2,
	}, fixtureStart)
	snapshots, err := provider.Forecast(context.Background(), points, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := []int{<-batchSizes, <-batchSizes, <-batchSizes}
	if len(snapshots) != len(points) || !reflect.DeepEqual(got, []int{2, 2, 1}) {
		t.Fatalf("快照数量 = %d，批次 = %v", len(snapshots), got)
	}
}

func TestNewNormalizesConfiguredBatchSize(t *testing.T) {
	cases := []struct {
		value, expected int
	}{
		{value: 0, expected: 25},
		{value: -1, expected: 25},
		{value: 1, expected: 1},
		{value: 25, expected: 25},
		{value: 26, expected: 25},
	}
	for _, test := range cases {
		provider := New(nil, Config{MaxPointsPerRequest: test.value})
		if provider.maxPointsPerRequest != test.expected {
			t.Errorf("MaxPointsPerRequest(%d) = %d", test.value, provider.maxPointsPerRequest)
		}
	}
}

func TestForecastRejectsInvalidQuery(t *testing.T) {
	cases := []struct {
		name           string
		points         []spatial.Point
		past, forecast int
	}{
		{name: "坐标为空", points: nil, forecast: 1},
		{name: "坐标过多", points: makePoints(101), forecast: 1},
		{name: "坐标越界", points: []spatial.Point{{Longitude: 181}}, forecast: 1},
		{name: "坐标非有限", points: []spatial.Point{{Longitude: math.NaN()}}, forecast: 1},
		{name: "历史小时为负", points: makePoints(1), past: -1, forecast: 1},
		{name: "预报小时非正", points: makePoints(1), forecast: 0},
	}
	provider := newTestProvider("http://127.0.0.1:1", "", fixtureStart)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := provider.Forecast(context.Background(), test.points, test.past, test.forecast)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Forecast() error = %v", err)
			}
		})
	}
}

func makePoints(count int) []spatial.Point {
	result := make([]spatial.Point, count)
	for index := range result {
		result[index] = spatial.Point{
			Longitude: 100 + float64(index)/10,
			Latitude:  20 + float64(index)/100,
		}
	}
	return result
}
