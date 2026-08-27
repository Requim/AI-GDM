package amap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestFindNearbyMapsPOIAndRedactsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("key") != "test-key" || query.Get("jscode") != "test-code" {
			t.Fatalf("密钥参数未正确注入: %v", query)
		}
		if query.Get("keywords") != "医院" || query.Get("extensions") != "all" {
			t.Fatalf("POI 参数错误: %v", query)
		}
		w.Header().Set("X-Request-ID", "amap-request-1")
		_, _ = w.Write([]byte(`{"status":"1","pois":[{"id":"B001","name":"测试医院","address":"测试路1号","location":"116.410244,39.916404","distance":"1200"}]}`))
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	result, err := provider.FindNearby(context.Background(), spatial.Point{Longitude: 116.397128, Latitude: 39.916527}, evacuation.FacilityHospital, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].ID != "B001" || result[0].DistanceMeters != 1200 {
		t.Fatalf("设施结果错误: %+v", result)
	}
	if result[0].Source.ProviderRequestID != "amap-request-1" ||
		strings.Contains(result[0].Source.SourceURI, "test-key") ||
		strings.Contains(result[0].Source.SourceURI, "test-code") {
		t.Fatalf("来源信息错误或包含密钥: %+v", result[0].Source)
	}
}

func TestPlanMapsDrivingRouteToWGS84(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v5/direction/driving" {
			t.Fatalf("路线路径错误: %s", request.URL.Path)
		}
		if request.URL.Query().Get("show_fields") != "cost,polyline" {
			t.Fatalf("路线字段参数错误: %s", request.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"1","route":{"paths":[{"distance":"1800","duration":"420","steps":[{"instruction":"向北行驶","road_name":"测试路","distance":"800","polyline":"116.410244,39.916404;116.411000,39.920000"},{"instruction":"到达终点","road_name":"终点路","distance":"1000","polyline":"116.411000,39.920000;116.420000,39.925000"}]}]}}`))
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	result, err := provider.Plan(context.Background(), spatial.Point{Longitude: 116.397128, Latitude: 39.916527}, spatial.Point{Longitude: 116.410000, Latitude: 39.930000}, evacuation.TravelDriving)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].DistanceMeters != 1800 || result[0].DurationSeconds != 420 {
		t.Fatalf("路线结果错误: %+v", result)
	}
	if result[0].Geometry.Type != "LineString" || strings.Contains(string(result[0].Geometry.Coordinates), "116.410244,39.916404") {
		t.Fatalf("路线几何未转换为 WGS84: %+v", result[0].Geometry)
	}
	if err := result[0].Source.Validate(); err != nil {
		t.Fatalf("路线来源校验失败: %v", err)
	}
}

func TestProviderSanitizesBusinessErrorAndSupportsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v5/place/around" {
			_, _ = w.Write([]byte(`{"status":"0","info":"KEY_INVALID:test-key","infocode":"10001"}`))
			return
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	_, err := provider.FindNearby(context.Background(), spatial.Point{Longitude: 116.4, Latitude: 39.9}, evacuation.FacilityShelter, 100)
	if !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("高德业务错误未正确脱敏: %v", err)
	}
	provider.timeout = time.Millisecond
	_, err = provider.Plan(context.Background(), spatial.Point{Longitude: 116.4, Latitude: 39.9}, spatial.Point{Longitude: 116.5, Latitude: 39.8}, evacuation.TravelWalking)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时错误未保留: %v", err)
	}
}

func TestPlanRejectsTransitWithoutCity(t *testing.T) {
	provider := newTestProvider(t, "https://example.test")
	_, err := provider.Plan(context.Background(), spatial.Point{Longitude: 116.4, Latitude: 39.9}, spatial.Point{Longitude: 116.5, Latitude: 39.8}, evacuation.TravelTransit)
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("公交模式错误 = %v", err)
	}
}

func newTestProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{AllowHTTP: true, MaxAttempts: 1})
	provider, err := New(client, Config{BaseURL: baseURL, APIKey: "test-key", SecurityCode: "test-code"})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
