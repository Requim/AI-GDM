package gdal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestMosaickerBuildsAndValidatesTargetGrid(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "mosaic.tif")
	runner := &mosaicRunner{info: chinaMosaicInfo}
	mosaicker := &Mosaicker{
		runner: runner, config: applyMosaicDefaults(MosaicConfig{BBox: chinaBBox}),
	}
	inputs := []string{filepath.Join(directory, "a.tif"), filepath.Join(directory, "b.tif")}
	if err := mosaicker.Mosaic(context.Background(), inputs, output); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.calls[0], " ")
	if !strings.Contains(joined, "raster mosaic") || !strings.Contains(joined, "BIGTIFF=IF_SAFER") ||
		!strings.Contains(joined, inputs[0]) || !strings.Contains(joined, output) {
		t.Fatalf("mosaic arguments = %s", joined)
	}
}

func TestValidateMosaicInfoRejectsWrongSize(t *testing.T) {
	payload := strings.Replace(chinaMosaicInfo, `"size":[7392,4272]`, `"size":[7391,4272]`, 1)
	if err := validateMosaicInfo([]byte(payload), chinaBBox); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("validateMosaicInfo() error = %v", err)
	}
}

type mosaicRunner struct {
	calls [][]string
	info  string
}

func (r *mosaicRunner) Run(_ context.Context, _ string, arguments ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), arguments...))
	if len(arguments) > 1 && arguments[0] == "raster" && arguments[1] == "mosaic" {
		return nil, os.WriteFile(arguments[len(arguments)-1], []byte("mosaic"), 0o600)
	}
	if len(arguments) > 1 && arguments[0] == "raster" && arguments[1] == "info" {
		return []byte(r.info), nil
	}
	return nil, nil
}

const chinaMosaicInfo = `{
  "driver":"GTiff",
  "size":[7392,4272],
  "geoTransform":[73.5,0.008333333333333333,0,53.6,0,-0.008333333333333333],
  "stac":{"proj:projjson":{"id":{"authority":"EPSG","code":4326}}},
  "bands":[{"type":"Float32","noDataValue":-9999,"minimum":0.02,"maximum":0.95}]
}`
