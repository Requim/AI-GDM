package lhasa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestTiledFetcherDownloadsMosaicsAndPersistsPartChecksums(t *testing.T) {
	fixture := newDownloadFixture(t, nil)
	defer fixture.server.Close()
	artifact, err := fixture.provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mosaicker := &mosaickerStub{}
	store := artifactstore.New(filepath.Join(t.TempDir(), "artifacts"), 16<<20)
	fetcher := newTestTiledFetcher(t, fixture, mosaicker, store)

	stored, err := fetcher.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if mosaicker.calls != 1 || stored.LocalPath == "" || stored.Provenance.SHA256 == "" {
		t.Fatalf("stored=%+v mosaicCalls=%d", stored, mosaicker.calls)
	}
	for _, part := range stored.Provenance.SourceParts {
		if len(part.SHA256) != 64 {
			t.Fatalf("part checksum = %+v", part)
		}
	}
}

func TestTiledFetcherRejectsChangedGetETag(t *testing.T) {
	fixture := newDownloadFixture(t, func(method string, index int, _ int64) string {
		if method == http.MethodGet && index == 1 {
			return `"changed"`
		}
		return fmt.Sprintf(`"revision-%d"`, index)
	})
	defer fixture.server.Close()
	artifact, err := fixture.provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mosaicker := &mosaickerStub{}
	saver := &saverStub{}
	fetcher := newTestTiledFetcher(t, fixture, mosaicker, saver)

	_, err = fetcher.Fetch(context.Background(), artifact)
	if !errorsIsProviderUnavailable(err) {
		t.Fatalf("Fetch() error = %v", err)
	}
	if mosaicker.calls != 0 || saver.calls != 0 {
		t.Fatalf("changed GET still committed: mosaic=%d save=%d", mosaicker.calls, saver.calls)
	}
}

func TestTiledFetcherRejectsRevisionChangedAfterDownload(t *testing.T) {
	var changed atomic.Bool
	fixture := newDownloadFixture(t, func(method string, index int, getCount int64) string {
		if method == http.MethodHead && changed.Load() && index == 0 {
			return `"changed"`
		}
		if method == http.MethodGet && getCount == 4 {
			changed.Store(true)
		}
		return fmt.Sprintf(`"revision-%d"`, index)
	})
	defer fixture.server.Close()
	artifact, err := fixture.provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mosaicker := &mosaickerStub{}
	saver := &saverStub{}
	fetcher := newTestTiledFetcher(t, fixture, mosaicker, saver)

	_, err = fetcher.Fetch(context.Background(), artifact)
	if !errorsIsProviderUnavailable(err) {
		t.Fatalf("Fetch() error = %v", err)
	}
	if mosaicker.calls != 0 || saver.calls != 0 {
		t.Fatalf("post-check handling: mosaic=%d save=%d", mosaicker.calls, saver.calls)
	}
}

func TestTiledFetcherRetriesBodyReadTimeout(t *testing.T) {
	downloader := &sequenceFetcher{
		errors:   []error{fmt.Errorf("读取响应体: %w", context.DeadlineExceeded), nil},
		artifact: provenance.Artifact{Reference: "stored"},
	}
	fetcher := &TiledFetcher{
		maxAttempts: 2, retryBackoff: time.Nanosecond,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	stored, err := fetcher.downloadPart(context.Background(), downloader,
		provenance.Artifact{Reference: "tile"}, 0, 1)
	if err != nil || stored.Reference != "stored" || downloader.calls != 2 {
		t.Fatalf("stored=%+v error=%v calls=%d", stored, err, downloader.calls)
	}
}

type downloadFixture struct {
	server   *httptest.Server
	provider *Provider
	client   *httpclient.Client
}

func newDownloadFixture(t *testing.T,
	revision func(method string, index int, getCount int64) string,
) downloadFixture {
	t.Helper()
	bbox := [4]float64{115, 39, 117, 41}
	tiles, err := buildTiles(bbox, defaultResolution, 128, 128)
	if err != nil {
		t.Fatal(err)
	}
	server := earthdataDownloadServer(t, tiles, revision)
	client := httpclient.New(httpclient.Options{AllowHTTP: true})
	provider := newTestProvider(t, client, server.URL, bbox, 128, 128, 1<<20)
	return downloadFixture{server: server, provider: provider, client: client}
}

func earthdataDownloadServer(t *testing.T, tiles []sourceTile,
	revision func(method string, index int, getCount int64) string,
) *httptest.Server {
	t.Helper()
	var gets atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		index := downloadTileIndex(t, request, tiles)
		if index < 0 {
			http.Error(w, "unexpected tile", http.StatusBadRequest)
			return
		}
		count := gets.Load()
		if request.Method == http.MethodGet {
			count = gets.Add(1)
		}
		etag := fmt.Sprintf(`"revision-%d"`, index)
		if revision != nil {
			etag = revision(request.Method, index, count)
		}
		payload := tilePayload(int(partSize(index)))
		w.Header().Set("Content-Type", "image/tiff")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Header().Set("ETag", etag)
		if request.Method == http.MethodGet {
			_, _ = w.Write(payload)
		}
	}))
}

func downloadTileIndex(t *testing.T, request *http.Request, tiles []sourceTile) int {
	t.Helper()
	if request.Method != http.MethodHead && request.Method != http.MethodGet {
		t.Errorf("method = %s", request.Method)
		return -1
	}
	if request.URL.Path != "/ImageServer/exportImage" {
		t.Errorf("path = %s", request.URL.Path)
		return -1
	}
	assertCommonExportQuery(t, request)
	for index, tile := range tiles {
		if request.URL.Query().Get("bbox") == bboxValue(tile.bbox) &&
			request.URL.Query().Get("size") == fmt.Sprintf("%d,%d", tile.width, tile.height) {
			return index
		}
	}
	return -1
}

func newTestTiledFetcher(t *testing.T, fixture downloadFixture,
	mosaicker RasterMosaicker, saver ArtifactSaver,
) *TiledFetcher {
	t.Helper()
	fetcher, err := NewTiledFetcher(fixture.client, fixture.provider, mosaicker, saver, FetcherConfig{
		TemporaryDir: t.TempDir(), MaxPartBytes: 1 << 20, MaxBytes: 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fetcher
}

type mosaickerStub struct {
	calls int
	err   error
}

func (s *mosaickerStub) Mosaic(_ context.Context, _ []string, output string) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return os.WriteFile(output, tilePayload(2048), 0o600)
}

type saverStub struct {
	calls int
}

type sequenceFetcher struct {
	errors   []error
	artifact provenance.Artifact
	calls    int
}

func (s *sequenceFetcher) Fetch(context.Context, provenance.Artifact) (provenance.Artifact, error) {
	index := s.calls
	s.calls++
	return s.artifact, s.errors[index]
}

func (s *saverStub) Save(_ context.Context, artifact provenance.Artifact,
	source io.Reader,
) (provenance.Artifact, error) {
	s.calls++
	_, _ = io.Copy(io.Discard, source)
	return artifact, nil
}

func tilePayload(size int) []byte {
	value := make([]byte, size)
	copy(value, []byte{'I', 'I', 42, 0})
	return value
}

func errorsIsProviderUnavailable(err error) bool {
	return err != nil && errors.Is(err, domain.ErrProviderUnavailable)
}
