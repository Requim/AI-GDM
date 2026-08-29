package overpass

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
)

func TestLiveInfrastructure(t *testing.T) {
	if os.Getenv("AI_GDM_LIVE_EXPOSURE") != "1" {
		t.Skip("未启用真实暴露供应商验证")
	}
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 40 * time.Second}, MaxAttempts: 2})
	provider, err := New(Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := provider.Infrastructure(ctx, exposurecollection.InfrastructureQuery{
		Bounds: exposurecollection.Bounds{South: 39.905, West: 116.400, North: 39.918, East: 116.420}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[applicationloss.LossFeatureKind]bool{}
	for _, value := range result.Features {
		kinds[value.Kind] = true
	}
	if !kinds[applicationloss.LossFeatureRoad] || !kinds[applicationloss.LossFeatureFacility] {
		t.Fatalf("Infrastructure() 未同时返回道路和设施: %+v", result)
	}
}
