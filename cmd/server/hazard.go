package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/http/hazardapi"
	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/lhasa"
	"github.com/Requim/AI-GDM/internal/adapters/raster/gdal"
	spatialpg "github.com/Requim/AI-GDM/internal/adapters/spatial/postgis"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/collection"
	hazardapp "github.com/Requim/AI-GDM/internal/application/hazard"
	spatialapp "github.com/Requim/AI-GDM/internal/application/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

type hazardRuntime struct {
	service         hazardapp.RiskService
	landslide       *hazardapp.HazardProvider
	latestRisk      ports.LatestRiskReader
	riskDetail      ports.RiskDetailReader
	spatialAnalysis ports.SpatialAnalysisReader
	hazardAuthority ports.HazardAuthorityReader
	database        *pgxpool.Pool
}

func newHazardRuntime(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (*hazardRuntime, error) {
	if dependencies == nil || logger == nil {
		return nil, fmt.Errorf("%w: 风险预警运行时依赖为空", config.ErrInvalidConfig)
	}
	if dependencies.Database == nil {
		return nil, nil
	}
	repository := postgres.NewHazardRepository(dependencies.Database)
	exposures, err := newExposureCollector(dependencies, logger, repository)
	if err != nil {
		return nil, err
	}
	collector, err := newLHASACollector(cfg, dependencies, logger, repository)
	if err != nil {
		return nil, err
	}
	provider, err := newLandslideProvider(dependencies, logger, repository, collector, exposures)
	if err != nil {
		return nil, err
	}
	registry, err := hazardapp.NewRegistry(provider)
	if err != nil {
		return nil, fmt.Errorf("创建灾种注册表: %w", err)
	}
	service, err := hazardapp.NewService(repository, repository, repository, registry, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建风险预警用例: %w", err)
	}
	return &hazardRuntime{service: service, landslide: provider, latestRisk: repository,
		riskDetail: repository, spatialAnalysis: spatialpg.New(dependencies.Database),
		hazardAuthority: repository, database: dependencies.Database}, nil
}

func newLHASACollector(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger, repository *postgres.HazardRepository,
) (*collection.LHASACollector, error) {
	clients := newLHASAClients(dependencies, logger)
	provider, err := lhasa.New(clients.discovery, lhasa.Config{
		ServiceURL: cfg.LHASA.ServiceURL, StaleAfter: cfg.LHASA.StaleAfter,
		MaxPartBytes: maxLHASAPartBytes, MaxBytes: maxLHASAArtifactBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Earthdata LHASA 发现适配器: %w", err)
	}
	downloader, err := newLHASADownloader(cfg, logger, clients.download, provider)
	if err != nil {
		return nil, err
	}
	processor, err := gdal.New(gdal.Config{
		Binary: cfg.LHASA.GDALBinary, ArtifactRoot: cfg.LHASA.DataDir,
		TemporaryDir: cfg.LHASA.TemporaryDir,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 LHASA 栅格处理器: %w", err)
	}
	collector, err := collection.NewLHASACollector(provider, downloader, processor,
		repository, repository, utcClock{}, cfg.LHASA.StaleAfter)
	if err != nil {
		return nil, fmt.Errorf("创建 LHASA 采集用例: %w", err)
	}
	return collector, nil
}

type lhasaClients struct {
	discovery *httpclient.Client
	download  *httpclient.Client
}

func newLHASAClients(dependencies *resources.Resources, logger *slog.Logger) lhasaClients {
	limiter := rate.NewLimiter(rate.Every(lhasaProviderRequestRate), 1)
	return lhasaClients{
		discovery: httpclient.New(httpclient.Options{
			HTTPClient: &http.Client{Timeout: lhasaDiscoveryTimeout}, Cache: refreshCache(dependencies),
			Limiter: limiter, Logger: logger, MaxAttempts: lhasaMaxAttempts,
		}),
		download: httpclient.New(httpclient.Options{
			HTTPClient: &http.Client{Timeout: lhasaDownloadTimeout}, Limiter: limiter,
			Logger: logger, MaxAttempts: lhasaMaxAttempts,
		}),
	}
}

func newLHASADownloader(cfg config.Config, logger *slog.Logger, client *httpclient.Client,
	provider *lhasa.Provider,
) (*lhasa.TiledFetcher, error) {
	store := artifactstore.New(cfg.LHASA.DataDir, maxLHASAArtifactBytes)
	mosaicker, err := gdal.NewMosaicker(gdal.MosaicConfig{Binary: cfg.LHASA.GDALBinary})
	if err != nil {
		return nil, fmt.Errorf("创建 LHASA 栅格拼接器: %w", err)
	}
	downloader, err := lhasa.NewTiledFetcher(client, provider, mosaicker, store,
		lhasa.FetcherConfig{
			TemporaryDir: cfg.LHASA.TemporaryDir,
			MaxPartBytes: maxLHASAPartBytes, MaxBytes: maxLHASAArtifactBytes, Logger: logger,
		})
	if err != nil {
		return nil, fmt.Errorf("创建 Earthdata LHASA 分片获取器: %w", err)
	}
	return downloader, nil
}

func newLandslideProvider(dependencies *resources.Resources, logger *slog.Logger,
	repository *postgres.HazardRepository, collector *collection.LHASACollector,
	exposures exposureCollector,
) (*hazardapp.HazardProvider, error) {
	engine, err := risk.NewEngine(risk.ModelCapability{
		HazardType: hazard.TypeLandslide, ModelName: gdal.ModelName, Dataset: lhasa.DatasetName,
	})
	if err != nil {
		return nil, fmt.Errorf("创建滑坡风险引擎: %w", err)
	}
	spatialExecutor := spatialpg.New(dependencies.Database)
	spatialService, err := spatialapp.New(spatialExecutor, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建空间分析用例: %w", err)
	}
	refresher := &spatialRefresh{
		upstream: hazardapp.RefreshFunc(collector.Collect), analyzer: spatialService,
		analyses: spatialExecutor, zones: repository, evaluator: engine, assessments: repository, authority: repository,
		exposureState: repository, exposures: exposures, clock: utcClock{}, logger: logger,
	}
	provider, err := hazardapp.NewHazardProvider(hazard.TypeLandslide, refresher, engine)
	if err != nil {
		return nil, fmt.Errorf("创建滑坡灾种能力: %w", err)
	}
	return provider, nil
}

type spatialAnalyzer interface {
	Analyze(context.Context, string) (spatialanalysis.Analysis, error)
}

type spatialRefresh struct {
	upstream      ports.HazardRefresher
	analyzer      spatialAnalyzer
	analyses      ports.SpatialAnalysisReader
	zones         ports.RiskZoneReader
	evaluator     ports.RiskEvaluator
	assessments   ports.RiskAssessmentWriter
	authority     ports.RiskAuthorityReuser
	exposureState exposureProjectionChecker
	exposures     exposureCollector
	clock         ports.Clock
	logger        *slog.Logger
}

func (r *spatialRefresh) Refresh(ctx context.Context) (
	hazard.Snapshot, []hazard.RiskZone, error,
) {
	snapshot, zones, err := r.upstream.Refresh(ctx)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	reused, err := r.authority.ReuseRiskAuthority(ctx, snapshot, zones)
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("复用风险快照 %s 权威评估: %w", snapshot.ID, err)
	}
	if reused {
		calculated, readErr := r.zones.ZonesBySnapshot(ctx, snapshot.ID)
		if readErr != nil {
			return hazard.Snapshot{}, nil, fmt.Errorf("读取风险快照 %s 已固化空间结果: %w", snapshot.ID, readErr)
		}
		analysis, analysisErr := r.latestSpatialAnalysis(ctx, snapshot.ID)
		if analysisErr != nil {
			return hazard.Snapshot{}, nil, analysisErr
		}
		if err = r.ensureExposure(ctx, snapshot.ID, analysis.ID); err != nil {
			return hazard.Snapshot{}, nil, err
		}
		return snapshot, calculated, nil
	}
	analysis, err := r.analyzer.Analyze(ctx, snapshot.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return hazard.Snapshot{}, nil, err
		}
		r.logger.ErrorContext(ctx, "风险刷新后的空间分析不可用",
			"snapshot_id", snapshot.ID, "error", err)
		return spatialUnavailable(snapshot, zones), zones, nil
	}
	calculated, err := r.zones.ZonesBySnapshot(ctx, snapshot.ID)
	if err != nil {
		r.logger.ErrorContext(ctx, "读取已计算风险区面积失败",
			"snapshot_id", snapshot.ID, "error", err)
		return spatialUnavailable(snapshot, zones), zones, nil
	}
	if err = r.persistAssessment(ctx, snapshot, calculated); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	if err = r.ensureExposure(ctx, snapshot.ID, analysis.ID); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	return snapshot, calculated, nil
}

func (r *spatialRefresh) latestSpatialAnalysis(ctx context.Context,
	snapshotID string,
) (spatialanalysis.Analysis, error) {
	if r.analyses == nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 空间分析读取依赖为空", domain.ErrInvalidInput)
	}
	value, err := r.analyses.LatestBySnapshot(ctx, snapshotID)
	if err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("读取快照 %s 最新空间分析: %w", snapshotID, err)
	}
	if value.ID == "" || value.SnapshotID != snapshotID {
		return spatialanalysis.Analysis{}, fmt.Errorf("%w: 最新空间分析身份无效", domain.ErrInsufficientData)
	}
	return value, nil
}

