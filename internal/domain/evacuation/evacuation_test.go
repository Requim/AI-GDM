package evacuation

import (
	"testing"

	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestRouteValidate(t *testing.T) {
	route := Route{
		Origin:      spatial.Point{Longitude: 116.4, Latitude: 39.9},
		Destination: spatial.Point{Longitude: 116.5, Latitude: 39.8},
		Mode:        TravelDriving, DistanceMeters: 1_000, DurationSeconds: 300,
	}
	if err := route.Validate(); err != nil {
		t.Fatal(err)
	}

	route.Mode = "flying"
	if err := route.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝未知交通方式")
	}
}
