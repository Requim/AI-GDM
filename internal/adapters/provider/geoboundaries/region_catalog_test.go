package geoboundaries

import (
	"strings"
	"testing"
)

func TestRegionMetadataURLAcceptsOnlyADM1AndADM2(t *testing.T) {
	value, err := regionMetadataURL("CHN", "ADM1")
	if err != nil || value != "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM1/" {
		t.Fatalf("url=%q error=%v", value, err)
	}
	if _, err = regionMetadataURL("chn", "ADM1"); err == nil {
		t.Fatal("小写国家代码未被拒绝")
	}
	if _, err = regionMetadataURL("CHN", "ADM0"); err == nil {
		t.Fatal("ADM0 未被拒绝")
	}
}

func TestRegionMediaURLBindsCountryAndLevel(t *testing.T) {
	raw := "https://github.com/wmgeolab/geoBoundaries/raw/abcdef1/releaseData/gbOpen/CHN/ADM1/geoBoundaries-CHN-ADM1_simplified.geojson"
	value, err := regionMediaURL(raw, "CHN", "ADM1")
	if err != nil || !strings.Contains(value, "media.githubusercontent.com") {
		t.Fatalf("media url=%q error=%v", value, err)
	}
	if _, err = regionMediaURL(raw, "CHN", "ADM2"); err == nil {
		t.Fatal("层级不匹配未被拒绝")
	}
}

func TestDecodeRegionCollectionReturnsValidatedRegions(t *testing.T) {
	payload := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","properties":{"shapeID":"CHN-1","shapeName":"区域一","shapeISO":"CN-1","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"Polygon","coordinates":[]}},
		{"type":"Feature","properties":{"shapeID":"CHN-2","shapeName":"区域二","shapeISO":"CN-2","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"MultiPolygon","coordinates":[]}}
	]}`)
	values, err := DecodeRegionCollection(payload, "CHN", "ADM1")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Code != "CN-1" || values[1].Level != "ADM1" {
		t.Fatalf("区域目录=%+v", values)
	}
}

func TestDecodeRegionCollectionUsesShapeIDWhenCountryISOIsNotUnique(t *testing.T) {
	payload := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","properties":{"shapeID":"43563684B32591653033375","shapeName":"上海市","shapeISO":"CHN","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"Polygon","coordinates":[]}},
		{"type":"Feature","properties":{"shapeID":"43563684B17418114845297","shapeName":"北京市","shapeISO":"CHN","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"Polygon","coordinates":[]}}
	]}`)
	values, err := DecodeRegionCollection(payload, "CHN", "ADM1")
	if err != nil || len(values) != 2 ||
		values[0].Code != "CHN-ADM1-43563684B32591653033375" ||
		values[1].Code != "CHN-ADM1-43563684B17418114845297" ||
		values[0].BoundaryID != "CHN-ADM1-43563684B32591653033375" ||
		values[1].BoundaryID != "CHN-ADM1-43563684B17418114845297" {
		t.Fatalf("区域目录=%+v error=%v", values, err)
	}
}

func TestDecodeRegionCollectionUsesStableFallbackNameWhenProviderOmitsName(t *testing.T) {
	payload := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","properties":{"shapeID":"17275852B34966799109471","shapeName":null,"shapeISO":"","shapeGroup":"CHN","shapeType":"ADM2"},"geometry":{"type":"Polygon","coordinates":[]}}
	]}`)
	values, err := DecodeRegionCollection(payload, "CHN", "ADM2")
	if err != nil || len(values) != 1 ||
		values[0].Code != "CHN-ADM2-17275852B34966799109471" ||
		values[0].Name != "未命名行政区-CHN-ADM2-17275852B34966799109471" {
		t.Fatalf("区域目录=%+v error=%v", values, err)
	}
}

func TestDecodeRegionCollectionRejectsDuplicateCode(t *testing.T) {
	payload := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","properties":{"shapeID":"CHN-1","shapeName":"区域一","shapeISO":"CN-1","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"Polygon","coordinates":[]}},
		{"type":"Feature","properties":{"shapeID":"CHN-2","shapeName":"区域二","shapeISO":"CN-1","shapeGroup":"CHN","shapeType":"ADM1"},"geometry":{"type":"Polygon","coordinates":[]}}
	]}`)
	_, err := DecodeRegionCollection(payload, "CHN", "ADM1")
	if err == nil || !strings.Contains(err.Error(), "行政区代码重复") {
		t.Fatalf("错误=%v", err)
	}
}
