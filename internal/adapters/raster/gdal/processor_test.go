package gdal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestProcessBuildsSnapshotAndZones(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.tif")
	if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{geoJSON: validGeoJSON}
	processor := newProcessor(applyDefaults(Config{ArtifactRoot: directory, TemporaryDir: directory}), runner)
	processor.now = func() time.Time { return fixtureTime().Add(time.Hour) }
	snapshot, zones, err := processor.Process(context.Background(), fixtureArtifact(input), processingBoundaryFixture())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != hazard.SnapshotAvailable || snapshot.Source.TransformVersion != TransformVersion {
		t.Fatalf("Snapshot = %+v", snapshot)
	}
	if len(zones) != 1 || zones[0].Level != hazard.RiskHigh || zones[0].Mean != 0.62 ||
		len(zones[0].AdminCodes) != 1 || zones[0].AdminCodes[0] != "CN" {
		t.Fatalf("Zones = %+v", zones)
	}
	if snapshot.Coverage == nil || snapshot.Coverage.BoundaryID != "CHN-ADM0-1" {
		t.Fatalf("Coverage = %+v", snapshot.Coverage)
	}
	if len(runner.calls) != 9 || !strings.Contains(strings.Join(runner.calls[2], " "), bboxValue(chinaBBox)) ||
		!strings.Contains(strings.Join(runner.calls[6], " "), "vector clip --input-format GeoJSON --like") {
		t.Fatalf("Calls = %+v", runner.calls)
	}
}

func TestProcessDistinguishesNoThresholdZonesFromBoundaryOmissions(t *testing.T) {
	for _, item := range []struct {
		name       string
		runner     *recordingRunner
		limitation string
		calls      int
	}{
		{name: "外接矩形无风险区", runner: &recordingRunner{rawGeoJSON: emptyGeoJSON},
			limitation: "中国外接矩形内未生成达到阈值的风险区", calls: 6},
		{name: "风险区均在国界外", runner: &recordingRunner{clippedGeoJSON: emptyGeoJSON},
			limitation: "均位于 CHN ADM0 边界外", calls: 7},
	} {
		t.Run(item.name, func(t *testing.T) {
			directory := t.TempDir()
			input := filepath.Join(directory, "input.tif")
			if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			processor := newProcessor(applyDefaults(Config{ArtifactRoot: directory,
				TemporaryDir: directory}), item.runner)
			snapshot, zones, err := processor.Process(context.Background(), fixtureArtifact(input),
				processingBoundaryFixture())
			if err != nil || len(zones) != 0 || !containsText(snapshot.Limitations, item.limitation) ||
				len(item.runner.calls) != item.calls {
				t.Fatalf("snapshot=%+v zones=%v calls=%d error=%v",
					snapshot, zones, len(item.runner.calls), err)
			}
		})
	}
}

func TestProcessRejectsInvalidRasterRange(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.tif")
	if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{info: strings.Replace(validRasterInfo, `"minimum":0.02`, `"minimum":-1`, 1)}
	processor := newProcessor(applyDefaults(Config{ArtifactRoot: directory, TemporaryDir: directory}), runner)
	_, _, err := processor.Process(context.Background(), fixtureArtifact(input), processingBoundaryFixture())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Process() error = %v", err)
	}
}

func TestDecodeFeaturesRejectsInvalidStatistics(t *testing.T) {
	payload := strings.Replace(validGeoJSON, `"mean":0.62`, `"mean":1.2`, 1)
	_, err := decodeFeatures([]byte(payload), 10)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("decodeFeatures() error = %v", err)
	}
}

func TestDecodeFeaturesRejectsNonPolygon(t *testing.T) {
	payload := strings.Replace(validGeoJSON,
		`"geometry":{"type":"Polygon","coordinates":[[[100,30],[101,30],[101,31],[100,31],[100,30]]]}`,
		`"geometry":{"type":"Point","coordinates":[100,30]}`, 1)
	_, err := decodeFeatures([]byte(payload), 10)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("decodeFeatures() error = %v", err)
	}
}

func TestDecodeFeaturesRejectsCoordinatesOutsideWGS84(t *testing.T) {
	payload := strings.Replace(validGeoJSON, `[[100,30],[101,30],[101,31],[100,31],[100,30]]`,
		`[[190,30],[191,30],[191,31],[190,31],[190,30]]`, 1)
	_, err := decodeFeatures([]byte(payload), 10)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("decodeFeatures() error = %v", err)
	}
}

func TestDefaultThresholdsCoverProbabilityRange(t *testing.T) {
	if err := hazard.ValidateThresholds(defaultThresholds()); err != nil {
		t.Fatal(err)
	}
}

func TestClassBoundariesUseStrictGreaterThan(t *testing.T) {
	arguments := strings.Join(classifyArguments("input.tif", "output.tif"), " ")
	if !strings.Contains(arguments, "(X>0.1)") || !strings.Contains(arguments, "UInt8") || strings.Contains(arguments, ">=") {
		t.Fatalf("classify arguments = %s", arguments)
	}
	if validStatistics(1, 0.1, 0.2, 0.5) || !validStatistics(1, 0.100001, 0.2, 0.5) {
		t.Fatal("中风险边界未采用 (0.1,0.5]")
	}
}

