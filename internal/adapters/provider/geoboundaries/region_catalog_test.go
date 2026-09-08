package geoboundaries

import (
	"strings"
	"testing"
)

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
