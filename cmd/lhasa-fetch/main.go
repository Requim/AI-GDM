package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/lhasa"
	"github.com/Requim/AI-GDM/internal/adapters/raster/gdal"
	"github.com/Requim/AI-GDM/internal/application/collection"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	client := httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: 3 * time.Minute}, Logger: logger,
	})
	provider, err := lhasa.New(client, lhasa.Config{ServiceURL: os.Getenv("LHASA_EARTHDATA_URL")})
	if err != nil {
		return fmt.Errorf("创建 Earthdata LHASA 发现适配器: %w", err)
	}
	store := artifactstore.New(dataDirectory(), 512<<20)
	mosaicker, err := gdal.NewMosaicker(gdal.MosaicConfig{Binary: gdalBinary()})
	if err != nil {
		return fmt.Errorf("创建 LHASA 栅格拼接器: %w", err)
	}
	downloader, err := lhasa.NewTiledFetcher(client, provider, mosaicker, store, lhasa.FetcherConfig{
		TemporaryDir: os.Getenv("GDAL_TEMP_DIR"), MaxPartBytes: 32 << 20,
		MaxBytes: 512 << 20, Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("创建 Earthdata LHASA 分片获取器: %w", err)
	}
	collector := collection.NewArtifactCollector(provider, downloader)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	artifact, err := collector.CollectLatest(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(artifact)
}

func gdalBinary() string {
	if value := os.Getenv("GDAL_BINARY"); value != "" {
		return value
	}
	return gdal.DefaultBinary
}

func dataDirectory() string {
	if value := os.Getenv("LHASA_DATA_DIR"); value != "" {
		return value
	}
	return "data/raw/lhasa"
}
