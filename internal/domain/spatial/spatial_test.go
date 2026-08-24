package spatial

import "testing"

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
