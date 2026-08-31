package overpass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
)

func TestInfrastructureReturnsRealOSMIdentityAndGeometry(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertOverpassRequest(t, request)
		writeOverpassJSON(t, writer, overpassFixture(now.Add(-time.Minute)))
	}))
	defer server.Close()
	provider := testOverpassProvider(t, server, now)
	result, err := provider.Infrastructure(context.Background(), exposurecollection.InfrastructureQuery{
		Bounds: exposurecollection.Bounds{South: 39.90, West: 116.40, North: 39.91, East: 116.41}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Features) != 2 || result.Features[0].FeatureID != "osm-facility-node-22" ||
		result.Features[1].FeatureID != "osm-road-way-11" {
		t.Fatalf("Infrastructure()=%+v", result)
	}
	if result.Features[0].Kind != applicationloss.LossFeatureFacility ||
		result.Features[1].Kind != applicationloss.LossFeatureRoad {
		t.Fatalf("feature kinds=%+v", result.Features)
	}
}

func TestInfrastructureRejectsNationwideBBoxBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	provider := testOverpassProvider(t, server, time.Now().UTC())
	_, err := provider.Infrastructure(context.Background(), exposurecollection.InfrastructureQuery{
		Bounds: exposurecollection.Bounds{South: 18, West: 73, North: 54, East: 135}})
	if err == nil || requests != 0 {
		t.Fatalf("Infrastructure() error=%v requests=%d", err, requests)
	}
}

func TestInfrastructureRejectsElementOverflow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		value := overpassFixture(now.Add(-time.Minute))
		value["elements"] = append(value["elements"].([]map[string]any), value["elements"].([]map[string]any)[0])
		writeOverpassJSON(t, writer, value)
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, Endpoint: server.URL, MaxElements: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Infrastructure(context.Background(), exposurecollection.InfrastructureQuery{
		Bounds: exposurecollection.Bounds{South: 39.90, West: 116.40, North: 39.91, East: 116.41}})
	if err == nil {
		t.Fatal("Infrastructure() 未拒绝元素数量溢出")
	}
}

func TestInfrastructureRejectsRemarkEvenWithPartialElements(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		value := overpassFixture(now.Add(-time.Minute))
		value["remark"] = "runtime error: Query timed out; partial results follow"
		writeOverpassJSON(t, writer, value)
	}))
	defer server.Close()
	_, err := testOverpassProvider(t, server, now).Infrastructure(context.Background(),
		exposurecollection.InfrastructureQuery{Bounds: testBounds()})
	if err == nil {
		t.Fatal("Infrastructure() 未拒绝带 remark 的部分响应")
	}
}

func TestInfrastructureDeniesEveryRedirectWithoutRetry(t *testing.T) {
	statuses := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			probe := &overpassRedirectProbe{status: status}
			client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Transport: probe},
				MaxAttempts: 3, Now: func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }})
			provider, err := New(Options{Client: client,
				Endpoint: "https://overpass.example.test/api/interpreter"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Infrastructure(context.Background(),
				exposurecollection.InfrastructureQuery{Bounds: testBounds()})
			if !errors.Is(err, domain.ErrProviderUnavailable) || probe.source.Load() != 1 ||
				probe.target.Load() != 0 {
				t.Fatalf("redirect status=%d error=%v source=%d target=%d", status, err,
					probe.source.Load(), probe.target.Load())
			}
		})
	}
}