func (r *spatialRefresh) ensureExposure(ctx context.Context, snapshotID, analysisID string) error {
	if r.exposureState == nil || r.exposures == nil || r.clock == nil || r.logger == nil {
		return fmt.Errorf("%w: 真实暴露投影刷新依赖为空", domain.ErrInvalidInput)
	}
	now := r.clock.Now()
	exists, err := r.exposureState.HasCurrentExposureProjection(ctx, snapshotID, analysisID, now)
	if err != nil {
		return r.handleExposureFailure(ctx, snapshotID, "检查当前暴露投影", err)
	}
	if exists {
		return nil
	}
	if _, err = r.exposures.Collect(ctx, snapshotID, analysisID); err != nil {
		return r.handleExposureFailure(ctx, snapshotID, "采集真实暴露投影", err)
	}
	return nil
}

func (r *spatialRefresh) handleExposureFailure(ctx context.Context, snapshotID, operation string,
	err error,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.logger.ErrorContext(ctx, operation+"失败，损失评估保持不可用",
		"snapshot_id", snapshotID, "error", err)
	return nil
}

func (r *spatialRefresh) persistAssessment(ctx context.Context, snapshot hazard.Snapshot,
	zones []hazard.RiskZone,
) error {
	if r.evaluator == nil || r.assessments == nil || r.clock == nil {
		return fmt.Errorf("%w: 风险评估持久化依赖为空", domain.ErrInvalidInput)
	}
	evaluatedAt := r.clock.Now()
	assessment, err := r.evaluator.Evaluate(risk.Input{
		Snapshot: snapshot, Zones: zones, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		return fmt.Errorf("生成风险快照 %s 固化评估: %w", snapshot.ID, err)
	}
	if err = r.assessments.SaveRiskAssessment(ctx, snapshot, assessment); err != nil {
		return fmt.Errorf("固化风险快照 %s 评估: %w", snapshot.ID, err)
	}
	return nil
}

func spatialUnavailable(snapshot hazard.Snapshot, _ []hazard.RiskZone) hazard.Snapshot {
	snapshot.Limitations = append([]string(nil), snapshot.Limitations...)
	for _, value := range snapshot.Limitations {
		if value == "空间分析暂时不可用，风险等级仍来自基础模型结果" {
			return snapshot
		}
	}
	snapshot.Limitations = append(snapshot.Limitations,
		"空间分析暂时不可用，风险等级仍来自基础模型结果")
	return snapshot
}

func newHazardAPIHandler(runtime *hazardRuntime, logger *slog.Logger) (http.Handler, error) {
	if runtime == nil {
		logger.Warn("未配置 Postgres，风险预警 API 未挂载")
		return nil, nil
	}
	handler, err := hazardapi.New(runtime.service, logger)
	if err != nil {
		return nil, fmt.Errorf("创建风险预警 HTTP 适配器: %w", err)
	}
	return handler, nil
}

func mountHazardAPI(server *httpserver.Server, runtime *hazardRuntime,
	logger *slog.Logger,
) error {
	if runtime == nil {
		logger.Warn("未配置 Postgres，风险预警 API 未挂载")
		return nil
	}
	handler, err := hazardapi.New(runtime.service, logger)
	if err != nil {
		return fmt.Errorf("创建风险预警 HTTP 适配器: %w", err)
	}
	if err = server.Mount(hazardapi.BasePath, handler); err != nil {
		return fmt.Errorf("挂载风险预警 HTTP 路由: %w", err)
	}
	return nil
}
