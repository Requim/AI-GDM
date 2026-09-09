package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Requim/AI-GDM/internal/adapters/baseline/lossreference"
	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// newLossAPIHandler 创建损失计算、查询和来源审计接口。
func newLossAPIHandler(runtime *hazardRuntime, logger *slog.Logger) (http.Handler, error) {
	if runtime == nil || runtime.database == nil {
		logger.Warn("未配置 Postgres，损失 API 未挂载")
		return nil, nil
	}
	repository := postgres.NewHazardRepository(runtime.database)
	baseline := lossreference.NewFallback(postgres.NewLossBaselineRepository(runtime.database))
	assessmentStore := postgres.NewLossAssessmentRepository(runtime.database)
	service, err := applicationloss.NewService(repository, baseline, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建损失评估用例: %w", err)
	}
	var projector lossapi.RegionalExposureProjector
	if runtime.regionalExposures != nil && runtime.spatialAnalysis != nil {
		boundaries, _ := runtime.regionCatalog.(exposurecollection.RegionalBoundaryProvider)
		projector = regionalExposureProjector{collector: runtime.regionalExposures, analyses: runtime.spatialAnalysis,
			boundaries: boundaries, impacts: repository}
	}
	handler, err := lossapi.NewWithRegionCatalogAndProjector(service, assessmentStore, assessmentStore,
		"/api/v1/loss", logger, runtime.regionCatalog, projector)
	if err != nil {
		return nil, fmt.Errorf("创建损失 HTTP 适配器: %w", err)
	}
	return handler, nil
}

type regionalExposureProjector struct {
	collector interface {
		CollectRegion(context.Context, string, string, string) (exposurecollection.ExposureProjection, error)
	}
	analyses ports.SpatialAnalysisReader
	boundaries exposurecollection.RegionalBoundaryProvider
	impacts exposurecollection.RegionalImpactReader
}

func (p regionalExposureProjector) PreviewRegion(ctx context.Context, snapshotID, regionCode string) (
	exposurecollection.RegionalImpact, error,
) {
	if p.boundaries == nil || p.impacts == nil {
		return exposurecollection.RegionalImpact{}, fmt.Errorf("区域影响范围读取未配置")
	}
	boundary, err := p.boundaries.BoundaryForRegion(ctx, regionCode)
	if err != nil {
		return exposurecollection.RegionalImpact{}, fmt.Errorf("读取所选行政区边界: %w", err)
	}
	analysis, err := p.analyses.LatestBySnapshot(ctx, snapshotID)
	if err != nil {
		return exposurecollection.RegionalImpact{}, fmt.Errorf("读取区域风险分析: %w", err)
	}
	return p.impacts.ReadRegionalImpact(ctx, snapshotID, analysis.ID, boundary)
}

func (p regionalExposureProjector) CollectRegion(ctx context.Context, snapshotID, regionCode string) (
	exposurecollection.ExposureProjection, error,
) {
	analysis, err := p.analyses.LatestBySnapshot(ctx, snapshotID)
	if err != nil {
		return exposurecollection.ExposureProjection{}, fmt.Errorf("读取快照 %s 最新空间分析: %w", snapshotID, err)
	}
	return p.collector.CollectRegion(ctx, snapshotID, analysis.ID, regionCode)
}
