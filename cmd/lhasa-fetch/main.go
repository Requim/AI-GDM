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
	provider := lhasa.New(client, lhasa.Config{BaseURL: os.Getenv("LHASA_BASE_URL")})
	store := artifactstore.New(dataDirectory(), 512<<20)
	downloader := artifactstore.NewDownloader(client, store, 512<<20)
	collector := collection.NewArtifactCollector(provider, downloader)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	artifact, err := collector.CollectLatest(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(artifact)
}

func dataDirectory() string {
	if value := os.Getenv("LHASA_DATA_DIR"); value != "" {
		return value
	}
	return "data/raw/lhasa"
}