func TestDecodeResponseDistinguishesMissingAndNullRemark(t *testing.T) {
	element := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}`
	missing := overpassResponsePayload("0.6", element)
	value, err := decodeResponse([]byte(missing), defaultMaxElements, defaultMaxTotalCoordinates)
	if err != nil || value.Remark.Present || value.Remark.Null {
		t.Fatalf("missing remark=%+v error=%v", value.Remark, err)
	}
	empty := strings.Replace(missing, `"elements"`, `"remark":"","elements"`, 1)
	value, err = decodeResponse([]byte(empty), defaultMaxElements, defaultMaxTotalCoordinates)
	if err != nil || !value.Remark.Present || value.Remark.Null || value.Remark.Value != "" {
		t.Fatalf("empty remark=%+v error=%v", value.Remark, err)
	}
	nullRemark := strings.Replace(missing, `"elements"`, `"remark":null,"elements"`, 1)
	if _, err = decodeResponse([]byte(nullRemark), defaultMaxElements,
		defaultMaxTotalCoordinates); err == nil {
		t.Fatal("显式 null remark 未被拒绝")
	}
}

func TestDecodeResponseRejectsDuplicateTopLevelKeys(t *testing.T) {
	timestamp := "2026-08-28T11:59:00Z"
	metadata := fmt.Sprintf(`{"timestamp_osm_base":%q}`, timestamp)
	elements := `[{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}]`
	cases := map[string]string{
		"remark":              fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"remark":"","remark":"partial","elements":%s}`, metadata, elements),
		"remark_case":         fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"remark":"partial","Remark":"","elements":%s}`, metadata, elements),
		"elements":            fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":%s,"elements":%s}`, metadata, elements, elements),
		"elements_case":       fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":%s,"Elements":%s}`, metadata, elements, elements),
		"osm3s":               fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"osm3s":%s,"elements":%s}`, metadata, metadata, elements),
		"osm3s_case":          fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"OSM3S":%s,"elements":%s}`, metadata, metadata, elements),
		"osm_timestamp_case":  fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":{"timestamp_osm_base":%q,"Timestamp_OSM_Base":%q},"elements":%s}`, timestamp, timestamp, elements),
		"element_id_case":     fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"node","id":22,"ID":23,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}]}`, metadata),
		"coordinate_lat_case": fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"way","id":22,"tags":{"highway":"primary"},"geometry":[{"lat":39.905,"Lat":39.906,"lon":116.405},{"lat":39.906,"lon":116.406}]}]}`, metadata),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResponse([]byte(payload), defaultMaxElements,
				defaultMaxTotalCoordinates); err == nil {
				t.Fatal("重复顶层键未被拒绝")
			}
		})
	}
}

func TestDecodeResponseRejectsNonCanonicalCriticalKeys(t *testing.T) {
	timestamp := "2026-08-28T11:59:00Z"
	metadata := fmt.Sprintf(`{"timestamp_osm_base":%q}`, timestamp)
	element := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}`
	cases := map[string]string{
		"version":  fmt.Sprintf(`{"Version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, element),
		"elements": fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"Elements":[%s]}`, metadata, element),
		"id":       fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"node","ID":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}]}`, metadata),
		"lat":      fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"node","id":22,"Lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}]}`, metadata),
		"geometry": fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"way","id":11,"tags":{"highway":"primary"},"Geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}]}`, metadata),
		"tags":     fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"node","id":22,"lat":39.905,"lon":116.405,"Tags":{"amenity":"hospital"}}]}`, metadata),
		"tag":      fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[{"type":"way","id":11,"tags":{"Highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}]}`, metadata),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResponse([]byte(payload), defaultMaxElements,
				defaultMaxTotalCoordinates); err == nil {
				t.Fatal("非规范安全字段拼写未被拒绝")
			}
		})
	}
}

func TestDecodeResponseRejectsUnicodeFoldedCriticalKeys(t *testing.T) {
	metadata := `{"timestamp_osm_base":"2026-08-28T11:59:00Z"}`
	element := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}`
	unicodeElement := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tagſ":{"amenity":"hospital"}}`
	canonicalBadTags := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":null,"tagſ":{"amenity":"hospital"}}`
	reversedBadTags := `{"type":"node","id":22,"lat":39.905,"lon":116.405,"tagſ":{"amenity":"hospital"},"tags":null}`
	cases := map[string]string{
		"version_only":    fmt.Sprintf(`{"verſion":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, element),
		"version_after":   fmt.Sprintf(`{"version":0.5,"verſion":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, element),
		"version_before":  fmt.Sprintf(`{"verſion":0.5,"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, element),
		"elements_only":   fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elementſ":[%s]}`, metadata, element),
		"elements_after":  fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[],"elementſ":[%s]}`, metadata, element),
		"elements_before": fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elementſ":[],"elements":[%s]}`, metadata, element),
		"tags_only":       fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, unicodeElement),
		"bad_tags_after":  fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, canonicalBadTags),
		"bad_tags_before": fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":%s,"elements":[%s]}`, metadata, reversedBadTags),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResponse([]byte(payload), defaultMaxElements,
				defaultMaxTotalCoordinates); !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("Unicode 折叠安全字段 error=%v", err)
			}
		})
	}
}

