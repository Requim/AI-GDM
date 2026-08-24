package artifactstore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestDownloaderFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Request-ID", "download-1")
		_, _ = w.Write([]byte{'I', 'I', 42, 0, 'd', 'a', 't', 'a'})
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{AllowHTTP: true})
	downloader := NewDownloader(client, New(t.TempDir(), 1024), 1024)
	artifact := provenance.Artifact{
		Reference: server.URL + "/latest.tif", MediaType: "image/tiff",
		Provenance: provenance.Provenance{
			Provider: "test", Dataset: "hazard", SourceURI: server.URL,
			DataKind: provenance.DataKindNowcast, FetchedAt: time.Now().UTC(),
		},
	}
	stored, err := downloader.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SizeBytes != 8 || stored.MediaType != "image/tiff" || stored.Provenance.ProviderRequestID != "download-1" {
		t.Fatalf("Fetch() = %+v", stored)
	}
}

func TestDownloaderRejectsInvalidTIFFSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/tiff")
		_, _ = w.Write([]byte("not-a-tiff"))
	}))
	defer server.Close()
	downloader := NewDownloader(httpclient.New(httpclient.Options{AllowHTTP: true}), New(t.TempDir(), 1024), 1024)
	artifact := fixtureArtifact(time.Now().UTC())
	artifact.Reference = server.URL + "/latest.tif"
	artifact.Provenance.SourceURI = artifact.Reference
	_, err := downloader.Fetch(context.Background(), artifact)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Fetch() error = %v", err)
	}
}

func TestDownloaderRejectsUnexpectedMediaType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>temporary error</html>"))
	}))
	defer server.Close()
	downloader := NewDownloader(httpclient.New(httpclient.Options{AllowHTTP: true}), New(t.TempDir(), 1024), 1024)
	artifact := fixtureArtifact(time.Now().UTC())
	artifact.Reference = server.URL + "/latest.tif"
	artifact.Provenance.SourceURI = artifact.Reference
	_, err := downloader.Fetch(context.Background(), artifact)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Fetch() error = %v", err)
	}
}
