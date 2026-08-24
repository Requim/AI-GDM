package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/openmeteo"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/collection"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/platform/scheduler"
)

const weatherFallbackFlag = "fallback_last_success"

func newRefreshRunner(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (*scheduler.Runner, error) {
	if !cfg.Refresh.Enabled {
		return nil, nil
	}
	if dependencies == nil || dependencies.Database == nil {
		return nil, fmt.Errorf("%w: 后台刷新缺少 Postgres 连接", config.ErrInvalidConfig)
	}
	client := httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Limiter:    rate.NewLimiter(rate.Every(200*time.Millisecond), 2),
		Logger:     logger,
	})
	provider := openmeteo.New(client, openmeteo.Config{
		BaseURL: cfg.Weather.BaseURL, APIKey: cfg.Weather.APIKey,
		MaxPointsPerRequest: cfg.Weather.MaxPointsPerRequest,
	})
	repository := postgres.NewWeatherRepository(dependencies.Database)
	collector, err := collection.NewWeatherCollector(
		provider, repository, repository, utcClock{}, cfg.Weather.FallbackMaxAge,
	)
	if err != nil {
		return nil, fmt.Errorf("创建气象采集用例: %w", err)
	}
	points := append([]spatial.Point(nil), cfg.Weather.Points...)
	task := weatherRefreshTask(collector, points, cfg, logger)
	return scheduler.New(cfg.Refresh.Interval, cfg.Refresh.Timeout, logger, task)
}

func weatherRefreshTask(collector *collection.WeatherCollector, points []spatial.Point,
	cfg config.Config, logger *slog.Logger,
) scheduler.Task {
	return scheduler.Task{Name: "openmeteo-weather", Run: func(ctx context.Context) error {
		snapshots, err := collector.Collect(ctx, points, cfg.Weather.PastHours, cfg.Weather.ForecastHours)
		if err != nil {
			return err
		}
		if weatherUsesFallback(snapshots) {
			logger.WarnContext(ctx, "气象刷新使用同点集最后成功批次", "points", len(snapshots))
		}
		return nil
	}}
}

func weatherUsesFallback(snapshots []hazard.WeatherSnapshot) bool {
	for _, snapshot := range snapshots {
		for _, flag := range snapshot.Source.QualityFlags {
			if flag == weatherFallbackFlag {
				return true
			}
		}
	}
	return false
}

type utcClock struct{}

func (utcClock) Now() time.Time { return time.Now().UTC() }