func TestProcessRejectsChecksumMismatch(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.tif")
	if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := fixtureArtifact(input)
	artifact.Provenance.SHA256 = strings.Repeat("0", 64)
	processor := newProcessor(applyDefaults(Config{ArtifactRoot: directory, TemporaryDir: directory}), &recordingRunner{})
	_, _, err := processor.Process(context.Background(), artifact, processingBoundaryFixture())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Process() error = %v", err)
	}
}

func TestProcessRejectsBoundaryOutsideConfiguredRange(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.tif")
	if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundary := processingBoundaryForBBox([4]float64{-20, -20, -10, -10})
	runner := &recordingRunner{}
	processor := newProcessor(applyDefaults(Config{ArtifactRoot: directory, TemporaryDir: directory}), runner)
	_, _, err := processor.Process(context.Background(), fixtureArtifact(input), boundary)
	if !errors.Is(err, domain.ErrInvalidInput) || len(runner.calls) != 0 {
		t.Fatalf("Process() error=%v calls=%v", err, runner.calls)
	}
}

func TestValidateBoundaryForBBoxRejectsIncompleteAcquisitionCoverage(t *testing.T) {
	boundary := processingBoundaryForBBox(chinaBBox)
	for _, item := range []struct {
		name string
		bbox [4]float64
	}{
		{name: "西侧越界", bbox: [4]float64{chinaBBox[0] + 0.1, chinaBBox[1], chinaBBox[2], chinaBBox[3]}},
		{name: "东侧越界", bbox: [4]float64{chinaBBox[0], chinaBBox[1], chinaBBox[2] - 0.1, chinaBBox[3]}},
		{name: "南侧越界", bbox: [4]float64{chinaBBox[0], chinaBBox[1] + 0.1, chinaBBox[2], chinaBBox[3]}},
		{name: "北侧越界", bbox: [4]float64{chinaBBox[0], chinaBBox[1], chinaBBox[2], chinaBBox[3] - 0.1}},
		{name: "小范围完全位于边界内部", bbox: [4]float64{
			chinaBBox[0] + 1, chinaBBox[1] + 1, chinaBBox[2] - 1, chinaBBox[3] - 1}},
	} {
		t.Run(item.name, func(t *testing.T) {
			err := validateBoundaryForBBox(boundary.Geometry, item.bbox)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("validateBoundaryForBBox() error = %v", err)
			}
		})
	}
}

func TestVectorClipArgumentsBindBoundaryAndOutputOrder(t *testing.T) {
	arguments := vectorClipArguments("raw.geojson", "china.geojson", "clipped.geojson")
	want := "vector clip --input-format GeoJSON --like china.geojson --output-format GeoJSON --overwrite raw.geojson clipped.geojson"
	if strings.Join(arguments, " ") != want {
		t.Fatalf("vectorClipArguments()=%q", strings.Join(arguments, " "))
	}
}

func TestRasterInfoRejectsEPSG3857(t *testing.T) {
	payload := strings.Replace(validRasterInfo,
		`"authority":"EPSG","code":4326`, `"authority":"EPSG","code":3857`, 1)
	if err := validateSourceRasterInfo([]byte(payload), chinaBBox); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("validateSourceRasterInfo() error = %v", err)
	}
}

type recordingRunner struct {
	calls          [][]string
	info           string
	geoJSON        string
	rawGeoJSON     string
	clippedGeoJSON string
}

func (r *recordingRunner) Run(_ context.Context, _ string, arguments ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), arguments...))
	if len(arguments) > 1 && arguments[0] == "raster" && arguments[1] == "info" {
		if r.info != "" {
			return []byte(r.info), nil
		}
		return []byte(validRasterInfo), nil
	}
	if len(arguments) > 1 && arguments[0] == "raster" && arguments[1] == "zonal-stats" {
		return nil, os.WriteFile(arguments[3], []byte(r.geoJSON), 0o600)
	}
	if len(arguments) > 1 && arguments[0] == "raster" && arguments[1] == "polygonize" {
		payload := validPolygonGeoJSON
		if r.rawGeoJSON != "" {
			payload = r.rawGeoJSON
		}
		return nil, os.WriteFile(argumentValue(arguments, "--output"), []byte(payload), 0o600)
	}
	if len(arguments) > 1 && arguments[0] == "vector" && arguments[1] == "clip" {
		payload := validPolygonGeoJSON
		if r.clippedGeoJSON != "" {
			payload = r.clippedGeoJSON
		}
		return nil, os.WriteFile(arguments[len(arguments)-1], []byte(payload), 0o600)
	}
	return nil, nil
}

func containsText(values []string, expected string) bool {
	for _, value := range values {
		if strings.Contains(value, expected) {
			return true
		}
	}
	return false
}

