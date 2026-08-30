package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/openmeteo"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/adapters/storage/rediscache"
	"github.com/Requim/AI-GDM/internal/application/collection"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/platform/scheduler"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	weatherFallbackFlag      = "fallback_last_success"
	maxLHASAArtifactBytes    = 512 << 20
	maxLHASAPartBytes        = 32 << 20
	lhasaDiscoveryTimeout    = 30 * time.Second
	lhasaDownloadTimeout     = 3 * time.Minute
	lhasaMaxAttempts         = 2
	lhasaProviderRequestRate = time.Second
)

func newRefreshService(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger, landslide ports.HazardRefresher,
) (runnableService, error) {
	return newRefreshServiceWithObservations(cfg, dependencies, logger, landslide, nil)
}

func newRefreshServiceWithObservations(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger, landslide ports.HazardRefresher, recorder ports.ObservationRecorder,
) (runnableService, error) {
	if !cfg.Refresh.Enabled {
		return nil, nil
	}
	if dependencies == nil || dependencies.Database == nil {
		return nil, fmt.Errorf("%w: 后台刷新缺少 Postgres 连接", config.ErrInvalidConfig)
	}
	weather, err := newWeatherRunnerWithObservations(cfg, dependencies, logger, recorder)
	if err != nil {
		return nil, err
	}
	lhasaRunner, err := newLHASARunnerWithObservations(cfg, landslide, logger, recorder)
	if err != nil {
		return nil, err
	}
	return &refreshGroup{services: []namedRefreshService{
		{name: "Open-Meteo 刷新调度器", service: weather},
		{name: "LHASA 刷新调度器", service: lhasaRunner},
	}}, nil
}

func newWeatherRunner(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (*scheduler.Runner, error) {
	return newWeatherRunnerWithObservations(cfg, dependencies, logger, nil)
}

func newWeatherRunnerWithObservations(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger, recorder ports.ObservationRecorder,
) (*scheduler.Runner, error) {
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
	task := weatherRefreshTaskWithObservations(collector, points, cfg, logger, recorder)
	return scheduler.New(cfg.Refresh.Interval, cfg.Refresh.Timeout, logger, task)
}

func newLHASARunner(cfg config.Config, refresher ports.HazardRefresher,
	logger *slog.Logger,
) (*scheduler.Runner, error) {
	return newLHASARunnerWithObservations(cfg, refresher, logger, nil)
}

func newLHASARunnerWithObservations(cfg config.Config, refresher ports.HazardRefresher,
	logger *slog.Logger, recorder ports.ObservationRecorder,
) (*scheduler.Runner, error) {
	if refresher == nil {
		return nil, fmt.Errorf("%w: LHASA 刷新器为空", config.ErrInvalidConfig)
	}
	return scheduler.New(cfg.Refresh.Interval, cfg.Refresh.Timeout, logger,
		lhasaRefreshTaskWithObservations(refresher, logger, recorder))
}

func refreshCache(dependencies *resources.Resources) ports.Cache {
	if dependencies.Redis == nil {
		return nil
	}
	return rediscache.New(dependencies.Redis, "ai-gdm")
}

func weatherRefreshTask(collector *collection.WeatherCollector, points []spatial.Point,
	cfg config.Config, logger *slog.Logger,
) scheduler.Task {
	return weatherRefreshTaskWithObservations(collector, points, cfg, logger, nil)
}

func weatherRefreshTaskWithObservations(collector *collection.WeatherCollector, points []spatial.Point,
	cfg config.Config, logger *slog.Logger, recorder ports.ObservationRecorder,
) scheduler.Task {
	return scheduler.Task{Name: "openmeteo-weather", Run: func(ctx context.Context) error {
		started := time.Now()
		snapshots, err := collector.Collect(ctx, points, cfg.Weather.PastHours, cfg.Weather.ForecastHours)
		if err != nil {
			recordRefreshObservation(recorder, componentWeather, started, ports.ObservationFailure, err)
			return err
		}
		if weatherUsesFallback(snapshots) {
			logger.WarnContext(ctx, "气象刷新使用同点集最后成功批次", "points", len(snapshots))
			recordRefreshObservation(recorder, componentWeather, started, ports.ObservationDegraded, nil)
			return nil
		}
		recordRefreshObservation(recorder, componentWeather, started, ports.ObservationSuccess, nil)
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

func lhasaRefreshTask(refresher ports.HazardRefresher, logger *slog.Logger) scheduler.Task {
	return lhasaRefreshTaskWithObservations(refresher, logger, nil)
}

func lhasaRefreshTaskWithObservations(refresher ports.HazardRefresher, logger *slog.Logger,
	recorder ports.ObservationRecorder,
) scheduler.Task {
	return scheduler.Task{Name: "nasa-lhasa", Run: func(ctx context.Context) error {
		started := time.Now()
		snapshot, _, err := refresher.Refresh(ctx)
		if err != nil {
			recordRefreshObservation(recorder, componentLHASA, started, ports.ObservationFailure, err)
			return err
		}
		if hasQualityFlag(snapshot.Source.QualityFlags, weatherFallbackFlag) {
			logger.WarnContext(ctx, "LHASA 刷新使用最后成功分析",
				"snapshot_id", snapshot.ID,
				"revision_first_seen_at", snapshot.Source.RevisionFirstSeenAt)
			recordRefreshObservation(recorder, componentLHASA, started, ports.ObservationDegraded, nil)
			return nil
		}
		recordRefreshObservation(recorder, componentLHASA, started, ports.ObservationSuccess, nil)
		return nil
	}}
}

func recordRefreshObservation(recorder ports.ObservationRecorder, componentID string,
	started time.Time, outcome ports.ObservationOutcome, err error,
) {
	if recorder == nil {
		return
	}
	errorClass := providerErrorClass(err)
	if outcome == ports.ObservationDegraded {
		errorClass = weatherFallbackFlag
	}
	recorder.RecordObservation(ports.Observation{
		ComponentID: componentID, Outcome: outcome, ObservedAt: time.Now().UTC(),
		Duration: time.Since(started), ErrorClass: errorClass,
	})
}

func hasQualityFlag(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type namedRefreshService struct {
	name    string
	service runnableService
}

type refreshGroup struct {
	services []namedRefreshService
}

func (g *refreshGroup) Run(ctx context.Context) error {
	if err := validateRefreshServices(g.services); err != nil {
		return err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan serviceResult, len(g.services))
	for _, value := range g.services {
		startService(serviceCtx, results, value.name, value.service)
	}
	first := <-results
	cancel()
	errorsFound := []error{namedServiceError(first)}
	for index := 1; index < len(g.services); index++ {
		errorsFound = append(errorsFound, namedServiceError(<-results))
	}
	return errors.Join(errorsFound...)
}

func validateRefreshServices(values []namedRefreshService) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: 刷新服务组不能为空", config.ErrInvalidConfig)
	}
	for _, value := range values {
		if value.name == "" || value.service == nil {
			return fmt.Errorf("%w: 刷新服务名称或实现为空", config.ErrInvalidConfig)
		}
	}
	return nil
}

type utcClock struct{}

func (utcClock) Now() time.Time { return time.Now().UTC() }
