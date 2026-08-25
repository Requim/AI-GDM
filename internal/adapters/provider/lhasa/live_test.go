package lhasa

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
)

func TestLiveDiscovery(t *testing.T) {
	if os.Getenv("LHASA_LIVE_TEST") != "1" {
		t.Skip("未启用 LHASA_LIVE_TEST")
	}
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxAttempts: 2})
	provider, err := New(client, Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	artifact, err := provider.DiscoverLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	if artifact.Provenance.SourceRevision == "" || artifact.Provenance.RevisionFirstSeenAt.IsZero() ||
		!artifact.Provenance.ObservedAt.IsZero() || len(artifact.Provenance.SourceParts) != 12 {
		t.Fatalf("Earthdata 来源时间或修订无效：%+v", artifact.Provenance)
	}
	t.Logf("发现 LHASA 制品：%s", artifact.Reference)
}
