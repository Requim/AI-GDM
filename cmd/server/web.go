package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Requim/AI-GDM/internal/adapters/http/webui"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/dashboard"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

func mountWebConsole(server *httpserver.Server, runtime *hazardRuntime, cfg config.Config,
	dependencies *resources.Resources, logger *slog.Logger,
) error {
	handler, err := newWebConsole(runtime, cfg, dependencies, logger)
	if err != nil {
		return err
	}
	if err = server.Mount("/", handler); err != nil {
		return fmt.Errorf("挂载监控控制台: %w", err)
	}
	return nil
}

func newWebConsole(runtime *hazardRuntime, cfg config.Config,
	dependencies *resources.Resources, logger *slog.Logger,
) (http.Handler, error) {
	var risk ports.LatestRiskReader
	var weather ports.WeatherSnapshotReader
	if runtime != nil {
		risk = runtime.latestRisk
	}
	if dependencies != nil && dependencies.Database != nil {
		weather = postgres.NewWeatherRepository(dependencies.Database)
	}
	service, err := dashboard.New(risk, weather, dashboardCapabilities(cfg, dependencies), utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建控制台应用服务: %w", err)
	}
	handler, err := webui.New(service, logger)
	if err != nil {
		return nil, fmt.Errorf("创建控制台 HTTP 适配器: %w", err)
	}
	return handler, nil
}

func dashboardCapabilities(cfg config.Config, dependencies *resources.Resources) dashboard.Capabilities {
	database, cache := false, false
	if dependencies != nil {
		database, cache = dependencies.Database != nil, dependencies.Redis != nil
	}
	return dashboard.Capabilities{
		Environment: cfg.Environment, Version: version, Database: database, Cache: cache,
		Refresh: cfg.Refresh.Enabled, Map: cfg.Map.Enabled, Search: cfg.Search.Enabled,
		LLM: cfg.LLM.Enabled, LLMProvider: cfg.LLM.ProviderName, LLMModel: cfg.LLM.Model,
		WeatherPoints: cfg.Weather.Points,
	}
}
