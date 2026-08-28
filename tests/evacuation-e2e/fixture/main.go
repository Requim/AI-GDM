package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/adapters/http/mapapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/webui"
	"github.com/Requim/AI-GDM/internal/application/dashboard"
	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
)

const defaultAddress = "127.0.0.1:18081"

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
		return fmt.Errorf("创建疏散工作台浏览器回归控制台: %w", err)
	}
	scenarios := newScenarioStore()
	mapHandler, err := newMapHandler(scenarios, logger)
	if err != nil {
		return err
	}
	scenarios.useMapHandler(http.StripPrefix("/api/v1/map", middleware.RequestID(mapHandler)))
	router := http.NewServeMux()
	router.HandleFunc("/api/v1/map/places/nearby", scenarios.serveFacilities)
	router.HandleFunc("/api/v1/map/routes", scenarios.serveRoutes)
	router.HandleFunc("/api/v1/hazards/landslide/risks/latest/map", riskUnavailable)
	router.HandleFunc("/__fixture/health", health)
	router.HandleFunc("/__fixture/scenario", scenarios.configure)
	router.Handle("/", console)
	address := os.Getenv("E2E_ADDR")
	if address == "" {
		address = defaultAddress
	}
	logger.Info("疏散工作台浏览器回归 fixture 已启动", "address", address)
	return http.ListenAndServe(address, router)
}

func newMapHandler(scenarios *scenarioStore, logger *slog.Logger) (http.Handler, error) {
	facilities, err := applicationevacuation.NewService(scenarios, scenarios)
	if err != nil {
		return nil, fmt.Errorf("创建真实设施筛选服务: %w", err)
	}
	routeSafety, err := applicationevacuation.NewRouteSafetyService(scenarios, scenarios)
	if err != nil {
		return nil, fmt.Errorf("创建真实路线安全服务: %w", err)
	}
	handler, err := mapapi.NewWithTransitAndSafety(
		facilities, scenarios, scenarios, routeSafety, logger,
	)
	if err != nil {
		return nil, fmt.Errorf("创建真实地图代理: %w", err)
	}
	return handler, nil
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func riskUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeProviderUnavailable(w)
}

type overviewService struct{}

func (overviewService) Overview(context.Context) dashboard.Overview {
	return dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		Environment: "e2e", Version: "evacuation-browser",
		Sources: []dashboard.SourceStatus{{
			ID: "lhasa", Name: "滑坡风险分析", Provider: "NASA Earthdata LHASA",
			Category: "风险", State: dashboard.StateAvailable, Detail: "疏散浏览器回归 fixture",
		}},
		Summary: dashboard.Summary{Available: 1},
	}
}
