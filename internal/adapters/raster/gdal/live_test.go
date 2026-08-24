package gdal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func TestLivePipeline(t *testing.T) {
	if os.Getenv("GDAL_LIVE_TEST") != "1" {
		t.Skip("未启用 GDAL_LIVE_TEST")
	}
	directory := t.TempDir()
	input := filepath.Join(directory, "lhasa-fixture.tif")
	runner := execRunner{binary: DefaultBinary}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bbox := [4]float64{100, 30, 100.03333333333333, 30.03333333333333}
	_, err := runner.Run(ctx, directory, "raster", "create", "--size", "4,4", "--output-data-type", "Float32",
		"--crs", "EPSG:4326", "--bbox", bboxValue(bbox), "--nodata", "-9999", "--burn", "0.6", input)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := New(Config{ArtifactRoot: directory, TemporaryDir: directory, BBox: bbox})
	if err != nil {
		t.Fatal(err)
	}
	artifact := fixtureArtifact(input)
	artifact.SizeBytes = fileSize(t, input)
	checksum, err := fileChecksum(ctx, input, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Provenance.SHA256 = checksum
	snapshot, zones, err := processor.Process(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != hazard.SnapshotAvailable || len(zones) != 1 || zones[0].Level != hazard.RiskHigh {
		t.Fatalf("snapshot=%+v zones=%+v", snapshot, zones)
	}
	if zones[0].Minimum < 0.59 || zones[0].Maximum > 0.61 {
		t.Fatalf("zone statistics = %+v", zones[0])
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
