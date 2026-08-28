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

func TestRouteValidateRequiresExplicitRiskScorePresence(t *testing.T) {
	route := Route{
		Origin:      spatial.Point{Longitude: 116.4, Latitude: 39.9},
		Destination: spatial.Point{Longitude: 116.5, Latitude: 39.8},
		Mode:        TravelDriving, DistanceMeters: 1_000, DurationSeconds: 300,
		RiskScore: 2,
	}
	if err := route.Validate(); err == nil {
		t.Fatal("未声明提供性的风险分数应被拒绝")
	}
	route.RiskScoreProvided = true
	if err := route.Validate(); err != nil {
		t.Fatalf("显式提供的风险分数应通过: %v", err)
	}
	route.RiskScore = 101
	if err := route.Validate(); err == nil {
		t.Fatal("超过 100 的风险分数应被拒绝")
	}
}
