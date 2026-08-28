package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/webui"
	"github.com/Requim/AI-GDM/internal/application/dashboard"
)

const defaultAddress = "127.0.0.1:18080"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	console, err := webui.New(overviewService{}, logger)
	if err != nil {
		return fmt.Errorf("创建浏览器回归控制台: %w", err)
	}
	scenarios := newScenarioStore()
	router := http.NewServeMux()
	router.Handle("/api/v1/hazards/landslide/risks/latest/map", scenarios)
	router.HandleFunc("/__fixture/health", health)
	router.HandleFunc("/__fixture/scenario", scenarios.configure)
	router.Handle("/", console)
	address := os.Getenv("E2E_ADDR")
	if address == "" {
		address = defaultAddress
	}
	logger.Info("风险地图浏览器回归 fixture 已启动", "address", address)
	return http.ListenAndServe(address, router)
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

type overviewService struct{}

func (overviewService) Overview(context.Context) dashboard.Overview {
	return dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		Environment: "e2e", Version: "risk-map-browser",
		Sources: []dashboard.SourceStatus{{
			ID: "lhasa", Name: "滑坡风险分析", Provider: "NASA Earthdata LHASA",
			Category: "风险", State: dashboard.StateAvailable, Detail: "浏览器回归 fixture",
		}},
		Summary: dashboard.Summary{Available: 1},
	}
}
