// Package authority 把持久化的确定性结果投影为 AI 可读取的固定权威 DTO。
package authority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Resolver 只从服务端端口读取并重新校验权威结果，不接受浏览器数值字段。
type Resolver struct {
	risks         ports.HazardAuthorityReader
	spatial       ports.SpatialAnalysisReader
	losses        ports.LossAssessmentReader
	catalog       applicationsurvival.CatalogService
	survival      applicationsurvival.AssessmentService
	cache         ports.Cache
	clock         ports.Clock
	survivalMu    sync.RWMutex
	survivalIndex map[string]applicationsurvival.ReplayAssessment
	survivalReady bool
}

var _ applicationagent.AuthoritativeAnalysisResolver = (*Resolver)(nil)

// New 创建四类确定性权威分析 resolver；cache 可以为空，此时不提供路线引用。
func New(risks ports.HazardAuthorityReader, spatial ports.SpatialAnalysisReader,
	losses ports.LossAssessmentReader, catalog applicationsurvival.CatalogService,
	survival applicationsurvival.AssessmentService, cache ports.Cache, clock ports.Clock,
) (*Resolver, error) {
	if risks == nil || spatial == nil || losses == nil || catalog == nil || survival == nil || clock == nil {
		return nil, fmt.Errorf("%w: 权威分析 resolver 依赖为空", domain.ErrInvalidInput)
	}
	return &Resolver{
		risks: risks, spatial: spatial, losses: losses, catalog: catalog,
		survival: survival, cache: cache, clock: clock,
	}, nil
}

// Resolve 按固定类型分派到服务端权威读取流程。
func (r *Resolver) Resolve(ctx context.Context, reference report.AnalysisReference) (report.Authority, error) {
	normalized, err := reference.Normalize()
	if err != nil {
		return report.Authority{}, err
	}
	switch normalized.Kind {
	case report.AuthorityHazardSnapshot:
		return r.resolveHazard(ctx, normalized.ID)
	case report.AuthorityEvacuationRoute:
		return r.resolveRoute(ctx, normalized.ID)
	case report.AuthorityLossAssessment:
		return r.resolveLoss(ctx, normalized.ID)
	case report.AuthoritySurvivalAssessment:
		return r.resolveSurvival(ctx, normalized.ID)
	default:
		return report.Authority{}, fmt.Errorf("%w: 权威分析类型未实现", domain.ErrInvalidInput)
	}
}

func (r *Resolver) newAuthority(kind report.AuthorityKind, id, version, schema string,
	analysis any,
) (report.Authority, error) {
	payload, err := json.Marshal(analysis)
	if err != nil {
		return report.Authority{}, fmt.Errorf("编码权威分析 DTO: %w", err)
	}
	return r.canonicalAuthority(kind, id, version, schema, payload)
}

func (r *Resolver) canonicalAuthority(kind report.AuthorityKind, id, version, schema string,
	payload json.RawMessage,
) (report.Authority, error) {
	now, err := r.resolvedAt()
	if err != nil {
		return report.Authority{}, err
	}
	value := report.Authority{
		Kind: kind, ID: id, Version: version, SchemaVersion: schema,
		AnalysisJSON: payload, ResolvedAt: now,
	}
	canonical, err := value.Canonical()
	if err != nil {
		return report.Authority{}, fmt.Errorf("规范化权威分析 %s/%s: %w", kind, id, err)
	}
	return canonical, nil
}

func (r *Resolver) resolvedAt() (time.Time, error) {
	now := r.clock.Now()
	if now.IsZero() {
		return time.Time{}, invalidBinding("权威分析解析时间为空", domain.ErrInvalidInput)
	}
	if _, offset := now.Zone(); offset != 0 {
		return time.Time{}, invalidBinding("权威分析解析时间不是 UTC", domain.ErrInvalidInput)
	}
	return now, nil
}

func unsafeStored(label string, err error) error {
	return fmt.Errorf("%s: %w", label, errors.Join(report.ErrUnsafeStoredAnalysis, err))
}

func invalidBinding(label string, err error) error {
	return fmt.Errorf("%s: %w", label, errors.Join(report.ErrInvalidAuthority, err))
}
