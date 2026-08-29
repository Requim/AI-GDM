package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/webui"
	"github.com/Requim/AI-GDM/internal/application/dashboard"
)

const defaultAddress = "127.0.0.1:18082"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	identity, err := loadFixtureIdentity()
	if err != nil {
		return err
	}
	console, err := webui.New(overviewService{}, logger)
	if err != nil {
		return fmt.Errorf("创建评估界面浏览器回归控制台: %w", err)
	}
	scenarios, err := newScenarioStore()
	if err != nil {
		return fmt.Errorf("创建评估浏览器回归场景: %w", err)
	}
	lossHandler, lossStore, err := newLossHandler(scenarios, logger)
	if err != nil {
		return err
	}
	scenarios.useLossHandler(lossHandler, lossStore)
	aiHandler, err := newAIHandler(scenarios, logger)
	if err != nil {
		return err
	}
	scenarios.useAIHandler(aiHandler)
	router := http.NewServeMux()
	mountFixtureRoutes(router, scenarios, identity)
	router.Handle("/", console)
	address := os.Getenv("E2E_ADDR")
	if address == "" {
		address = defaultAddress
	}
	listener, err := listenFixture(address, os.Getenv("E2E_RUNTIME_FILE"))
	if err != nil {
		return err
	}
	logger.Info("评估界面浏览器回归 fixture 已启动",
		"address", listener.Addr().String(), "treeSha", identity.treeSHA)
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	return server.Serve(listener)
}

func mountFixtureRoutes(router *http.ServeMux, scenarios *scenarioStore, identity fixtureIdentity) {
	router.HandleFunc("/__fixture/health", health(identity))
	router.HandleFunc("/__fixture/scenario", scenarios.configure)
	router.HandleFunc("/__fixture/state", scenarios.serveState)
	router.Handle("/api/v1/loss/", http.StripPrefix("/api/v1/loss", http.HandlerFunc(scenarios.serveLoss)))
	router.Handle("/api/v1/survival/",
		http.StripPrefix("/api/v1/survival", http.HandlerFunc(scenarios.serveSurvival)))
	router.Handle("/api/v1/ai/", http.StripPrefix("/api/v1/ai", http.HandlerFunc(scenarios.serveAIReport)))
	router.HandleFunc("/api/v1/hazards/landslide/risks/latest/map", providerUnavailable)
}

func providerUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "实时风险图层未在评估 fixture 中启用")
}

type overviewService struct{}

func (overviewService) Overview(context.Context) dashboard.Overview {
	return dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		Environment: "e2e", Version: "assessment-browser",
		Sources: []dashboard.SourceStatus{{
			ID: "assessment", Name: "评估浏览器回归", Provider: "AI-GDM fixture",
			Category: "评估", State: dashboard.StateAvailable, Detail: "固定真实 HTTP 契约",
		}},
		Summary: dashboard.Summary{Available: 1},
	}
}
