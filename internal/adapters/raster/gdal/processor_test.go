package gdal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
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
	snapshot, zones, err := processor.Process(context.Background(), fixtureArtifact(input))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != hazard.SnapshotAvailable || snapshot.Source.TransformVersion != TransformVersion {
		t.Fatalf("Snapshot = %+v", snapshot)
	}
	if len(zones) != 1 || zones[0].Level != hazard.RiskHigh || zones[0].Mean != 0.62 {
		t.Fatalf("Zones = %+v", zones)
	}
	if len(runner.calls) != 7 || !strings.Contains(strings.Join(runner.calls[1], " "), bboxValue(chinaBBox)) {
		t.Fatalf("Calls = %+v", runner.calls)
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
	_, _, err := processor.Process(context.Background(), fixtureArtifact(input))
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
	_, _, err := processor.Process(context.Background(), artifact)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Process() error = %v", err)
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
	calls   [][]string
	info    string
	geoJSON string
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
		return nil, os.WriteFile(argumentValue(arguments, "--output"), []byte(validPolygonGeoJSON), 0o600)
	}
	return nil, nil
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
			Provider: "NASA", Dataset: "LHASA NRT Hazard", DatasetVersion: "2.1.1",
			SourceURI: "https://example.test/20260824T0600.tif", DataKind: provenance.DataKindNowcast,
			ObservedAt: observed, FetchedAt: observed.Add(time.Minute), ValidFrom: observed,
			ValidTo: observed.Add(12 * time.Hour), SHA256: hex.EncodeToString(digest[:]), CRS: "EPSG:4326",
		},
	}
}

func fixtureTime() time.Time {
	return time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC)
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

const validGeoJSON = `{
  "type":"FeatureCollection",
  "name":"stats",
  "features":[{
    "type":"Feature",
    "properties":{"level":2,"min":0.51,"mean":0.62,"max":0.77},
    "geometry":{"type":"Polygon","coordinates":[[[100,30],[101,30],[101,31],[100,31],[100,30]]]}
  }]
}`
