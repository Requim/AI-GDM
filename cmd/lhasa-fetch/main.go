package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/nccs"
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
	provider, err := nccs.New(client, os.Getenv("LHASA_PORTAL_URL"), 12*time.Hour)
	if err != nil {
		return fmt.Errorf("创建 NCCS LHASA 发现适配器: %w", err)
	}
	store := artifactstore.New(dataDirectory(), 512<<20)
	downloader, err := nccs.NewFetcher(client, provider, store, nccs.FetchConfig{
		Directory: filepath.Join(dataDirectory(), ".nccs-downloads"), Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("创建 NCCS LHASA 完整文件获取器: %w", err)
	}
	collector := collection.NewArtifactCollector(provider, downloader)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
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
