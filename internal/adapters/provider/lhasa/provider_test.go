package lhasa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
)

func TestDiscoverLatest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>
            <a href="20260823T0000.tif">old</a>
            <a href="20260824T0600.tif">latest</a>
            <a href="notes.txt">ignore</a>
        </body></html>`))
	}))
	defer server.Close()
	provider := New(httpclient.New(httpclient.Options{AllowHTTP: true}), Config{BaseURL: server.URL})
	provider.now = func() time.Time { return time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC) }
	artifact, err := provider.DiscoverLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Reference != server.URL+"/nrt/hazard/tif/20260824T0600.tif" {
		t.Fatalf("Reference = %q", artifact.Reference)
	}
	if artifact.Provenance.ObservedAt.Hour() != 6 || artifact.Provenance.Stale {
		t.Fatalf("Provenance = %+v", artifact.Provenance)
	}
}

func TestDiscoverLatestReportsMissingFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="notes.txt">notes</a></body></html>`))
	}))
	defer server.Close()
	provider := New(httpclient.New(httpclient.Options{AllowHTTP: true}), Config{BaseURL: server.URL})
	_, err := provider.DiscoverLatest(context.Background())
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("DiscoverLatest() error = %v", err)
	}
}

func TestParseDirectoryDeduplicates(t *testing.T) {
	content := []byte(`<a href="20260824T0000.tif">a</a>
        <a href="20260824T0000.tif">b</a>
        <a href="https://attacker.test/20260824T1200.tif">external</a>`)
	values, err := parseDirectory(content, "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("parseDirectory() count = %d", len(values))
	}
}
