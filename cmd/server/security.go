package main

import (
	"log/slog"

	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
)

func newHTTPServer(cfg config.Config, logger *slog.Logger) (*httpserver.Server, error) {
	return httpserver.New(cfg.HTTPAddr, cfg.ShutdownTimeout, logger, httpserver.SecurityOptions{
		AdminToken: cfg.Security.AdminToken, RateLimitPerMinute: cfg.Security.RateLimitPerMinute,
		RateLimitBurst: cfg.Security.RateLimitBurst,
	})
}
