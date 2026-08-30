package main

import (
	"fmt"
	"net/http"

	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	platformobservability "github.com/Requim/AI-GDM/internal/platform/observability"
)

var observedComponents = []string{
	componentLHASA,
	componentWeather,
	componentAMap,
	componentBocha,
	componentLLM,
}

func newObservationRegistry() (*platformobservability.Registry, error) {
	registry, err := platformobservability.New(observedComponents)
	if err != nil {
		return nil, fmt.Errorf("创建运行观测注册表: %w", err)
	}
	return registry, nil
}

func mountMetrics(server *httpserver.Server, registry *platformobservability.Registry) error {
	if server == nil || registry == nil {
		return fmt.Errorf("挂载运行指标缺少 HTTP 服务或观测注册表")
	}
	handler := metricsMethodHandler(registry.MetricsHandler())
	if err := server.HandleExact("/metrics", handler); err != nil {
		return fmt.Errorf("挂载运行指标: %w", err)
	}
	return nil
}

func metricsMethodHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}
