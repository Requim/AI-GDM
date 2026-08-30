package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/webui"
	"github.com/Requim/AI-GDM/internal/application/dashboard"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
)

const defaultAddress = "127.0.0.1:18083"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	address := os.Getenv("E2E_ADDR")
	if address == "" {
		address = defaultAddress
	}
	token := os.Getenv("APP_ADMIN_TOKEN")
	server, err := httpserver.New(address, 5*time.Second, logger, httpserver.SecurityOptions{
		AdminToken: token, RateLimitPerMinute: 60_000, RateLimitBurst: 10_000,
	})
	if err != nil {
		return err
	}
	console, err := webui.New(overviewService{}, logger)
	if err != nil {
		return err
	}
	if err = server.Mount("/api/v1", &probeHandler{}); err != nil {
		return err
	}
	if err = server.Mount("/", console); err != nil {
		return err
	}
	logger.Info("P9.1 浏览器安全 fixture 已启动", "address", address)
	return server.Run(context.Background())
}

type probeHandler struct{ calls atomic.Uint64 }

func (handler *probeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/security/probe") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if writeBrokenUnauthorized(w, r.URL.Query().Get("response")) {
		return
	}
	if r.Method == http.MethodPost {
		count := handler.calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "count": count})
		return
	}
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "count": handler.calls.Load()})
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func writeBrokenUnauthorized(w http.ResponseWriter, mode string) bool {
	switch mode {
	case "oversize_401":
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusUnauthorized)
		return true
	case "interrupted_401":
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusUnauthorized)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(`{"error":`))
		return true
	default:
		return false
	}
}

type overviewService struct{}

func (overviewService) Overview(context.Context) dashboard.Overview {
	return dashboard.Overview{
		GeneratedAt: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
		Environment: "e2e", Version: "p9-security",
		Sources: []dashboard.SourceStatus{{
			ID: "security", Name: "安全边界", Provider: "AI-GDM",
			Category: "安全", State: dashboard.StateAvailable, Detail: "浏览器安全回归",
		}},
		Summary: dashboard.Summary{Available: 1},
	}
}
