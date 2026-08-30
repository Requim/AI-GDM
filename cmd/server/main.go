package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	hazardapp "github.com/Requim/AI-GDM/internal/application/hazard"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/logging"
	"github.com/Requim/AI-GDM/internal/platform/resources"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel)
	if err != nil {
		return err
	}
	observations, err := newObservationRegistry()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("AI-GDM 启动", "version", version, "environment", cfg.Environment, "addr", cfg.HTTPAddr)
	dependencies, err := resources.Open(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("初始化外部资源: %w", err)
	}
	defer dependencies.Close()
	hazards, err := newHazardRuntime(cfg, dependencies, logger)
	if err != nil {
		return fmt.Errorf("初始化风险预警: %w", err)
	}
	var landslideRefresher *hazardapp.HazardProvider
	if hazards != nil {
		landslideRefresher = hazards.landslide
	}
	refresh, err := newRefreshServiceWithObservations(
		cfg, dependencies, logger, landslideRefresher, observations,
	)
	if err != nil {
		return fmt.Errorf("初始化数据刷新: %w", err)
	}
	server, err := newHTTPServer(cfg, logger)
	if err != nil {
		return fmt.Errorf("初始化 HTTP 安全边界: %w", err)
	}
	if err = mountMetrics(server, observations); err != nil {
		return err
	}
	survival, err := newSurvivalRuntime(logger)
	if err != nil {
		return fmt.Errorf("初始化生还历史回放: %w", err)
	}
	authorityResolver, err := newAuthorityResolver(hazards, survival, dependencies)
	if err != nil {
		return fmt.Errorf("初始化权威分析解析器: %w", err)
	}
	aiHandler, err := newAIHandlerWithObservations(
		cfg, dependencies, authorityResolver, logger, observations,
	)
	if err != nil {
		return fmt.Errorf("初始化智能研判: %w", err)
	}
	if err = mountApplicationAPIWithObservations(server, hazards, cfg, dependencies, logger,
		aiHandler, survival.handler, authorityResolver, observations); err != nil {
		return err
	}
	if err = mountWebConsoleWithObservations(
		server, hazards, cfg, dependencies, logger, observations,
	); err != nil {
		return err
	}
	if err = runServices(ctx, server, refresh); err != nil {
		return err
	}
	logger.Info("AI-GDM 已停止", slog.String("version", version))
	return nil
}
