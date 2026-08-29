package authority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestSurvivalAuthorityBuildsBoundedIndexOnce(t *testing.T) {
	fixture := newResolverFixture(t)
	catalog := &countingCatalog{base: fixture.catalog}
	survival := &countingSurvival{base: fixture.survival}
	resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
		catalog, survival, fixture.cache, fixedClock{fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	reference := report.AnalysisReference{Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID}
	for index := 0; index < 2; index++ {
		if _, err = resolver.Resolve(context.Background(), reference); err != nil {
			t.Fatal(err)
		}
	}
	if catalog.listCalls != 1 || survival.calls == 0 || catalog.getCalls != survival.calls {
		t.Fatalf("历史回放索引调用次数错误: list=%d get=%d assess=%d",
			catalog.listCalls, catalog.getCalls, survival.calls)
	}
}

func TestSurvivalAuthorityProjectsDeterministicFactorsAndLimitations(t *testing.T) {
	fixture := newResolverFixture(t)
	replay, err := fixture.survival.AssessCase(context.Background(), "case-oso-2014")
	if err != nil {
		t.Fatal(err)
	}
	value, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
		Kind: report.AuthoritySurvivalAssessment, ID: replay.AssessmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var analysis report.SurvivalAuthorityAnalysis
	if err = json.Unmarshal(value.AnalysisJSON, &analysis); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(analysis.Factors, replay.Assessment.Factors) ||
		!slices.Equal(analysis.Limitations, replay.Assessment.Limitations) {
		t.Fatalf("Authority 未绑定确定性因素或限制: %+v", analysis)
	}
}

func TestSurvivalAuthorityPreservesOperationalErrors(t *testing.T) {
	tests := map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
		"provider": domain.ErrProviderUnavailable,
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			service := &failingSurvival{err: target}
			resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
				fixture.catalog, service, fixture.cache, fixedClock{fixture.now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Resolve(context.Background(), report.AnalysisReference{
				Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID,
			})
			if !errors.Is(err, target) || errors.Is(err, report.ErrInvalidAuthority) {
				t.Fatalf("运行时错误被错误分类: error=%v target=%v", err, target)
			}
		})
	}
}

func TestSurvivalAuthorityRejectsOversizedOrCrossBoundCatalog(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		fixture := newResolverFixture(t)
		resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
			overflowCatalog{}, fixture.survival, fixture.cache, fixedClock{fixture.now})
		if err != nil {
			t.Fatal(err)
		}
		_, err = resolver.Resolve(context.Background(), report.AnalysisReference{
			Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID,
		})
		assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
		assertErrorIs(t, err, domain.ErrInsufficientData)
	})
	t.Run("cross binding", func(t *testing.T) {
		fixture := newResolverFixture(t)
		catalog := &driftingCatalog{base: fixture.catalog}
		resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
			catalog, fixture.survival, fixture.cache, fixedClock{fixture.now})
		if err != nil {
			t.Fatal(err)
		}
		_, err = resolver.Resolve(context.Background(), report.AnalysisReference{
			Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID,
		})
		assertErrorIs(t, err, report.ErrInvalidAuthority)
	})
}

func TestSurvivalAuthorityValidatesReplayTimeBinding(t *testing.T) {
	fixture := newResolverFixture(t)
	detail, err := fixture.catalog.GetCase(context.Background(), "case-oso-2014")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		calculated time.Time
		wantError  bool
	}{
		{name: "scenario boundary", calculated: detail.Scenario.AsOf},
		{name: "current tolerance boundary", calculated: fixture.now.Add(survivalAssessmentFutureTolerance)},
		{name: "before scenario", calculated: detail.Scenario.AsOf.Add(-time.Nanosecond), wantError: true},
		{name: "after current tolerance", calculated: fixture.now.Add(survivalAssessmentFutureTolerance + time.Nanosecond), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &mutatingSurvivalService{base: fixture.survival, mutate: func(value *applicationsurvival.ReplayAssessment) {
				if value.CaseID == detail.Event.ID {
					value.Assessment.CalculatedAt = test.calculated
				}
			}}
			resolver, newErr := New(fixture.risk, fixture.spatial, fixture.loss,
				fixture.catalog, service, fixture.cache, fixedClock{fixture.now})
			if newErr != nil {
				t.Fatal(newErr)
			}
			_, resolveErr := resolver.Resolve(context.Background(), report.AnalysisReference{
				Kind: report.AuthoritySurvivalAssessment, ID: fixture.survivalID,
			})
			if !test.wantError {
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				return
			}
			assertErrorIs(t, resolveErr, report.ErrUnsafeStoredAnalysis)
			assertErrorIs(t, resolveErr, domain.ErrInsufficientData)
			if errors.Is(resolveErr, report.ErrInvalidAuthority) {
				t.Fatalf("损坏服务端时间被误分类为客户端权威引用错误: %v", resolveErr)
			}
		})
	}
}

type countingCatalog struct {
	base      applicationsurvival.CatalogService
	listCalls int
	getCalls  int
}

func (c *countingCatalog) ListCases(ctx context.Context) ([]applicationsurvival.HistoricalCase, error) {
	c.listCalls++
	return c.base.ListCases(ctx)
}

func (c *countingCatalog) GetCase(ctx context.Context,
	id string,
) (applicationsurvival.HistoricalCaseDetail, error) {
	c.getCalls++
	return c.base.GetCase(ctx, id)
}

type countingSurvival struct {
	base  applicationsurvival.AssessmentService
	calls int
}

func (s *countingSurvival) AssessCase(ctx context.Context,
	id string,
) (applicationsurvival.ReplayAssessment, error) {
	s.calls++
	return s.base.AssessCase(ctx, id)
}

type failingSurvival struct{ err error }

func (s *failingSurvival) AssessCase(context.Context,
	string,
) (applicationsurvival.ReplayAssessment, error) {
	return applicationsurvival.ReplayAssessment{}, s.err
}

type overflowCatalog struct{}

func (overflowCatalog) ListCases(context.Context) ([]applicationsurvival.HistoricalCase, error) {
	return make([]applicationsurvival.HistoricalCase, maxSurvivalAuthorityCases+1), nil
}

func (overflowCatalog) GetCase(context.Context,
	string,
) (applicationsurvival.HistoricalCaseDetail, error) {
	return applicationsurvival.HistoricalCaseDetail{}, fmt.Errorf("%w: 不应读取详情", domain.ErrNotFound)
}

type driftingCatalog struct {
	base applicationsurvival.CatalogService
}

func (c *driftingCatalog) ListCases(ctx context.Context) ([]applicationsurvival.HistoricalCase, error) {
	values, err := c.base.ListCases(ctx)
	if err == nil && len(values) > 0 {
		values[0].ScenarioID = "scenario-cross-binding"
	}
	return values, err
}

func (c *driftingCatalog) GetCase(ctx context.Context,
	id string,
) (applicationsurvival.HistoricalCaseDetail, error) {
	return c.base.GetCase(ctx, id)
}
