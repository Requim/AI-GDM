package nccs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
)

func TestLatestLinkRestrictsOriginDirectoryAndDate(t *testing.T) {
	directory, _ := url.Parse("https://example.test/nrt/")
	body := []byte(`<a href="20260901T0000.tif">old</a><a href="/nrt/20260908T0000.tif">new</a>
	<a href="https://evil.test/nrt/20260909T0000.tif">evil</a>
	<a href="../20260910T0000.tif">parent</a><a href="20269999T0000.tif">invalid</a>`)
	got, err := latestLink(body, directory)
	if err != nil || got != "https://example.test/nrt/20260908T0000.tif" {
		t.Fatalf("%s %v", got, err)
	}
}

type rasterServer struct {
	data    []byte
	fail    atomic.Bool
	wrong   atomic.Bool
	ranges  atomic.Int64
	resumes atomic.Int64
}

func (s *rasterServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		_, _ = w.Write([]byte(`<a href="20260907T0000.tif">file</a>`))
		return
	}
	var start, end int
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || end >= len(s.data) {
		w.WriteHeader(416)
		return
	}
	w.Header().Set("ETag", `"revision-1"`)
	w.Header().Set("Last-Modified", time.Now().UTC().Add(-time.Hour).Truncate(time.Hour).Format(http.TimeFormat))
	w.Header().Set("Content-Type", "image/tiff")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.data)))
	w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
	if end > 0 && s.wrong.Load() {
		w.Header().Set("ETag", `"changed"`)
	}
	w.WriteHeader(206)
	if end == 0 {
		_, _ = w.Write(s.data[:1])
		return
	}
	s.ranges.Add(1)
	if start%1024 != 0 {
		s.resumes.Add(1)
	}
	if s.fail.Load() {
		_, _ = w.Write(s.data[start:min(start+64, end+1)])
		return
	}
	_, _ = w.Write(s.data[start : end+1])
}

func setupFetcher(t *testing.T, server *rasterServer, root string) (*Provider, *Fetcher) {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(server.serve))
	t.Cleanup(httpServer.Close)
	client := httpclient.New(httpclient.Options{HTTPClient: httpServer.Client(), AllowHTTP: true, MaxAttempts: 1})
	provider, err := New(client, httpServer.URL+"/", 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(client, provider, artifactstore.New(root, MaxFileBytes),
		FetchConfig{Directory: filepath.Join(root, "downloads"), ChunkBytes: 1024,
			Workers: 1, Attempts: 1, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return provider, fetcher
}

func TestFetcherResumesAndReusesCompleteFile(t *testing.T) {
	server := &rasterServer{data: append([]byte{'I', 'I', 42, 0}, bytes.Repeat([]byte{17}, 2496)...)}
	root := t.TempDir()
	provider, fetcher := setupFetcher(t, server, root)
	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server.fail.Store(true)
	if _, err = fetcher.Fetch(context.Background(), artifact); err == nil {
		t.Fatal("截断未拒绝")
	}
	server.fail.Store(false)
	restarted, err := NewFetcher(fetcher.client, provider, fetcher.store, fetcher.config)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := restarted.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(stored.LocalPath)
	if err != nil || !bytes.Equal(content, server.data) || server.resumes.Load() == 0 {
		t.Fatalf("恢复结果错误: %v resumes=%d", err, server.resumes.Load())
	}
	count := server.ranges.Load()
	if _, err = restarted.Fetch(context.Background(), artifact); err != nil || count != server.ranges.Load() {
		t.Fatalf("完整文件未复用: %v", err)
	}
}

func TestFetcherRejectsVersionChangeWithoutPublishing(t *testing.T) {
	server := &rasterServer{data: append([]byte{'I', 'I', 42, 0}, bytes.Repeat([]byte{17}, 2044)...)}
	root := t.TempDir()
	provider, fetcher := setupFetcher(t, server, root)
	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server.wrong.Store(true)
	if _, err = fetcher.Fetch(context.Background(), artifact); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("版本变化未拒绝: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*.tif"))
	if len(matches) != 0 {
		t.Fatal("失败下载发布了制品")
	}
}

func TestBlockChecksumRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stampBlock(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedBlock(path, 8); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
}

func TestRangeValidationRejectsIgnoredOrShiftedResponse(t *testing.T) {
	server := &rasterServer{data: append([]byte{'I', 'I', 42, 0}, bytes.Repeat([]byte{17}, 2044)...)}
	provider, _ := setupFetcher(t, server, t.TempDir())
	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []*http.Response{
		{StatusCode: 200, Header: http.Header{}},
		{StatusCode: 206, Header: http.Header{"Etag": {`"revision-1"`}, "Content-Range": {"bytes 1-1024/2048"}}},
	} {
		if err = validateRange(response, artifact, 0, 1023); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatal(err)
		}
	}
}
