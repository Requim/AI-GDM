package gdal

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/geoboundaries"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	lhasaprovider "github.com/Requim/AI-GDM/internal/adapters/provider/lhasa"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const earthdataLiveMaxBytes = 16 << 20

func TestLiveEarthdataChinaAcquisition(t *testing.T) {
	if os.Getenv("EARTHDATA_CHINA_LIVE_TEST") != "1" {
		t.Skip("未启用 EARTHDATA_CHINA_LIVE_TEST")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	directory := t.TempDir()
	client := newLiveEarthdataClient()
	provider, err := lhasaprovider.New(client, lhasaprovider.Config{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := provider.DiscoverLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mosaicker, err := NewMosaicker(MosaicConfig{})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := lhasaprovider.NewTiledFetcher(client, provider, mosaicker,
		artifactstore.New(directory, 512<<20), lhasaprovider.FetcherConfig{
			TemporaryDir: directory, MaxPartBytes: 32 << 20, MaxBytes: 512 << 20,
		})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fetcher.Fetch(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	boundaryProvider, err := geoboundaries.New(geoboundaries.Options{Client: newLiveEarthdataClient()})
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := boundaryProvider.RiskBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateBoundaryForBBox(boundary.Geometry, chinaBBox); err != nil {
		t.Fatal(err)
	}
	if len(stored.Provenance.SourceParts) != 12 || stored.LocalPath == "" || stored.Provenance.SHA256 == "" {
		t.Fatalf("Earthdata 中国区域组合制品无效：%+v", stored)
	}
	processor, err := New(Config{ArtifactRoot: directory, TemporaryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, zones, err := processor.Process(ctx, stored, boundary)
	if err != nil {
		t.Fatal(err)
	}
	assertEarthdataSnapshot(t, snapshot, len(zones))
	if len(zones) == 0 || snapshot.Coverage == nil ||
		snapshot.Coverage.Identity() != boundary.Coverage.Identity() {
		t.Fatalf("Earthdata 中国区域风险结果无效：snapshot=%+v zones=%d", snapshot, len(zones))
	}
	t.Logf("Earthdata 中国区域获取和处理通过：parts=%d bytes=%d zones=%d sha256=%s",
		len(stored.Provenance.SourceParts), stored.SizeBytes, len(zones), stored.Provenance.SHA256)
}

func TestLiveEarthdataPipeline(t *testing.T) {
	if os.Getenv("EARTHDATA_LIVE_TEST") != "1" {
		t.Skip("未启用 EARTHDATA_LIVE_TEST")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	directory := t.TempDir()
	bbox := [4]float64{115, 39, 117, 41}
	client := newLiveEarthdataClient()
	provider, err := lhasaprovider.New(client, lhasaprovider.Config{
		BBox: bbox, TileWidth: 128, TileHeight: 128,
		MaxPartBytes: 4 << 20, MaxBytes: earthdataLiveMaxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := provider.DiscoverLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mosaicker, err := NewMosaicker(MosaicConfig{BBox: bbox})
	if err != nil {
		t.Fatal(err)
	}
	downloader, err := lhasaprovider.NewTiledFetcher(client, provider, mosaicker,
		artifactstore.New(directory, earthdataLiveMaxBytes), lhasaprovider.FetcherConfig{
			TemporaryDir: directory, MaxPartBytes: 4 << 20, MaxBytes: earthdataLiveMaxBytes,
		})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := downloader.Fetch(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := New(Config{ArtifactRoot: directory, TemporaryDir: directory, BBox: bbox})
	if err != nil {
		t.Fatal(err)
	}
	boundary := processingBoundaryForBBox(bbox)
	snapshot, zones, err := processor.Process(ctx, stored, boundary)
	if err != nil {
		t.Fatal(err)
	}
	assertEarthdataSnapshot(t, snapshot, len(zones))
}

func newLiveEarthdataClient() *httpclient.Client {
	return httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: 3 * time.Minute}, MaxAttempts: 2,
		Limiter: rate.NewLimiter(rate.Every(time.Second), 1),
	})
}

func assertEarthdataSnapshot(t *testing.T, snapshot hazard.Snapshot, zoneCount int) {
	t.Helper()
	if snapshot.Status != hazard.SnapshotAvailable || snapshot.Source.Provider != lhasaprovider.ProviderName ||
		snapshot.Source.SourceRevision == "" || snapshot.Source.SHA256 == "" {
		t.Fatalf("Earthdata 快照无效：%+v", snapshot)
	}
	if !snapshot.Source.ObservedAt.IsZero() || snapshot.Source.RevisionFirstSeenAt.IsZero() {
		t.Fatalf("Earthdata 时间语义无效：%+v", snapshot.Source)
	}
	for _, part := range snapshot.Source.SourceParts {
		if len(part.SHA256) != 64 {
			t.Fatalf("Earthdata 分片摘要无效：%+v", part)
		}
	}
	t.Logf("Earthdata 实时链路通过：revision=%s zones=%d sha256=%s",
		snapshot.Source.SourceRevision, zoneCount, snapshot.Source.SHA256)
}
