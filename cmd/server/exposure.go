package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/provider/geoboundaries"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/overpass"
	"github.com/Requim/AI-GDM/internal/adapters/provider/worldpop"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	geoBoundariesTimeout  = 30 * time.Second
	worldPopTimeout       = 30 * time.Second
	overpassTimeout       = 40 * time.Second
	geoBoundariesInterval = time.Second
	worldPopInterval      = 500 * time.Millisecond
	overpassInterval      = 2 * time.Second
	geoBoundariesAttempts = 2
	worldPopAttempts      = 3
	overpassAttempts      = 2
)

type exposureProjectionChecker interface {
	HasCurrentExposureProjection(context.Context, string, string, time.Time) (bool, error)
}

type exposureCollector interface {
	Collect(context.Context, string, string) (exposurecollection.ExposureProjection, error)
}

type exposureProviderSet struct {
	boundary       exposurecollection.AdministrativeBoundaryProvider
	population     exposurecollection.PopulationProvider
	infrastructure exposurecollection.InfrastructureProvider
}

type exposureCollectorRuntime struct {
	geometries    exposurecollection.GeometryInputReader
	administrator exposurecollection.AdministrativeProjector
	projector     exposurecollection.InfrastructureProjector
	writer        exposurecollection.ProjectionWriter
	clock         ports.Clock
}

func newExposureCollector(dependencies *resources.Resources, logger *slog.Logger,
	repository *postgres.HazardRepository,
) (*exposurecollection.Collector, error) {
	if dependencies == nil || dependencies.Database == nil || logger == nil || repository == nil {
		return nil, fmt.Errorf("%w: 真实暴露采集组合根依赖为空", domain.ErrInvalidInput)
	}
	providers, err := newExposureProviders(newExposureHTTPClients(logger))
	if err != nil {
		return nil, err
	}
	return buildExposureCollector(exposureCollectorRuntime{geometries: repository,
		administrator: repository, projector: repository, writer: repository, clock: utcClock{}}, providers)
}

func newExposureProviders(clients exposureHTTPClients) (exposureProviderSet, error) {
	boundary, err := geoboundaries.New(geoboundaries.Options{Client: clients.boundary})
	if err != nil {
		return exposureProviderSet{}, fmt.Errorf("创建 geoBoundaries provider: %w", err)
	}
	population, err := worldpop.New(worldpop.Options{Client: clients.population})
	if err != nil {
		return exposureProviderSet{}, fmt.Errorf("创建 WorldPop provider: %w", err)
	}
	infrastructure, err := overpass.New(overpass.Options{Client: clients.infrastructure})
	if err != nil {
		return exposureProviderSet{}, fmt.Errorf("创建 Overpass provider: %w", err)
	}
	return exposureProviderSet{boundary: boundary, population: population, infrastructure: infrastructure}, nil
}

func buildExposureCollector(runtime exposureCollectorRuntime,
	providers exposureProviderSet,
) (*exposurecollection.Collector, error) {
	collector, err := exposurecollection.New(runtime.geometries, providers.boundary,
		providers.population, providers.infrastructure, runtime.administrator, runtime.projector,
		runtime.writer, runtime.clock)
	if err != nil {
		return nil, fmt.Errorf("创建真实暴露采集用例: %w", err)
	}
	return collector, nil
}

type exposureHTTPClients struct {
	boundary       *httpclient.Client
	population     *httpclient.Client
	infrastructure *httpclient.Client
}

type exposureHTTPPolicy struct {
	timeout     time.Duration
	interval    time.Duration
	maxAttempts int
}

type exposureHTTPPolicies struct {
	boundary       exposureHTTPPolicy
	population     exposureHTTPPolicy
	infrastructure exposureHTTPPolicy
}

type exposureHTTPBases struct {
	boundary       *http.Client
	population     *http.Client
	infrastructure *http.Client
}

func newExposureHTTPClients(logger *slog.Logger) exposureHTTPClients {
	return buildExposureHTTPClients(logger, defaultExposureHTTPPolicies(), exposureHTTPBases{})
}

func buildExposureHTTPClients(logger *slog.Logger, policies exposureHTTPPolicies,
	bases exposureHTTPBases,
) exposureHTTPClients {
	return exposureHTTPClients{
		boundary:       buildExposureHTTPClient(logger, policies.boundary, bases.boundary),
		population:     buildExposureHTTPClient(logger, policies.population, bases.population),
		infrastructure: buildExposureHTTPClient(logger, policies.infrastructure, bases.infrastructure),
	}
}

func defaultExposureHTTPPolicies() exposureHTTPPolicies {
	return exposureHTTPPolicies{
		boundary: exposureHTTPPolicy{timeout: geoBoundariesTimeout,
			interval: geoBoundariesInterval, maxAttempts: geoBoundariesAttempts},
		population: exposureHTTPPolicy{timeout: worldPopTimeout,
			interval: worldPopInterval, maxAttempts: worldPopAttempts},
		infrastructure: exposureHTTPPolicy{timeout: overpassTimeout,
			interval: overpassInterval, maxAttempts: overpassAttempts},
	}
}

func newExposureHTTPClient(logger *slog.Logger, policy exposureHTTPPolicy) *httpclient.Client {
	return buildExposureHTTPClient(logger, policy, nil)
}

func buildExposureHTTPClient(logger *slog.Logger, policy exposureHTTPPolicy,
	base *http.Client,
) *httpclient.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.Timeout = policy.timeout
	return httpclient.New(httpclient.Options{
		HTTPClient: &client,
		Limiter:    rate.NewLimiter(rate.Every(policy.interval), 1), Logger: logger,
		MaxAttempts: policy.maxAttempts,
	})
}
