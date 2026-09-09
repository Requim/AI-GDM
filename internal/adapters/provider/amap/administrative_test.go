package amap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestAdministrativeBoundaryConvertsCoordinatesAndOmitsKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config/district" || r.URL.Query().Get("keywords") != "110000" {
			t.Error("行政区请求未使用确定代码")
		}
		_, _ = w.Write([]byte(`{"status":"1","districts":[{"name":"北京市","adcode":"110000","level":"province",
			"polyline":"116.410244,39.916404;116.42,39.916404;116.42,39.93;116.410244,39.916404"}]}`))
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	record, err := normalizeDistrict(districtRecord{Adcode: "110000", Name: "北京市", Level: "province"}, "")
	if err != nil {
		t.Fatal(err)
	}
	value, err := provider.AdministrativeBoundary(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if value.Code != "CN-110000" || value.Level != "ADM1" || len(value.Digest) != 64 ||
		strings.Contains(value.Reference, "key=") || strings.Contains(value.Reference, "test-key") {
		t.Fatal("边界身份或来源脱敏失败")
	}
	var geometry spatial.Geometry
	var points [][][][]float64
	if json.Unmarshal(value.Geometry, &geometry) != nil || geometry.ValidateArea() != nil ||
		json.Unmarshal(geometry.Coordinates, &points) != nil {
		t.Fatal("边界几何无效")
	}
	if points[0][0][0][0] == 116.410244 {
		t.Fatal("GCJ-02 边界未转换为 WGS84")
	}
}

func TestDistrictGeometryRejectsMissingAndInvalidRings(t *testing.T) {
	provider := newTestProvider(t, "https://restapi.amap.com")
	for _, value := range []string{"", "116,39;116,40", "invalid,39;117,39;117,40"} {
		if _, err := provider.districtGeometry(value); err == nil {
			t.Fatalf("接受了无效边界: %q", value)
		}
	}
}
