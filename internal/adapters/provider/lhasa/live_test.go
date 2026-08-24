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
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	provider := New(client, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	artifact, err := provider.DiscoverLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Logf("发现 LHASA 制品：%s", artifact.Reference)
}