func TestDecodeResponseRequiresVersionAndAllowsOfficialExtensions(t *testing.T) {
	element := `{"type":"way","id":11,"timestamp":"2026-08-28T11:58:00Z","version":7,"changeset":8,"user":"osm","uid":9,"nodes":[1,2],"bounds":{"minlat":39,"minlon":116,"maxlat":40,"maxlon":117},"tags":{"highway":"primary","name":"道路"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`
	for _, version := range []string{"0.5", "0.7", "1.0"} {
		payload := overpassResponsePayload(version, element)
		if _, err := decodeResponse([]byte(payload), defaultMaxElements,
			defaultMaxTotalCoordinates); err == nil {
			t.Fatalf("version=%s 未被拒绝", version)
		}
	}
	value, err := decodeResponse([]byte(overpassResponsePayload("0.6", element)),
		defaultMaxElements, defaultMaxTotalCoordinates)
	if err != nil {
		t.Fatal(err)
	}
	features, _, err := convertElements(value.Elements)
	if err != nil || len(features) != 1 || features[0].FeatureID != "osm-road-way-11" {
		t.Fatalf("features=%+v error=%v", features, err)
	}
}

func TestInfrastructureRejectsTotalCoordinateOverflow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeOverpassJSON(t, writer, overpassFixture(now.Add(-time.Minute)))
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, Endpoint: server.URL, MaxTotalCoordinates: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Infrastructure(context.Background(),
		exposurecollection.InfrastructureQuery{Bounds: testBounds()}); err == nil {
		t.Fatal("Infrastructure() 未拒绝总坐标预算溢出")
	}
}

func TestInfrastructureAcceptsCompleteEmptyResult(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		value := overpassFixture(now.Add(-time.Minute))
		value["elements"] = []map[string]any{}
		writeOverpassJSON(t, writer, value)
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	value, err := provider.Infrastructure(context.Background(),
		exposurecollection.InfrastructureQuery{Bounds: testBounds()})
	if err != nil || value.Features == nil || len(value.Features) != 0 || len(value.InputReferences) != 2 {
		t.Fatalf("完整空结果未按真实零值返回: value=%+v error=%v", value, err)
	}
}

