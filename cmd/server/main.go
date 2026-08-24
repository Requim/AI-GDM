package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("AI-GDM 启动", "version", version, "environment", cfg.Environment, "addr", cfg.HTTPAddr)
	dependencies, err := resources.Open(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("初始化外部资源: %w", err)
	}
	defer dependencies.Close()
	refresh, err := newRefreshRunner(cfg, dependencies, logger)
	if err != nil {
		return fmt.Errorf("初始化数据刷新: %w", err)
	}
	server := httpserver.New(cfg.HTTPAddr, cfg.ShutdownTimeout, logger)
	if err = runServices(ctx, server, refresh); err != nil {
		return err
	}
	logger.Info("AI-GDM 已停止", slog.String("version", version))
	return nil
}
