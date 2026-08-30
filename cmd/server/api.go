package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/hazardapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/mapapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/survivalapi"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

// mountApplicationAPI 将风险和地图适配器合并到同一个 /api/v1 挂载点，避免通配路由互相遮蔽。
func mountApplicationAPI(server *httpserver.Server, runtime *hazardRuntime,
	cfg config.Config, dependencies *resources.Resources, logger *slog.Logger,
	aiHandler http.Handler, survivalHandler http.Handler, authority mapapi.RouteAuthorityRecorder,
) error {
	return mountApplicationAPIWithObservations(server, runtime, cfg, dependencies, logger,
		aiHandler, survivalHandler, authority, nil)
}

func mountApplicationAPIWithObservations(server *httpserver.Server, runtime *hazardRuntime,
	cfg config.Config, dependencies *resources.Resources, logger *slog.Logger,
	aiHandler http.Handler, survivalHandler http.Handler, authority mapapi.RouteAuthorityRecorder,
	recorder ports.ObservationRecorder,
) error {
	hazardHandler, err := newHazardAPIHandler(runtime, logger)
	if err != nil {
		return err
	}
	mapHandler, err := newMapAPIHandlerWithAuthorityAndObservations(
		cfg, dependencies, authority, logger, recorder,
	)
	if err != nil {
		return err
	}
	lossHandler, err := newLossAPIHandler(runtime, logger)
	if err != nil {
		return err
	}
	if hazardHandler == nil && mapHandler == nil && survivalHandler == nil && lossHandler == nil && aiHandler == nil {
		return nil
	}
	if err = server.Mount(hazardapi.BasePath, applicationAPI{
		hazard:   hazardHandler,
		mapAPI:   mapHandler,
		survival: survivalHandler,
		loss:     lossHandler,
		ai:       aiHandler,
	}); err != nil {
		return fmt.Errorf("挂载应用 HTTP 路由: %w", err)
	}
	return nil
}

// applicationAPI 按业务前缀分发已挂载的 HTTP 适配器。
type applicationAPI struct {
	hazard   http.Handler
	mapAPI   http.Handler
	survival http.Handler
	loss     http.Handler
	ai       http.Handler
}

func (m applicationAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, hazardapi.BasePath)
	if path == "" {
		path = "/"
	}
	if m.mapAPI != nil && (path == mapapi.BasePath || strings.HasPrefix(path, mapapi.BasePath+"/")) {
		m.mapAPI.ServeHTTP(w, requestWithPath(r, strings.TrimPrefix(path, mapapi.BasePath)))
		return
	}
	if m.survival != nil && (path == survivalapi.BasePath || strings.HasPrefix(path, survivalapi.BasePath+"/")) {
		m.survival.ServeHTTP(w, requestWithPath(r, strings.TrimPrefix(path, survivalapi.BasePath)))
		return
	}
	if m.loss != nil && (path == lossapi.BasePath || strings.HasPrefix(path, lossapi.BasePath+"/")) {
		m.loss.ServeHTTP(w, requestWithPath(r, strings.TrimPrefix(path, lossapi.BasePath)))
		return
	}
	if m.ai != nil && (path == aiapi.BasePath || strings.HasPrefix(path, aiapi.BasePath+"/")) {
		m.ai.ServeHTTP(w, requestWithPath(r, strings.TrimPrefix(path, aiapi.BasePath)))
		return
	}
	if m.hazard != nil {
		m.hazard.ServeHTTP(w, requestWithPath(r, path))
		return
	}
	http.NotFound(w, r)
}

func requestWithPath(request *http.Request, path string) *http.Request {
	if path == "" {
		path = "/"
	}
	clone := request.Clone(request.Context())
	clone.URL.Path = path
	clone.URL.RawPath = ""
	return clone
}