func TestDecodeResponseRejectsMissingAndNullElements(t *testing.T) {
	timestamp := "2026-08-31T11:59:00Z"
	base := fmt.Sprintf(`{"version":0.6,"generator":"Overpass API","osm3s":{"timestamp_osm_base":%q}}`, timestamp)
	for name, payload := range map[string]string{
		"missing": base,
		"null":    strings.TrimSuffix(base, "}") + `,"elements":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResponse([]byte(payload), defaultMaxElements,
				defaultMaxTotalCoordinates); err == nil {
				t.Fatal("缺失或 null elements 未被拒绝")
			}
		})
	}
}

func TestInfrastructureSkipsNonClosedFacilityWayWithLimitation(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		value := overpassFixture(now.Add(-time.Minute))
		value["elements"] = append(value["elements"].([]map[string]any), map[string]any{
			"type": "way", "id": 33, "tags": map[string]string{"amenity": "hospital"},
			"geometry": []map[string]float64{{"lat": 39.90, "lon": 116.40},
				{"lat": 39.91, "lon": 116.41}, {"lat": 39.90, "lon": 116.42}},
		})
		writeOverpassJSON(t, writer, value)
	}))
	defer server.Close()
	result, err := testOverpassProvider(t, server, now).Infrastructure(context.Background(),
		exposurecollection.InfrastructureQuery{Bounds: testBounds()})
	if err != nil || len(result.Features) != 2 || len(result.Limitations) != 1 ||
		!strings.Contains(result.Limitations[0], "1 条") {
		t.Fatalf("Infrastructure()=%+v error=%v", result, err)
	}
}

func TestNodeCoordinatesDistinguishMissingNullAndExplicitZero(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		longitude   bool
		wantPresent bool
		wantNull    bool
		wantError   bool
	}{
		{name: "lat-missing", payload: `{"type":"node","id":22,"lon":0,"tags":{"amenity":"hospital"}}`, wantError: true},
		{name: "lat-null", payload: `{"type":"node","id":22,"lat":null,"lon":0,"tags":{"amenity":"hospital"}}`, wantPresent: true, wantNull: true, wantError: true},
		{name: "lon-missing", payload: `{"type":"node","id":22,"lat":0,"tags":{"amenity":"hospital"}}`, longitude: true, wantError: true},
		{name: "lon-null", payload: `{"type":"node","id":22,"lat":0,"lon":null,"tags":{"amenity":"hospital"}}`, longitude: true, wantPresent: true, wantNull: true, wantError: true},
		{name: "explicit-zero", payload: `{"type":"node","id":22,"lat":0,"lon":0,"tags":{"amenity":"hospital"}}`, wantPresent: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			element := decodeOSMElement(t, test.payload)
			coordinate := element.Lat
			if test.longitude {
				coordinate = element.Lon
			}
			if coordinate.Present != test.wantPresent || coordinate.Null != test.wantNull {
				t.Fatalf("coordinate presence=%v null=%v", coordinate.Present, coordinate.Null)
			}
			feature, include, err := convertElement(element)
			if test.wantError {
				if err == nil {
					t.Fatal("convertElement() 未拒绝缺失或 null 节点坐标")
				}
				return
			}
			if err != nil || !include {
				t.Fatalf("convertElement() include=%v error=%v", include, err)
			}
			assertPointCoordinates(t, feature.Geometry, 0, 0)
		})
	}
}

func TestWayCoordinatesDistinguishMissingNullAndExplicitZero(t *testing.T) {
	cases := []struct {
		name        string
		first       string
		longitude   bool
		wantPresent bool
		wantNull    bool
		wantError   bool
	}{
		{name: "lat-missing", first: `{"lon":0}`, wantError: true},
		{name: "lat-null", first: `{"lat":null,"lon":0}`, wantPresent: true, wantNull: true, wantError: true},
		{name: "lon-missing", first: `{"lat":0}`, longitude: true, wantError: true},
		{name: "lon-null", first: `{"lat":0,"lon":null}`, longitude: true, wantPresent: true, wantNull: true, wantError: true},
		{name: "explicit-zero", first: `{"lat":0,"lon":0}`, wantPresent: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"type":"way","id":11,"tags":{"highway":"primary"},"geometry":[%s,{"lat":1,"lon":1}]}`, test.first)
			element := decodeOSMElement(t, payload)
			coordinate := element.Geometry[0].Lat
			if test.longitude {
				coordinate = element.Geometry[0].Lon
			}
			if coordinate.Present != test.wantPresent || coordinate.Null != test.wantNull {
				t.Fatalf("coordinate presence=%v null=%v", coordinate.Present, coordinate.Null)
			}
			feature, include, err := convertElement(element)
			if test.wantError {
				if err == nil {
					t.Fatal("convertElement() 未拒绝缺失或 null way 坐标")
				}
				return
			}
			if err != nil || !include {
				t.Fatalf("convertElement() include=%v error=%v", include, err)
			}
			assertLineFirstCoordinate(t, feature.Geometry, 0, 0)
		})
	}
}

func TestCenterCoordinatesDistinguishMissingNullAndExplicitZero(t *testing.T) {
	cases := []struct {
		name              string
		center            string
		checkMember       bool
		longitude         bool
		wantPresent       bool
		wantNull          bool
		wantMemberPresent bool
		wantMemberNull    bool
		wantError         bool
	}{
		{name: "missing"},
		{name: "null", center: `,"center":null`, wantPresent: true, wantNull: true, wantError: true},
		{name: "lat-missing", center: `,"center":{"lon":0}`, checkMember: true, wantPresent: true, wantError: true},
		{name: "lat-null", center: `,"center":{"lat":null,"lon":0}`, checkMember: true, wantPresent: true, wantMemberPresent: true, wantMemberNull: true, wantError: true},
		{name: "lon-missing", center: `,"center":{"lat":0}`, checkMember: true, longitude: true, wantPresent: true, wantError: true},
		{name: "lon-null", center: `,"center":{"lat":0,"lon":null}`, checkMember: true, longitude: true, wantPresent: true, wantMemberPresent: true, wantMemberNull: true, wantError: true},
		{name: "explicit-zero", center: `,"center":{"lat":0,"lon":0}`, wantPresent: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"type":"way","id":11,"tags":{"highway":"primary"}%s,"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`, test.center)
			element := decodeOSMElement(t, payload)
			if element.Center.Present != test.wantPresent || element.Center.Null != test.wantNull {
				t.Fatalf("center presence=%v null=%v", element.Center.Present, element.Center.Null)
			}
			if test.checkMember {
				coordinate := element.Center.Lat
				if test.longitude {
					coordinate = element.Center.Lon
				}
				if coordinate.Present != test.wantMemberPresent || coordinate.Null != test.wantMemberNull {
					t.Fatalf("center member presence=%v null=%v", coordinate.Present, coordinate.Null)
				}
			}
			feature, include, err := convertElement(element)
			if test.wantError {
				if err == nil {
					t.Fatal("convertElement() 未拒绝 null 或不完整 center")
				}
				return
			}
			if err != nil || !include {
				t.Fatalf("convertElement() include=%v error=%v", include, err)
			}
			if test.name == "explicit-zero" && (!element.Center.Lat.Present || element.Center.Lat.Null ||
				!element.Center.Lon.Present || element.Center.Lon.Null) {
				t.Fatal("显式 center (0,0) 未保留为有效坐标")
			}
			assertLineFirstCoordinate(t, feature.Geometry, 116, 39)
		})
	}
}

