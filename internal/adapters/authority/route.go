package authority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

const (
	routeCachePrefix = "authority:route:"
	maxRouteTTL      = 24 * time.Hour
)

type routeIdentity struct {
	SnapshotID         string                `json:"snapshotId"`
	RouteID            string                `json:"routeId"`
	Rank               int                   `json:"rank"`
	Mode               evacuation.TravelMode `json:"mode"`
	DistanceMeters     float64               `json:"distanceMeters"`
	DurationSeconds    int64                 `json:"durationSeconds"`
	RiskScore          float64               `json:"riskScore"`
	RiskScoreAvailable bool                  `json:"riskScoreAvailable"`
	IntersectsRiskZone bool                  `json:"intersectsRiskZone"`
	RuleVersion        string                `json:"ruleVersion"`
}

// RecordRoute 缓存无坐标、地址、几何、步骤和来源 URL 的路线权威投影。
func (r *Resolver) RecordRoute(ctx context.Context, snapshot hazard.Snapshot, route evacuation.Route,
	ruleVersion string,
) (*report.AnalysisReference, error) {
	if r.cache == nil {
		return nil, nil
	}
	now, err := r.resolvedAt()
	if err != nil {
		return nil, err
	}
	ttl, eligible, err := routeTTL(snapshot, now)
	if err != nil || !eligible {
		return nil, err
	}
	analysis, err := newRouteAnalysis(snapshot.ID, route, ruleVersion)
	if err != nil {
		return nil, err
	}
	if _, err = r.newAuthority(report.AuthorityEvacuationRoute, analysis.RouteAnalysisID,
		ruleVersion, report.AuthoritySchemaRouteV1, analysis); err != nil {
		return nil, err
	}
	if err = r.cache.Set(ctx, routeCachePrefix+analysis.RouteAnalysisID, analysis, ttl); err != nil {
		return nil, fmt.Errorf("缓存路线权威分析: %w", err)
	}
	return &report.AnalysisReference{Kind: report.AuthorityEvacuationRoute, ID: analysis.RouteAnalysisID}, nil
}

func (r *Resolver) resolveRoute(ctx context.Context, id string) (report.Authority, error) {
	if r.cache == nil {
		return report.Authority{}, fmt.Errorf("%w: 路线权威缓存未配置", domain.ErrNotFound)
	}
	var payload json.RawMessage
	found, err := r.cache.Get(ctx, routeCachePrefix+id, &payload)
	if err != nil {
		return report.Authority{}, fmt.Errorf("%w: 读取路线权威缓存: %w", domain.ErrProviderUnavailable, err)
	}
	if !found {
		return report.Authority{}, fmt.Errorf("%w: 路线权威分析 %s 不存在", domain.ErrNotFound, id)
	}
	var analysis report.RouteAuthorityAnalysis
	if err = decodeStrict(payload, &analysis); err != nil {
		return report.Authority{}, unsafeStored("路线权威缓存结构损坏", err)
	}
	if err = validateCachedRouteAnalysis(id, analysis); err != nil {
		return report.Authority{}, err
	}
	return r.newAuthority(report.AuthorityEvacuationRoute, id,
		applicationevacuation.RouteSafetyRuleVersion, report.AuthoritySchemaRouteV1, analysis)
}

