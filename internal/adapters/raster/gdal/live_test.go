package gdal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
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
	processor.now = func() time.Time { return fixtureTime().Add(time.Hour) }
	artifact := fixtureArtifact(input)
	artifact.SizeBytes = fileSize(t, input)
	checksum, err := fileChecksum(ctx, input, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Provenance.SHA256 = checksum
	boundary := processingBoundaryMultiPolygonForBBox(bbox)
	snapshot, zones, err := processor.Process(ctx, artifact, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != hazard.SnapshotAvailable || len(zones) != 2 {
		t.Fatalf("snapshot=%+v zones=%+v", snapshot, zones)
	}
	for _, zone := range zones {
		if zone.Level != hazard.RiskHigh || zone.Minimum < 0.59 || zone.Maximum > 0.61 {
			t.Fatalf("zone statistics = %+v", zone)
		}
	}
	width, height := bbox[2]-bbox[0], bbox[3]-bbox[1]
	assertZonesContainPoint(t, zones, spatial.Point{
		Longitude: bbox[0] + width*0.05, Latitude: bbox[1] + height*0.1,
	}, true, "主体")
	assertZonesContainPoint(t, zones, spatial.Point{
		Longitude: bbox[0] + width*0.325, Latitude: bbox[1] + height*0.45,
	}, false, "洞")
	assertZonesContainPoint(t, zones, spatial.Point{
		Longitude: bbox[0] + width*0.78, Latitude: bbox[1] + height*0.45,
	}, false, "边界外间隙")
	assertZonesContainPoint(t, zones, spatial.Point{
		Longitude: bbox[0] + width*0.92, Latitude: bbox[1] + height*0.35,
	}, true, "离岛")
}

func assertZonesContainPoint(t *testing.T, zones []hazard.RiskZone, point spatial.Point,
	want bool, label string,
) {
	t.Helper()
	inside := false
	for _, zone := range zones {
		matched, err := zone.Geometry.ContainsPoint(point)
		if err != nil {
			t.Fatalf("风险区 MultiPolygon %s点判断失败: %v", label, err)
		}
		inside = inside || matched
	}
	if inside != want {
		t.Fatalf("风险区 MultiPolygon %s裁剪结果无效: inside=%v want=%v zones=%+v",
			label, inside, want, zones)
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