func TestConvertElementsRejectsBadTagsAlongsideValidRoad(t *testing.T) {
	validRoad := `{"type":"way","id":11,"tags":{"highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`
	cases := map[string]string{
		"missing":        `{"type":"node","id":22,"lat":39.5,"lon":116.5}`,
		"null":           `{"type":"node","id":22,"lat":39.5,"lon":116.5,"tags":null}`,
		"empty":          `{"type":"node","id":22,"lat":39.5,"lon":116.5,"tags":{}}`,
		"unclassifiable": `{"type":"node","id":22,"lat":39.5,"lon":116.5,"tags":{"name":"医院"}}`,
	}
	for name, invalidFacility := range cases {
		t.Run(name, func(t *testing.T) {
			elements := decodeOSMElements(t, fmt.Sprintf(`[%s,%s]`, validRoad, invalidFacility))
			if _, _, err := convertElements(elements); err == nil {
				t.Fatal("合法道路旁的坏 tags 元素被静默忽略")
			}
		})
	}
}

func TestConvertElementsDeduplicatesOnlyIdenticalOSMIdentity(t *testing.T) {
	road := `{"type":"way","id":11,"tags":{"highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`
	identical := decodeOSMElements(t, fmt.Sprintf(`[%s,%s]`, road, road))
	features, _, err := convertElements(identical)
	if err != nil || len(features) != 1 {
		t.Fatalf("identical duplicate features=%+v error=%v", features, err)
	}
	reordered := `{"geometry":[{"lon":116,"lat":39},{"lon":117,"lat":40}],"tags":{"highway":"primary"},"id":11,"type":"way"}`
	features, _, err = convertElements(decodeOSMElements(t, fmt.Sprintf(`[%s,%s]`, road, reordered)))
	if err != nil || len(features) != 1 {
		t.Fatalf("reordered duplicate features=%+v error=%v", features, err)
	}
	conflicts := map[string]string{
		"geometry": `{"type":"way","id":11,"tags":{"highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":41,"lon":118}]}`,
		"kind":     `{"type":"way","id":11,"tags":{"amenity":"hospital"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117},{"lat":39,"lon":116}]}`,
	}
	for name, conflict := range conflicts {
		t.Run(name, func(t *testing.T) {
			elements := decodeOSMElements(t, fmt.Sprintf(`[%s,%s]`, road, conflict))
			if _, _, err := convertElements(elements); err == nil {
				t.Fatal("同一 OSM type/id 的冲突内容未被拒绝")
			}
		})
	}
	metadataV7 := `{"type":"way","id":11,"version":7,"timestamp":"2026-08-28T11:58:00Z","changeset":8,"tags":{"highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`
	metadataV8 := `{"type":"way","id":11,"version":8,"timestamp":"2026-08-28T11:58:00Z","changeset":8,"tags":{"highway":"primary"},"geometry":[{"lat":39,"lon":116},{"lat":40,"lon":117}]}`
	if _, _, err = convertElements(decodeOSMElements(t,
		fmt.Sprintf(`[%s,%s]`, metadataV7, metadataV8))); err == nil {
		t.Fatal("同一 OSM type/id 的官方扩展字段冲突未被拒绝")
	}
}

func TestBuildQuerySeparatesServerAndWireBudgets(t *testing.T) {
	query := buildQuery(testBounds(), 32<<20)
	if !strings.Contains(query, `[maxsize:33554432]`) || strings.Contains(query, `[maxsize:6291456]`) {
		t.Fatalf("buildQuery()=%q", query)
	}
}

