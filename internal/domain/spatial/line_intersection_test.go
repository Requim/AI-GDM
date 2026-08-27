package spatial

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestGeometryIntersectsLineStringCrossingArea(t *testing.T) {
	area := polygonGeometry("[[[100,30],[104,30],[104,34],[100,34],[100,30]]]")
	line := lineGeometry("[[99,32],[105,32]]")
	matched, err := area.IntersectsLineString(line)
	if err != nil || !matched {
		t.Fatalf("穿过风险区的路线应命中: matched=%v err=%v", matched, err)
	}
}

func TestGeometryIntersectsLineStringInsideAndOutside(t *testing.T) {
	area := polygonGeometry("[[[100,30],[104,30],[104,34],[100,34],[100,30]]]")
	inside, err := area.IntersectsLineString(lineGeometry("[[101,31],[103,33]]"))
	if err != nil || !inside {
		t.Fatalf("风险区内路线应命中: inside=%v err=%v", inside, err)
	}
	outside, err := area.IntersectsLineString(lineGeometry("[[98,31],[99,33]]"))
	if err != nil || outside {
		t.Fatalf("风险区外路线不应命中: outside=%v err=%v", outside, err)
	}
}

func TestGeometryIntersectsLineStringTreatsBoundaryAsRisk(t *testing.T) {
	area := polygonGeometry("[[[100,30],[104,30],[104,34],[100,34],[100,30]]]")
	line := lineGeometry("[[99,30],[105,30]]")
	matched, err := area.IntersectsLineString(line)
	if err != nil || !matched {
		t.Fatalf("贴着风险区边界的路线应命中: matched=%v err=%v", matched, err)
	}
}

func TestGeometryIntersectsLineStringHandlesHole(t *testing.T) {
	area := polygonGeometry("[[[100,30],[104,30],[104,34],[100,34],[100,30]],[[101,31],[103,31],[103,33],[101,33],[101,31]]]")
	holeOnly, err := area.IntersectsLineString(lineGeometry("[[101.5,31.5],[102.5,32.5]]"))
	if err != nil || holeOnly {
		t.Fatalf("完全位于洞环内的路线不应命中: matched=%v err=%v", holeOnly, err)
	}
	crossHole, err := area.IntersectsLineString(lineGeometry("[[100.5,32],[103.5,32]]"))
	if err != nil || !crossHole {
		t.Fatalf("穿过洞环边界的路线仍应命中风险区: matched=%v err=%v", crossHole, err)
	}
}

func TestGeometryIntersectsLineStringMultiPolygon(t *testing.T) {
	area := Geometry{Type: "MultiPolygon", Coordinates: json.RawMessage("[[[[100,30],[101,30],[101,31],[100,31],[100,30]]],[[[110,30],[111,30],[111,31],[110,31],[110,30]]]]")}
	matched, err := area.IntersectsLineString(lineGeometry("[[109,30.5],[112,30.5]]"))
	if err != nil || !matched {
		t.Fatalf("命中 MultiPolygon 第二个面失败: matched=%v err=%v", matched, err)
	}
}

func TestGeometryIntersectsLineStringRejectsInvalidLine(t *testing.T) {
	area := polygonGeometry("[[[100,30],[104,30],[104,34],[100,34],[100,30]]]")
	_, err := area.IntersectsLineString(Geometry{Type: "LineString", Coordinates: json.RawMessage("[[100,30]]")})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法路线几何错误=%v", err)
	}
}

func polygonGeometry(coordinates string) Geometry {
	return Geometry{Type: "Polygon", Coordinates: json.RawMessage(coordinates)}
}

func lineGeometry(coordinates string) Geometry {
	return Geometry{Type: "LineString", Coordinates: json.RawMessage(coordinates)}
}