func argumentValue(arguments []string, name string) string {
	for index := range arguments {
		if arguments[index] == name && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

func fixtureArtifact(path string) provenance.Artifact {
	observed := fixtureTime()
	content, _ := os.ReadFile(path)
	digest := sha256.Sum256(content)
	return provenance.Artifact{
		Reference: "https://example.test/20260824T0600.tif", MediaType: "image/tiff",
		LocalPath: path, SizeBytes: 7,
		Provenance: provenance.Provenance{
			Provider: "NASA Earthdata GIS", Dataset: "LHASA Hazard Today", DatasetVersion: "2.1",
			SourceRevision: `"revision-1"`, SourceURI: "https://example.test/20260824T0600.tif",
			DataKind: provenance.DataKindNowcast, RevisionFirstSeenAt: observed,
			FetchedAt: observed.Add(time.Minute), ValidFrom: observed,
			ValidTo: observed.Add(12 * time.Hour), SHA256: hex.EncodeToString(digest[:]), CRS: "EPSG:4326",
		},
	}
}

func fixtureTime() time.Time {
	return time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC)
}

func processingBoundaryFixture() hazard.ProcessingBoundary {
	return processingBoundaryForBBox(chinaBBox)
}

func processingBoundaryForBBox(bbox [4]float64) hazard.ProcessingBoundary {
	coordinates, _ := json.Marshal([][][]float64{{
		{bbox[0], bbox[1]}, {bbox[2], bbox[1]}, {bbox[2], bbox[3]},
		{bbox[0], bbox[3]}, {bbox[0], bbox[1]},
	}})
	value := hazard.ProcessingBoundary{
		Coverage: hazard.Coverage{
			Mode: hazard.CoverageAdministrativeBoundary, RegionCode: "CN",
			BoundaryID: "CHN-ADM0-1", BoundaryType: "ADM0", BoundaryVersion: "2024",
			Source: "fixture", License: "Public Domain", Reference: "https://example.test/china.geojson",
			SHA256: strings.Repeat("a", 64), CollectedAt: fixtureTime(),
		},
		Geometry:        spatial.Geometry{Type: "Polygon", Coordinates: coordinates},
		InputReferences: []string{"https://example.test/china.geojson"},
	}
	value.Coverage.GeometrySHA256, _ = hazard.BoundaryGeometryDigest(value.Geometry)
	return value
}

func processingBoundaryMultiPolygonForBBox(bbox [4]float64) hazard.ProcessingBoundary {
	width, height := bbox[2]-bbox[0], bbox[3]-bbox[1]
	mainEast := bbox[0] + width*0.7
	holeWest, holeEast := bbox[0]+width*0.2, bbox[0]+width*0.45
	holeSouth, holeNorth := bbox[1]+height*0.25, bbox[1]+height*0.65
	islandWest := bbox[0] + width*0.85
	islandSouth, islandNorth := bbox[1]+height*0.2, bbox[1]+height*0.55
	coordinates, _ := json.Marshal([][][][]float64{
		{
			{{bbox[0], bbox[1]}, {mainEast, bbox[1]}, {mainEast, bbox[3]},
				{bbox[0], bbox[3]}, {bbox[0], bbox[1]}},
			{{holeWest, holeSouth}, {holeWest, holeNorth}, {holeEast, holeNorth},
				{holeEast, holeSouth}, {holeWest, holeSouth}},
		},
		{{{islandWest, islandSouth}, {bbox[2], islandSouth}, {bbox[2], islandNorth},
			{islandWest, islandNorth}, {islandWest, islandSouth}}},
	})
	value := processingBoundaryForBBox(bbox)
	value.Geometry.Type = "MultiPolygon"
	value.Geometry.Coordinates = coordinates
	value.Coverage.GeometrySHA256, _ = hazard.BoundaryGeometryDigest(value.Geometry)
	return value
}

const validRasterInfo = `{
  "driver":"GTiff",
  "size":[43200,21600],
  "geoTransform":[-180,0.008333333333333333,0,90,0,-0.008333333333333333],
  "stac":{"proj:projjson":{"id":{"authority":"EPSG","code":4326}}},
  "bands":[{"type":"Float32","noDataValue":-9999,"minimum":0.02,"maximum":0.95}]
}`

const validPolygonGeoJSON = `{
  "type":"FeatureCollection",
  "features":[{
    "type":"Feature",
    "properties":{"level":2},
    "geometry":{"type":"Polygon","coordinates":[[[100,30],[101,30],[101,31],[100,31],[100,30]]]}
  }]
}`

const emptyGeoJSON = `{"type":"FeatureCollection","features":[]}`

const validGeoJSON = `{
  "type":"FeatureCollection",
  "name":"stats",
  "features":[{
    "type":"Feature",
    "properties":{"level":2,"min":0.51,"mean":0.62,"max":0.77},
    "geometry":{"type":"Polygon","coordinates":[[[100,30],[101,30],[101,31],[100,31],[100,30]]]}
  }]
}`
