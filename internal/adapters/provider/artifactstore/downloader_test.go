package artifactstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestDownloaderFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/tiff")
		w.Header().Set("X-Request-ID", "download-1")
		_, _ = w.Write([]byte("tiff-data"))
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{AllowHTTP: true})
	downloader := NewDownloader(client, New(t.TempDir(), 1024), 1024)
	artifact := provenance.Artifact{
		Reference: server.URL + "/latest.tif", MediaType: "application/octet-stream",
		Provenance: provenance.Provenance{
			Provider: "test", Dataset: "hazard", SourceURI: server.URL,
			DataKind: provenance.DataKindNowcast, FetchedAt: time.Now().UTC(),
		},
	}
	stored, err := downloader.Fetch(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SizeBytes != 9 || stored.MediaType != "image/tiff" || stored.Provenance.ProviderRequestID != "download-1" {
		t.Fatalf("Fetch() = %+v", stored)
	}
}
