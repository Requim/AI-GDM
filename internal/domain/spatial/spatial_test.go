package spatial

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestPointValidate(t *testing.T) {
	tests := []struct {
		name    string
		point   Point
		wantErr bool
	}{
		{name: "valid", point: Point{Longitude: 116.4, Latitude: 39.9}},
		{name: "invalid longitude", point: Point{Longitude: 181}, wantErr: true},
		{name: "invalid latitude", point: Point{Latitude: -91}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.point.Validate(); (got != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v", got)
			}
		})
	}
}

func TestPointValidateRejectsNonFiniteValues(t *testing.T) {
	for name, point := range map[string]Point{
		"nan longitude":     {Longitude: math.NaN(), Latitude: 1},
		"infinite latitude": {Longitude: 1, Latitude: math.Inf(1)},
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(point.Validate(), domain.ErrInvalidInput) {
				t.Fatal("Validate() 应拒绝非有限坐标")
			}
		})
	}
}

func TestGeometryContainsPointPolygon(t *testing.T) {
	geometry := Geometry{Type: "Polygon", Coordinates: json.RawMessage([]byte("[\n\t\t[[100,30],[101,30],[101,31],[100,31],[100,30]]\n\t]"))}
	tests := []struct {
		name  string
		point Point
		want  bool
	}{
		{name: "inside", point: Point{Longitude: 100.5, Latitude: 30.5}, want: true},
		{name: "outside", point: Point{Longitude: 99.5, Latitude: 30.5}},
		{name: "boundary", point: Point{Longitude: 100, Latitude: 30.5}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := geometry.ContainsPoint(test.point)
			if err != nil || got != test.want {
				t.Fatalf("ContainsPoint() = %v, %v, want %v", got, err, test.want)
			}
		})
	}
}

func TestGeometryContainsPointRespectsHole(t *testing.T) {
	geometry := Geometry{Type: "Polygon", Coordinates: json.RawMessage([]byte("[\n\t\t[[100,30],[104,30],[104,34],[100,34],[100,30]],\n\t\t[[101,31],[103,31],[103,33],[101,33],[101,31]]\n\t]"))}
	inside, err := geometry.ContainsPoint(Point{Longitude: 100.5, Latitude: 30.5})
	if err != nil || !inside {
		t.Fatalf("外环内点判断 = %v, %v", inside, err)
	}
	hole, err := geometry.ContainsPoint(Point{Longitude: 102, Latitude: 32})
	if err != nil || hole {
		t.Fatalf("洞环内点判断 = %v, %v", hole, err)
	}
	boundary, err := geometry.ContainsPoint(Point{Longitude: 101, Latitude: 32})
	if err != nil || !boundary {
		t.Fatalf("洞环边界判断 = %v, %v", boundary, err)
	}
}

func TestGeometryContainsPointMultiPolygon(t *testing.T) {
	geometry := Geometry{Type: "MultiPolygon", Coordinates: json.RawMessage([]byte("[\n\t\t[[[100,30],[101,30],[101,31],[100,31],[100,30]]],\n\t\t[[[110,30],[111,30],[111,31],[110,31],[110,30]]]\n\t]"))}
	for name, point := range map[string]Point{
		"first polygon":  {Longitude: 100.5, Latitude: 30.5},
		"second polygon": {Longitude: 110.5, Latitude: 30.5},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := geometry.ContainsPoint(point)
			if err != nil || !got {
				t.Fatalf("ContainsPoint() = %v, %v", got, err)
			}
		})
	}
	outside, err := geometry.ContainsPoint(Point{Longitude: 105, Latitude: 30.5})
	if err != nil || outside {
		t.Fatalf("MultiPolygon 外部点判断 = %v, %v", outside, err)
	}
}

func TestGeometryContainsPointRejectsInvalidGeometry(t *testing.T) {
	tests := []Geometry{
		{Type: "Point", Coordinates: json.RawMessage([]byte("[100,30]"))},
		{Type: "Polygon", Coordinates: json.RawMessage([]byte("[[[100,30],[101,30],[101,31],[100,31]]]"))},
		{Type: "MultiPolygon", Coordinates: json.RawMessage([]byte("[]"))},
		{Type: "MultiPolygon", Coordinates: json.RawMessage([]byte("[[[[100,30],[101,30],[101,31],[100,31],[100,30]]],[[[110,30],[111,30],[111,31],[110,31]]]]"))},
	}
	for index, geometry := range tests {
		if _, err := geometry.ContainsPoint(Point{Longitude: 100.5, Latitude: 30.5}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("case %d error = %v", index, err)
		}
	}
}

func TestGeometryValidateAreaRejectsInvalidGeometryWithoutPointQuery(t *testing.T) {
	geometry := Geometry{Type: "Polygon", Coordinates: json.RawMessage([]byte("[[[100,30],[101,30],[101,31],[100,31]]]"))}
	if err := geometry.ValidateArea(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ValidateArea() error = %v", err)
	}
}

func TestGeometryContainsPointRejectsInvalidPoint(t *testing.T) {
	geometry := Geometry{Type: "Polygon", Coordinates: json.RawMessage([]byte("[\n\t\t[[100,30],[101,30],[101,31],[100,31],[100,30]]\n\t]"))}
	_, err := geometry.ContainsPoint(Point{Longitude: 181, Latitude: 30})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ContainsPoint() error = %v", err)
	}
}
