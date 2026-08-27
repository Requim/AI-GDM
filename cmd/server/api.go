package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Requim/AI-GDM/internal/adapters/http/hazardapi"
	"github.com/Requim/AI-GDM/internal/adapters/http/mapapi"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	"github.com/Requim/AI-GDM/internal/platform/resources"
)

// mountApplicationAPI 将风险和地图适配器合并到同一个 /api/v1 挂载点，避免通配路由互相遮蔽。
func mountApplicationAPI(server *httpserver.Server, runtime *hazardRuntime,
	cfg config.Config, dependencies *resources.Resources, logger *slog.Logger,
) error {
	hazardHandler, err := newHazardAPIHandler(runtime, logger)
	if err != nil {
		return err
	}
	mapHandler, err := newMapAPIHandler(cfg, dependencies, logger)
	if err != nil {
		return err
	}
	if hazardHandler == nil && mapHandler == nil {
		return nil
	}
	if err = server.Mount(hazardapi.BasePath, applicationAPI{
		hazard: hazardHandler,
		mapAPI: mapHandler,
	}); err != nil {
		return fmt.Errorf("挂载应用 HTTP 路由: %w", err)
	}
	return nil
}

// applicationAPI 按业务前缀分发已挂载的 HTTP 适配器。
type applicationAPI struct {
	hazard http.Handler
	mapAPI http.Handler
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
