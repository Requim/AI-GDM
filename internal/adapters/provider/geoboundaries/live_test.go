package geoboundaries

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
)

func TestLiveBoundary(t *testing.T) {
	if os.Getenv("AI_GDM_LIVE_EXPOSURE") != "1" {
		t.Skip("未启用真实暴露供应商验证")
	}
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxAttempts: 2})
	provider, err := New(Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := provider.Boundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.BoundaryID == "" || result.RegionCode != "CN" || len(result.Digest) != 64 ||
		len(result.Geometry) == 0 || len(result.InputReferences) != 2 {
		t.Fatalf("Boundary()=%+v", result)
	}
	t.Logf("boundary=%s digest=%s bytes=%d", result.BoundaryID, result.Digest, len(result.Geometry))
}
