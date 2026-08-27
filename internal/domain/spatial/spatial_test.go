package spatial

import (
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