func TestClassifyPrefersRoadForHighwayFeature(t *testing.T) {
	kind, include := classify(map[string]string{"highway": "service", "amenity": "hospital"})
	if !include || kind != applicationloss.LossFeatureRoad {
		t.Fatalf("classify()=%q include=%v", kind, include)
	}
}

func testOverpassProvider(t *testing.T, server *httptest.Server, now time.Time) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func assertOverpassRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("request=%s content-type=%q", request.Method, request.Header.Get("Content-Type"))
	}
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	query := request.Form.Get("data")
	if !strings.Contains(query, `[maxsize:67108864]`) || !strings.Contains(query, `way["highway"]`) ||
		!strings.Contains(query, `node["amenity"~"^(hospital|clinic)$"]`) {
		t.Fatalf("Overpass query=%q", query)
	}
	if _, err := url.ParseRequestURI(request.RequestURI); err != nil {
		t.Fatal(err)
	}
}

func testBounds() exposurecollection.Bounds {
	return exposurecollection.Bounds{South: 39.90, West: 116.40, North: 39.91, East: 116.41}
}

func overpassFixture(timestamp time.Time) map[string]any {
	return map[string]any{"version": 0.6, "generator": "Overpass API",
		"osm3s": map[string]any{"timestamp_osm_base": timestamp.Format(time.RFC3339)},
		"elements": []map[string]any{
			{"type": "way", "id": 11, "tags": map[string]string{"highway": "primary"},
				"geometry": []map[string]float64{{"lat": 39.90, "lon": 116.40}, {"lat": 39.91, "lon": 116.41}}},
			{"type": "node", "id": 22, "lat": 39.905, "lon": 116.405,
				"tags": map[string]string{"amenity": "hospital"}},
		}}
}

func decodeOSMElement(t *testing.T, payload string) osmElement {
	t.Helper()
	var element osmElement
	if err := json.Unmarshal([]byte(payload), &element); err != nil {
		t.Fatal(err)
	}
	return element
}

func decodeOSMElements(t *testing.T, payload string) []osmElement {
	t.Helper()
	var elements []osmElement
	if err := json.Unmarshal([]byte(payload), &elements); err != nil {
		t.Fatal(err)
	}
	return elements
}

func overpassResponsePayload(version, element string) string {
	return fmt.Sprintf(`{"version":%s,"generator":"Overpass API","license":"ODbL","osm3s":{"timestamp_osm_base":"2026-08-28T11:59:00Z","copyright":"OpenStreetMap contributors"},"elements":[%s]}`,
		version, element)
}

func assertPointCoordinates(t *testing.T, payload json.RawMessage, longitude, latitude float64) {
	t.Helper()
	var geometry struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(payload, &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Type != "Point" || len(geometry.Coordinates) != 2 ||
		geometry.Coordinates[0] != longitude || geometry.Coordinates[1] != latitude {
		t.Fatalf("geometry=%s", payload)
	}
}

func assertLineFirstCoordinate(t *testing.T, payload json.RawMessage, longitude, latitude float64) {
	t.Helper()
	var geometry struct {
		Type        string      `json:"type"`
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(payload, &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Type != "LineString" || len(geometry.Coordinates) == 0 ||
		len(geometry.Coordinates[0]) != 2 || geometry.Coordinates[0][0] != longitude ||
		geometry.Coordinates[0][1] != latitude {
		t.Fatalf("geometry=%s", payload)
	}
}

func writeOverpassJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

type overpassRedirectProbe struct {
	status int
	source atomic.Int32
	target atomic.Int32
}

func (p *overpassRedirectProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/api/interpreter":
		p.source.Add(1)
		if request.Method != http.MethodPost {
			return nil, fmt.Errorf("Overpass 初始方法=%s", request.Method)
		}
		header := make(http.Header)
		header.Set("Location", "https://overpass.example.test/sink")
		return overpassRedirectResponse(request, p.status, "", header), nil
	case "/sink":
		p.target.Add(1)
		body := overpassResponsePayload("0.6",
			`{"type":"node","id":22,"lat":39.905,"lon":116.405,"tags":{"amenity":"hospital"}}`)
		return overpassRedirectResponse(request, http.StatusOK, body, nil), nil
	default:
		return nil, fmt.Errorf("意外的 Overpass 路径=%s", request.URL.Path)
	}
}

func overpassRedirectResponse(request *http.Request, status int, body string,
	header http.Header,
) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header,
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}
