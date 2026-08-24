package openmeteo

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestLiveForecast(t *testing.T) {
	if os.Getenv("OPEN_METEO_LIVE_TEST") != "1" {
		t.Skip("未启用 OPEN_METEO_LIVE_TEST")
	}
	client := httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: 20 * time.Second},
	})
	provider := New(client, Config{APIKey: os.Getenv("OPEN_METEO_API_KEY")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	points := []spatial.Point{
		{Longitude: 116.4074, Latitude: 39.9042},
		{Longitude: 104.0665, Latitude: 30.5723},
	}
	snapshots, err := provider.Forecast(ctx, points, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != len(points) {
		t.Fatalf("快照数量 = %d", len(snapshots))
	}
	for index, snapshot := range snapshots {
		if len(snapshot.Hourly) != 6 {
			t.Fatalf("第 %d 个快照小时数 = %d", index, len(snapshot.Hourly))
		}
		if err = snapshot.Source.Validate(); err != nil {
			t.Fatalf("第 %d 个来源无效: %v", index, err)
		}
	}
	t.Logf("Open-Meteo 在线契约通过，来源：%s", snapshots[0].Source.SourceURI)
}
