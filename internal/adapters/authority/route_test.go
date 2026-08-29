package authority

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestRouteAnalysisIDIsStableAndBindsSnapshotAndRank(t *testing.T) {
	fixture := newResolverFixture(t)
	first := recordRoute(t, fixture)
	second := recordRoute(t, fixture)
	if first != second {
		t.Fatalf("相同路线重复记录 ID 不稳定: %+v / %+v", first, second)
	}
	rankChanged := fixture
	rankChanged.route.Rank = 2
	rankRef := recordRoute(t, rankChanged)
	snapshotChanged := fixture
	snapshotChanged.snapshot.ID = "snapshot-route-2"
	snapshotRef := recordRoute(t, snapshotChanged)
	if rankRef.ID == first.ID || snapshotRef.ID == first.ID || rankRef.ID == snapshotRef.ID {
		t.Fatalf("路线排名或快照变化未改变 ID: first=%s rank=%s snapshot=%s",
			first.ID, rankRef.ID, snapshotRef.ID)
	}
	ttl := fixture.cache.ttls[routeCachePrefix+first.ID]
	if ttl <= 0 || ttl > maxRouteTTL || ttl != 2*time.Hour {
		t.Fatalf("路线权威 TTL=%s, want 2h and <=24h", ttl)
	}
}

func TestRouteCacheContainsOnlyExplicitAuthorityDTO(t *testing.T) {
	fixture := newResolverFixture(t)
	ref := recordRoute(t, fixture)
	payload := fixture.cache.data[routeCachePrefix+ref.ID]
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(routeAuthorityKeys) {
		t.Fatalf("路线缓存字段不是固定 DTO: %s", payload)
	}
	for _, key := range routeAuthorityKeys {
		if _, exists := object[key]; !exists {
			t.Fatalf("路线缓存缺少 %s: %s", key, payload)
		}
	}
	assertAuthorityHasNoPII(t, payload)
}

func TestRouteRecordSkipsExpiredOrUnavailableCache(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.snapshot.ValidTo = fixture.now.Add(-time.Second)
	fixture.snapshot.Source.ValidTo = fixture.snapshot.ValidTo
	ref, err := fixture.resolver.RecordRoute(context.Background(), fixture.snapshot,
		fixture.route, applicationevacuation.RouteSafetyRuleVersion)
	if err != nil || ref != nil || len(fixture.cache.data) != 0 {
		t.Fatalf("过期路线不应生成引用: ref=%+v err=%v cache=%v", ref, err, fixture.cache.data)
	}
	fixture = newResolverFixture(t)
	fixture.cache.setErr = errors.New("redis down")
	ref, err = fixture.resolver.RecordRoute(context.Background(), fixture.snapshot,
		fixture.route, applicationevacuation.RouteSafetyRuleVersion)
	if err == nil || ref != nil {
		t.Fatalf("缓存失败未降级为无引用: ref=%+v err=%v", ref, err)
	}
}

func TestRouteResolverRejectsMismatchedCachedIdentity(t *testing.T) {
	tests := map[string]struct {
		field string
		value any
	}{
		"analysis id": {field: "routeAnalysisId", value: "route-other"},
		"snapshot":    {field: "snapshotId", value: "snapshot-other"},
		"provider id": {field: "routeId", value: "provider-other"},
		"rank":        {field: "rank", value: 2},
		"mode":        {field: "mode", value: "walking"},
		"distance":    {field: "distanceMeters", value: 1300},
		"duration":    {field: "durationSeconds", value: 361},
		"risk score":  {field: "riskScore", value: 3.5},
		"score flag":  {field: "riskScoreAvailable", value: false},
		"intersects":  {field: "intersectsRiskZone", value: true},
		"rule":        {field: "ruleVersion", value: "route-rules-v2"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			ref := recordRoute(t, fixture)
			injectCacheField(t, fixture.cache, routeCachePrefix+ref.ID, test.field, test.value)
			_, err := fixture.resolver.Resolve(context.Background(), ref)
			assertErrorIs(t, err, report.ErrInvalidAuthority)
			assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
		})
	}
}
