package amap

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestLiveFindNearbyAndWalkingPlan(t *testing.T) {
	if os.Getenv("AMAP_LIVE_TEST") != "1" {
		t.Skip("未启用真实高德 Web 服务契约测试")
	}
	apiKey := requiredAMapLiveEnv(t, "AMAP_API_KEY")
	securityCode := strings.TrimSpace(os.Getenv("AMAP_JSCODE"))
	client := httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxAttempts: 1,
	})
	provider, err := New(client, Config{
		APIKey: apiKey, SecurityCode: securityCode, Timeout: 25 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	origin := spatial.Point{Longitude: 116.417, Latitude: 39.908}
	destination := spatial.Point{Longitude: 116.419, Latitude: 39.9085}
	facilities, err := provider.FindNearby(ctx, origin, evacuation.FacilityHospital, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	assertLiveFacilities(t, facilities, apiKey, securityCode)
	routes, err := provider.Plan(ctx, origin, destination, evacuation.TravelWalking)
	if err != nil {
		t.Fatal(err)
	}
	assertLiveWalkingRoutes(t, routes, origin, destination, apiKey, securityCode)
	t.Logf("高德在线契约通过：医院候选=%d，步行候选=%d", len(facilities), len(routes))
}

func assertLiveFacilities(t *testing.T, values []evacuation.Facility, apiKey, securityCode string) {
	t.Helper()
	if len(values) == 0 || len(values) > maxPageSize {
		t.Fatalf("高德医院候选数量无效: %d", len(values))
	}
	for index, value := range values {
		if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Name) == "" ||
			value.Type != evacuation.FacilityHospital || value.DistanceMeters < 0 ||
			math.IsNaN(value.DistanceMeters) || math.IsInf(value.DistanceMeters, 0) {
			t.Fatalf("第 %d 个高德医院候选领域字段无效", index+1)
		}
		if err := value.Location.Validate(); err != nil {
			t.Fatalf("第 %d 个高德医院候选坐标无效: %v", index+1, err)
		}
		assertLiveAMapSource(t, value.Source, "/v5/place/around", apiKey, securityCode)
	}
}

func assertLiveWalkingRoutes(t *testing.T, values []evacuation.Route, origin, destination spatial.Point,
	apiKey, securityCode string,
) {
	t.Helper()
	if len(values) == 0 {
		t.Fatal("高德未返回短距离步行候选路线")
	}
	for index, value := range values {
		if err := value.Validate(); err != nil {
			t.Fatalf("第 %d 条高德步行候选路线领域校验失败: %v", index+1, err)
		}
		if value.Mode != evacuation.TravelWalking || value.Origin != origin || value.Destination != destination ||
			value.DistanceMeters > 3_000 || len(value.Steps) == 0 || len(value.Limitations) == 0 {
			t.Fatalf("第 %d 条高德步行候选路线领域字段无效", index+1)
		}
		assertLiveGeometry(t, value.Geometry, index)
		assertLiveAMapSource(t, value.Source, "/v5/direction/walking", apiKey, securityCode)
	}
}

func assertLiveGeometry(t *testing.T, geometry spatial.Geometry, routeIndex int) {
	t.Helper()
	if geometry.Type != "LineString" {
		t.Fatalf("第 %d 条高德步行路线几何类型无效", routeIndex+1)
	}
	var coordinates [][2]float64
	if err := json.Unmarshal(geometry.Coordinates, &coordinates); err != nil || len(coordinates) < 2 {
		t.Fatalf("第 %d 条高德步行路线几何无效", routeIndex+1)
	}
	for _, coordinate := range coordinates {
		point := spatial.Point{Longitude: coordinate[0], Latitude: coordinate[1]}
		if err := point.Validate(); err != nil {
			t.Fatalf("第 %d 条高德步行路线包含无效 WGS84 坐标: %v", routeIndex+1, err)
		}
	}
}

func assertLiveAMapSource(t *testing.T, source provenance.Provenance, path, apiKey, securityCode string) {
	t.Helper()
	if err := source.Validate(); err != nil {
		t.Fatalf("高德在线来源元数据无效: %v", err)
	}
	parsed, err := url.Parse(source.SourceURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "restapi.amap.com" || parsed.Path != path {
		t.Fatal("高德在线来源地址无效")
	}
	query := parsed.Query()
	if source.Provider != providerName || source.Dataset != datasetName || source.CRS != "WGS84" ||
		source.DataKind != provenance.DataKindObservation || query.Get("key") != "REDACTED" ||
		strings.Contains(source.SourceURI, apiKey) {
		t.Fatal("高德在线来源身份或密钥脱敏无效")
	}
	if securityCode == "" && query.Has("jscode") {
		t.Fatal("未配置 AMAP_JSCODE 时来源地址仍包含该参数")
	}
	if securityCode != "" && (query.Get("jscode") != "REDACTED" || strings.Contains(source.SourceURI, securityCode)) {
		t.Fatal("高德在线来源安全密钥脱敏无效")
	}
}

func requiredAMapLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("真实高德 Web 服务契约测试缺少环境变量 %s", name)
	}
	return value
}
