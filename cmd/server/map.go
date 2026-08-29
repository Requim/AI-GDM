package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/http/mapapi"
	"github.com/Requim/AI-GDM/internal/adapters/provider/amap"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	amapMaxAttempts = 2
	amapRequestRate = 200 * time.Millisecond
	amapBurstSize   = 2
)

// newMapProvider 在组合根创建高德适配器，业务层只接收地图端口。
func newMapProvider(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (*amap.Provider, error) {
	if !cfg.Map.Enabled {
		return nil, nil
	}
	if err := cfg.Map.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, fmt.Errorf("%w: 高德地图日志器为空", config.ErrInvalidConfig)
	}
	var cacheProvider ports.Cache
	if dependencies != nil && dependencies.Redis != nil {
		cacheProvider = refreshCache(dependencies)
	}
	client := httpclient.New(httpclient.Options{
		HTTPClient:  &http.Client{Timeout: cfg.Map.Timeout},
		Cache:       cacheProvider,
		Limiter:     rate.NewLimiter(rate.Every(amapRequestRate), amapBurstSize),
		Logger:      logger,
		MaxAttempts: amapMaxAttempts,
	})
	provider, err := amap.New(client, amap.Config{
		BaseURL: cfg.Map.BaseURL, APIKey: cfg.Map.APIKey,
		SecurityCode: cfg.Map.SecurityCode, Timeout: cfg.Map.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("创建高德地图适配器: %w", err)
	}
	return provider, nil
}

func newMapAPIHandler(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (http.Handler, error) {
	return newMapAPIHandlerWithAuthority(cfg, dependencies, nil, logger)
}

func newMapAPIHandlerWithAuthority(cfg config.Config, dependencies *resources.Resources,
	authority mapapi.RouteAuthorityRecorder, logger *slog.Logger,
) (http.Handler, error) {
	provider, err := newMapProvider(cfg, dependencies, logger)
	if err != nil {
		return nil, fmt.Errorf("初始化高德地图: %w", err)
	}
	if provider == nil {
		return nil, nil
	}
	var risks ports.LatestRiskReader = unavailableRiskReader{}
	if dependencies != nil && dependencies.Database != nil {
		risks = postgres.NewHazardRepository(dependencies.Database)
	}
	facilities, err := applicationevacuation.NewService(provider, risks)
	if err != nil {
		return nil, fmt.Errorf("创建避险设施搜索用例: %w", err)
	}
	routeSafety, err := applicationevacuation.NewRouteSafetyService(provider, risks)
	if err != nil {
		return nil, fmt.Errorf("创建路线安全评估用例: %w", err)
	}
	handler, err := mapapi.NewWithTransitSafetyAndAuthority(
		facilities, provider, provider, routeSafety, authority, logger,
	)
	if err != nil {
		return nil, fmt.Errorf("创建地图 HTTP 适配器: %w", err)
	}
	return handler, nil
}

// unavailableRiskReader 明确阻断没有完整风险区时的未过滤设施响应。
type unavailableRiskReader struct{}

func (unavailableRiskReader) LatestRisk(context.Context, hazard.Type) (hazard.Snapshot, []hazard.RiskZone, error) {
	return hazard.Snapshot{}, nil, fmt.Errorf("%w: PostGIS 风险分析未连接", domain.ErrInsufficientData)
}

// mountMapAPI 挂载不暴露高德密钥的服务端地图代理。
func mountMapAPI(server *httpserver.Server, cfg config.Config,
	dependencies *resources.Resources, logger *slog.Logger,
) error {
	handler, err := newMapAPIHandler(cfg, dependencies, logger)
	if err != nil {
		return err
	}
	if handler == nil {
		logger.Info("高德地图代理未启用")
		return nil
	}
	if err = server.Mount("/api/v1"+mapapi.BasePath, handler); err != nil {
		return fmt.Errorf("挂载地图 HTTP 路由: %w", err)
	}
	return nil
}
