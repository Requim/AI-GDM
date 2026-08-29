package main

import (
	"context"
	"fmt"

	authorityadapter "github.com/Requim/AI-GDM/internal/adapters/authority"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

type authorityReaders struct {
	risks   ports.HazardAuthorityReader
	spatial ports.SpatialAnalysisReader
	losses  ports.LossAssessmentReader
}

func newAuthorityResolver(hazards *hazardRuntime, survival *survivalRuntime,
	dependencies *resources.Resources,
) (*authorityadapter.Resolver, error) {
	if survival == nil || survival.catalog == nil || survival.assessment == nil {
		return nil, fmt.Errorf("权威分析生产依赖不完整")
	}
	readers, err := newAuthorityReaders(hazards)
	if err != nil {
		return nil, err
	}
	var cache ports.Cache
	if dependencies != nil && dependencies.Redis != nil {
		cache = refreshCache(dependencies)
	}
	resolver, err := authorityadapter.New(readers.risks, readers.spatial, readers.losses,
		survival.catalog, survival.assessment, cache, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建权威分析 resolver: %w", err)
	}
	return resolver, nil
}

func newAuthorityReaders(hazards *hazardRuntime) (authorityReaders, error) {
	readers := authorityReaders{risks: unavailableHazardAuthorityReader{},
		spatial: unavailableSpatialAnalysisReader{}, losses: unavailableLossAssessmentReader{}}
	if hazards == nil || hazards.database == nil {
		return readers, nil
	}
	if hazards.hazardAuthority == nil || hazards.spatialAnalysis == nil {
		return authorityReaders{}, fmt.Errorf("权威分析数据库读取依赖不完整")
	}
	readers.risks = hazards.hazardAuthority
	readers.spatial = hazards.spatialAnalysis
	readers.losses = postgres.NewLossAssessmentRepository(hazards.database)
	return readers, nil
}

type unavailableHazardAuthorityReader struct{}

func (unavailableHazardAuthorityReader) ReadAuthority(context.Context, string,
	ports.HazardAuthorityLimits,
) (ports.HazardAuthorityRead, error) {
	return ports.HazardAuthorityRead{}, unavailableAuthorityError("风险权威分析")
}

type unavailableSpatialAnalysisReader struct{}

func (unavailableSpatialAnalysisReader) Get(context.Context, string) (spatialanalysis.Analysis, error) {
	return spatialanalysis.Analysis{}, unavailableAuthorityError("空间分析")
}

func (unavailableSpatialAnalysisReader) LatestBySnapshot(context.Context,
	string,
) (spatialanalysis.Analysis, error) {
	return spatialanalysis.Analysis{}, unavailableAuthorityError("空间分析")
}

type unavailableLossAssessmentReader struct{}

func (unavailableLossAssessmentReader) GetAssessment(context.Context, string) (loss.Assessment, error) {
	return loss.Assessment{}, unavailableAuthorityError("损失评估")
}

func unavailableAuthorityError(label string) error {
	return fmt.Errorf("%w: 未配置 Postgres，%s不可用", domain.ErrProviderUnavailable, label)
}