func validateCachedRouteAnalysis(id string, analysis report.RouteAuthorityAnalysis) error {
	mode := evacuation.TravelMode(analysis.Mode)
	if mode != evacuation.TravelDriving && mode != evacuation.TravelWalking && mode != evacuation.TravelTransit {
		return unsafeRouteBinding("路线权威交通方式无效", domain.ErrInvalidInput)
	}
	if analysis.IntersectsRiskZone || analysis.RuleVersion != applicationevacuation.RouteSafetyRuleVersion {
		return unsafeRouteBinding("路线权威风险区或规则版本绑定损坏", domain.ErrInvalidInput)
	}
	identity := routeIdentity{
		SnapshotID: analysis.SnapshotID, RouteID: analysis.RouteID, Rank: analysis.Rank, Mode: mode,
		DistanceMeters: analysis.DistanceMeters, DurationSeconds: analysis.DurationSeconds,
		RiskScore: analysis.RiskScore, RiskScoreAvailable: analysis.RiskScoreAvailable,
		IntersectsRiskZone: analysis.IntersectsRiskZone, RuleVersion: analysis.RuleVersion,
	}
	recomputed, err := routeAnalysisID(identity)
	if err != nil || analysis.RouteAnalysisID != id || recomputed != id {
		return unsafeRouteBinding("路线权威内容寻址标识不一致", errors.Join(domain.ErrInvalidInput, err))
	}
	return nil
}

func unsafeRouteBinding(label string, err error) error {
	return fmt.Errorf("%s: %w", label,
		errors.Join(report.ErrUnsafeStoredAnalysis, report.ErrInvalidAuthority, err))
}

func newRouteAnalysis(snapshotID string, route evacuation.Route,
	ruleVersion string,
) (report.RouteAuthorityAnalysis, error) {
	if strings.TrimSpace(snapshotID) == "" || strings.TrimSpace(route.ID) == "" ||
		ruleVersion != applicationevacuation.RouteSafetyRuleVersion || route.Rank < 1 ||
		route.IntersectsRiskZone {
		return report.RouteAuthorityAnalysis{}, invalidBinding("路线权威身份、排名或规则无效", domain.ErrInvalidInput)
	}
	if err := route.Validate(); err != nil {
		return report.RouteAuthorityAnalysis{}, invalidBinding("路线权威数值无效", err)
	}
	identity := routeIdentity{
		SnapshotID: snapshotID, RouteID: route.ID, Rank: route.Rank, Mode: route.Mode,
		DistanceMeters: route.DistanceMeters, DurationSeconds: route.DurationSeconds,
		RiskScore: route.RiskScore, RiskScoreAvailable: route.RiskScoreProvided,
		IntersectsRiskZone: route.IntersectsRiskZone, RuleVersion: ruleVersion,
	}
	id, err := routeAnalysisID(identity)
	if err != nil {
		return report.RouteAuthorityAnalysis{}, err
	}
	return report.RouteAuthorityAnalysis{
		DistanceMeters: route.DistanceMeters, DurationSeconds: route.DurationSeconds,
		IntersectsRiskZone: route.IntersectsRiskZone, Mode: string(route.Mode), Rank: route.Rank,
		RiskScore: route.RiskScore, RiskScoreAvailable: route.RiskScoreProvided,
		RouteAnalysisID: id, RouteID: route.ID, RuleVersion: ruleVersion, SnapshotID: snapshotID,
	}, nil
}

func routeAnalysisID(identity routeIdentity) (string, error) {
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("编码路线权威标识: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "route-" + hex.EncodeToString(digest[:]), nil
}

func routeTTL(snapshot hazard.Snapshot, now time.Time) (time.Duration, bool, error) {
	if snapshot.ID == "" || !supportedHazardType(snapshot.HazardType) ||
		!validUTCTime(snapshot.ValidTo) || !validUTCTime(snapshot.Source.ValidTo) {
		return 0, false, invalidBinding("路线风险快照缺少有效期", domain.ErrInvalidInput)
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return 0, false, invalidBinding("路线风险快照状态不可用", domain.ErrInvalidInput)
	}
	if !snapshot.ValidTo.Equal(snapshot.Source.ValidTo) {
		return 0, false, invalidBinding("路线快照与来源有效期不一致", domain.ErrInvalidInput)
	}
	remaining := snapshot.ValidTo.Sub(now)
	if remaining <= 0 {
		return 0, false, nil
	}
	if remaining > maxRouteTTL {
		remaining = maxRouteTTL
	}
	return remaining, true, nil
}
