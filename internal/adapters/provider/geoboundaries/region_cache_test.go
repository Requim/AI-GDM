package geoboundaries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
)

func TestCachedRegionCatalogRefreshAndReadAvoidsNetworkOnPageRequest(t *testing.T) {
	provider, requests := regionProviderForCache(t, false)
	cache, err := NewCachedRegionCatalog(provider, filepath.Join(t.TempDir(), "regions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshRequests := requests.Load()
	regions, err := cache.RegionCatalog(context.Background(), "CHN", "ADM1")
	if err != nil || len(regions) != 1 || regions[0].Code != "CHN-ADM1-1" {
		t.Fatalf("RegionCatalog()=%+v error=%v", regions, err)
	}
	boundary, err := cache.BoundaryForRegion(context.Background(), "CHN-ADM2-2")
	if err != nil || boundary.RegionCode != "CHN-ADM2-2" {
		t.Fatalf("BoundaryForRegion()=%+v error=%v", boundary, err)
	}
	if requests.Load() != refreshRequests {
		t.Fatalf("本地缓存读取触发网络请求: before=%d after=%d", refreshRequests, requests.Load())
	}
}

func TestCachedRegionCatalogKeepsPreviousCacheAfterPartialRefreshFailure(t *testing.T) {
	provider, _ := regionProviderForCache(t, false)
	path := filepath.Join(t.TempDir(), "regions.json")
	cache, err := NewCachedRegionCatalog(provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	failing, _ := regionProviderForCache(t, true)
	cache, err = NewCachedRegionCatalog(failing, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Refresh(context.Background()); err == nil {
		t.Fatal("ADM2 刷新失败时未返回错误")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("部分刷新失败后替换了旧缓存")
	}
	if err = cache.Validate(context.Background()); err != nil {
		t.Fatalf("旧缓存被破坏: %v", err)
	}
}

func TestCachedRegionCatalogRejectsTamperedPayload(t *testing.T) {
	provider, _ := regionProviderForCache(t, false)
	cache, err := NewCachedRegionCatalog(provider, filepath.Join(t.TempDir(), "regions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	var stored regionCatalogCacheFile
	if err = json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	stored.ADM1.Digest = strings.Repeat("0", 64)
	payload, err = json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cache.path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = cache.Validate(context.Background()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("篡改缓存错误=%v", err)
	}
}

func regionProviderForCache(t *testing.T, failADM2 bool) (*Provider, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	adm1Payload := regionCollectionPayload("1", "省一", "ADM1")
	adm2Payload := regionCollectionPayload("2", "市一", "ADM2")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		switch request.URL.String() {
		case "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM1/":
			return boundaryResponse(request, http.StatusOK, regionMetadataPayload("ADM1"), ""), nil
		case "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM2/":
			if failADM2 {
				return nil, fmt.Errorf("模拟 ADM2 网络失败")
			}
			return boundaryResponse(request, http.StatusOK, regionMetadataPayload("ADM2"), ""), nil
		case "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/abcdef1/releaseData/gbOpen/CHN/ADM1/geoBoundaries-CHN-ADM1_simplified.geojson":
			return regionRangeResponse(request, adm1Payload)
		case "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/abcdef1/releaseData/gbOpen/CHN/ADM2/geoBoundaries-CHN-ADM2_simplified.geojson":
			return regionRangeResponse(request, adm2Payload)
		default:
			return nil, fmt.Errorf("意外的缓存测试请求: %s", request.URL)
		}
	})
	client := httpclient.New(httpclient.Options{
		HTTPClient:  &http.Client{Transport: transport},
		MaxAttempts: 1,
		Now:         func() time.Time { return time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC) },
	})
	provider, err := New(Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	return provider, requests
}

func regionRangeResponse(request *http.Request, payload []byte) (*http.Response, error) {
	value := strings.TrimPrefix(request.Header.Get("Range"), "bytes=")
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("测试 Range 请求无效: %q", value)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= int64(len(payload)) {
		return nil, fmt.Errorf("测试 Range 起点无效: %q", value)
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return nil, fmt.Errorf("测试 Range 终点无效: %q", value)
	}
	if end >= int64(len(payload)) {
		end = int64(len(payload)) - 1
	}
	headers := make(http.Header)
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
	return &http.Response{
		StatusCode: http.StatusPartialContent, Header: headers,
		Body:    io.NopCloser(strings.NewReader(string(payload[start : end+1]))),
		Request: request,
	}, nil
}

func regionMetadataPayload(level string) string {
	year, source, license := "2019", expectedSource, expectedLicense
	if level == "ADM2" {
		year = "2017"
		source = "National Administration of Surveying, Mapping and Geoinformation of China, Revolutionary GIS"
		license = "Open Data Commons Public Domain Dedication and License (PDDL) v1.0"
	}
	return `{"boundaryYearRepresented":"` + year + `","boundarySource":"` + source +
		`","boundaryLicense":"` + license +
		`","simplifiedGeometryGeoJSON":"https://github.com/wmgeolab/geoBoundaries/raw/abcdef1/releaseData/gbOpen/CHN/` +
		level + `/geoBoundaries-CHN-` + level + `_simplified.geojson"}`
}

func regionCollectionPayload(shapeID, name, level string) []byte {
	return []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"shapeID":"` +
		shapeID + `","shapeName":"` + name + `","shapeISO":"CHN-` + level + `-` + shapeID +
		`","shapeGroup":"CHN","shapeType":"` + level +
		`"},"geometry":{"type":"Polygon","coordinates":[]}}]}`)
}
