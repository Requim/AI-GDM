package worldpop

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
)

func TestLivePopulation(t *testing.T) {
	if os.Getenv("AI_GDM_LIVE_EXPOSURE") != "1" {
		t.Skip("未启用真实暴露供应商验证")
	}
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxAttempts: 2})
	provider, err := New(Options{Client: client, MaxPolls: 60, PollInterval: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	geometry := json.RawMessage(`{"type":"Polygon","coordinates":[[[116.400,39.905],[116.405,39.905],[116.405,39.910],[116.400,39.910],[116.400,39.905]]]}`)
	result, err := provider.Population(ctx, exposurecollection.PopulationQuery{
		Geometry: geometry, ExpectedAreaSquareMeter: 240_000, Year: 2026})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" || result.Total < 0 || result.DataSource == "" || len(result.InputReferences) != 2 {
		t.Fatalf("Population()=%+v", result)
	}
}
