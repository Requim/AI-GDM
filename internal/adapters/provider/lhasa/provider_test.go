package lhasa

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestDefaultGridCoversChinaWithoutGaps(t *testing.T) {
	tiles, err := buildTiles(defaultBBox, defaultResolution, defaultTileWidth, defaultTileHeight)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 12 {
		t.Fatalf("tiles = %d", len(tiles))
	}
	var pixels int64
	for _, tile := range tiles {
		pixels += int64(tile.width) * int64(tile.height)
	}
	last := tiles[len(tiles)-1]
	if pixels != 7392*4272 || last.width != 1248 || last.height != 176 {
		t.Fatalf("pixels=%d last=%+v", pixels, last)
	}
	if !near(last.bbox[2], defaultBBox[2]) || !near(last.bbox[3], defaultBBox[3]) {
		t.Fatalf("last bbox = %+v", last.bbox)
	}
}

func TestDiscoverLatestEarthdataRevision(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	bbox := [4]float64{115, 39, 117, 41}
	tiles, err := buildTiles(bbox, defaultResolution, 128, 128)
	if err != nil {
		t.Fatal(err)
	}
	server := earthdataTileServer(t, tiles, nil)
	defer server.Close()
	client := httpclient.New(httpclient.Options{AllowHTTP: true, Now: func() time.Time { return now }})
	provider := newTestProvider(t, client, server.URL, bbox, 128, 128, 1<<20)

	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Provenance.SourceParts) != 4 || artifact.SizeBytes != expectedPartSizeSum(tiles) {
		t.Fatalf("Artifact = %+v", artifact)
	}
	if artifact.Provenance.SourceRevision != provenance.CompositeSourceRevision(artifact.Provenance.SourceParts) ||
		artifact.Provenance.Provider != ProviderName || artifact.Provenance.Dataset != DatasetName {
		t.Fatalf("Provenance = %+v", artifact.Provenance)
	}
	if !artifact.Provenance.ObservedAt.IsZero() || !artifact.Provenance.RevisionFirstSeenAt.Equal(now) ||
		!artifact.Provenance.ValidTo.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("Provenance time = %+v", artifact.Provenance)
	}
}

func TestVerifyCurrentRejectsChangedPart(t *testing.T) {
	bbox := [4]float64{115, 39, 117, 41}
	tiles, err := buildTiles(bbox, defaultResolution, 128, 128)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	server := earthdataTileServer(t, tiles, func(index int) string {
		if changed.Load() && index == 1 {
			return `"changed"`
		}
		return fmt.Sprintf(`"revision-%d"`, index)
	})
	defer server.Close()
	provider := newTestProvider(t, httpclient.New(httpclient.Options{AllowHTTP: true}),
		server.URL, bbox, 128, 128, 1<<20)
	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if err = provider.VerifyCurrent(context.Background(), artifact); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("VerifyCurrent() error = %v", err)
	}
}

func TestDiscoverLatestRejectsInvalidEarthdataHeaders(t *testing.T) {
	tests := []struct {
		name, mediaType, length, etag string
	}{
		{name: "非 TIFF", mediaType: "text/html", length: "2048", etag: `"revision"`},
		{name: "错误正文", mediaType: "image/tiff", length: "156", etag: `"revision"`},
		{name: "超大响应", mediaType: "image/tiff", length: "2048", etag: `"revision"`},
		{name: "缺少修订", mediaType: "image/tiff", length: "1024"},
		{name: "弱修订", mediaType: "image/tiff", length: "1024", etag: `W/"revision"`},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			server := earthdataHeaderServer(item.mediaType, item.length, item.etag)
			defer server.Close()
			provider := newTestProvider(t, httpclient.New(httpclient.Options{AllowHTTP: true}),
				server.URL, [4]float64{115, 39, 115.1, 39.1}, 128, 128, 1024)
			_, err := provider.DiscoverLatest(context.Background())
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("DiscoverLatest() error = %v", err)
			}
		})
	}
}

func TestNewRejectsInvalidEarthdataConfig(t *testing.T) {
	tests := []Config{
		{ServiceURL: "https://example.test/not-image-service"},
		{ServiceURL: "https://example.test/ImageServer?unexpected=value"},
		{ServiceURL: "https://example.test/ImageServer", BBox: [4]float64{10, 10, 5, 5}},
		{ServiceURL: "https://example.test/ImageServer", BBox: [4]float64{0, 0, 1, 1}, Resolution: 0.3},
		{ServiceURL: "https://example.test/ImageServer", TileWidth: maxServiceWidth + 1},
		{ServiceURL: "https://example.test/ImageServer", TileHeight: maxServiceHeight + 1},
		{ServiceURL: "https://example.test/ImageServer", MaxPartBytes: 100, MaxBytes: 1024},
	}
	for _, config := range tests {
		if _, err := New(nil, config); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("New(%+v) error = %v", config, err)
		}
	}
}

func earthdataTileServer(t *testing.T, tiles []sourceTile,
	revision func(int) string,
) *httptest.Server {
	t.Helper()
	if revision == nil {
		revision = func(index int) string { return fmt.Sprintf(`"revision-%d"`, index) }
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		index := tileRequestIndex(t, request, tiles)
		if index < 0 {
			http.Error(w, "unexpected tile", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/tiff")
		w.Header().Set("Content-Length", fmt.Sprint(partSize(index)))
		w.Header().Set("ETag", revision(index))
	}))
}

func tileRequestIndex(t *testing.T, request *http.Request, tiles []sourceTile) int {
	t.Helper()
	if request.Method != http.MethodHead || request.URL.Path != "/ImageServer/exportImage" {
		t.Errorf("Request = %s %s", request.Method, request.URL)
		return -1
	}
	assertCommonExportQuery(t, request)
	for index, tile := range tiles {
		if request.URL.Query().Get("bbox") == bboxValue(tile.bbox) &&
			request.URL.Query().Get("size") == fmt.Sprintf("%d,%d", tile.width, tile.height) {
			return index
		}
	}
	t.Errorf("unexpected tile query = %s", request.URL.RawQuery)
	return -1
}

func assertCommonExportQuery(t *testing.T, request *http.Request) {
	t.Helper()
	want := map[string]string{
		"bboxSR": "4326", "imageSR": "4326", "format": "tiff", "pixelType": "F32",
		"noData": "-9999", "interpolation": "RSP_NearestNeighbor",
		"adjustAspectRatio": "false", "f": "image",
	}
	for name, expected := range want {
		if actual := request.URL.Query().Get(name); actual != expected {
			t.Errorf("query %s = %q", name, actual)
		}
	}
}

func newTestProvider(t *testing.T, client *httpclient.Client, baseURL string,
	bbox [4]float64, tileWidth, tileHeight int, maxPartBytes int64,
) *Provider {
	t.Helper()
	provider, err := New(client, Config{
		ServiceURL: baseURL + "/ImageServer", StaleAfter: 12 * time.Hour,
		BBox: bbox, Resolution: defaultResolution, TileWidth: tileWidth, TileHeight: tileHeight,
		MaxPartBytes: maxPartBytes, MaxBytes: 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func earthdataHeaderServer(mediaType, length, etag string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Content-Length", length)
		w.Header().Set("ETag", etag)
	}))
}

func expectedPartSizeSum(tiles []sourceTile) int64 {
	var total int64
	for index := range tiles {
		total += partSize(index)
	}
	return total
}

func partSize(index int) int64 { return int64(minimumTIFFBytes + index) }

func near(left, right float64) bool { return left-right < 1e-9 && right-left < 1e-9 }
