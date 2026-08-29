package authority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestHazardAuthorityUsesBoundedReaderAndImmutableCache(t *testing.T) {
	fixture := newResolverFixture(t)
	reference := report.AnalysisReference{
		Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
	}
	first, err := fixture.resolver.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.risk.calls != 1 || fixture.risk.lastLimits != defaultHazardLimits() {
		t.Fatalf("风险读取未收到前置边界: calls=%d limits=%+v", fixture.risk.calls, fixture.risk.lastLimits)
	}
	fixture.risk.value.Assessment.Decision.Level = hazard.RiskVeryHigh
	second, err := fixture.resolver.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.risk.calls != 1 || !bytes.Equal(first.AnalysisJSON, second.AnalysisJSON) {
		t.Fatalf("同一引用未保持不可变: calls=%d first=%s second=%s",
			fixture.risk.calls, first.AnalysisJSON, second.AnalysisJSON)
	}
}

func TestHazardAuthorityExpiresWithoutRebuildingSameReference(t *testing.T) {
	fixture := newResolverFixture(t)
	clock := &mutableClock{value: fixture.now}
	resolver, err := New(fixture.risk, fixture.spatial, fixture.loss,
		fixture.catalog, fixture.survival, fixture.cache, clock)
	if err != nil {
		t.Fatal(err)
	}
	reference := report.AnalysisReference{
		Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
	}
	if _, err = resolver.Resolve(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	clock.value = fixture.risk.value.Snapshot.ValidTo
	_, err = resolver.Resolve(context.Background(), reference)
	if !errors.Is(err, domain.ErrNotFound) || fixture.risk.calls != 1 {
		t.Fatalf("过期引用应失效且不得现场重建: calls=%d err=%v", fixture.risk.calls, err)
	}
}

func TestHazardAuthoritySupportsDebrisFlowAndRejectsCacheTamper(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.risk.value.Snapshot.HazardType = hazard.TypeDebrisFlow
	fixture.risk.value.Assessment.HazardType = hazard.TypeDebrisFlow
	reference := report.AnalysisReference{
		Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
	}
	value, err := fixture.resolver.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	var dto report.HazardAuthorityAnalysis
	if err = json.Unmarshal(value.AnalysisJSON, &dto); err != nil || dto.HazardType != "debris_flow" {
		t.Fatalf("泥石流权威投影错误: dto=%+v err=%v", dto, err)
	}
	mutateHazardCache(t, fixture.cache, reference.ID, func(record *hazardAuthorityRecord) {
		record.Analysis.RiskLevel = "catastrophic"
	})
	_, err = fixture.resolver.Resolve(context.Background(), reference)
	assertErrorIs(t, err, report.ErrUnsafeStoredAnalysis)
}

func TestSameZoneIDsRejectsDuplicatesOnEitherSide(t *testing.T) {
	riskZones := []ports.HazardZoneSummary{
		{ID: "zone-1", SnapshotID: "snapshot-1"},
		{ID: "zone-1", SnapshotID: "snapshot-1"},
	}
	spatialZones := []spatialanalysis.ZoneResult{{ZoneID: "zone-1"}, {ZoneID: "zone-2"}}
	if sameZoneIDs("snapshot-1", riskZones, spatialZones) {
		t.Fatal("风险侧重复标识不应通过集合比较")
	}
	riskZones[1].ID = "zone-2"
	spatialZones[1].ZoneID = "zone-1"
	if sameZoneIDs("snapshot-1", riskZones, spatialZones) {
		t.Fatal("空间侧重复标识不应通过集合比较")
	}
}

func TestHazardAuthorityRejectsDecisionZoneBindingDrift(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.risk.value.Assessment.Decision.HighestZoneIDs = []string{"zone-other"}
	_, err := fixture.resolver.Resolve(context.Background(), report.AnalysisReference{
		Kind: report.AuthorityHazardSnapshot, ID: fixture.risk.value.Snapshot.ID,
	})
	assertErrorIs(t, err, report.ErrInvalidAuthority)
}

func mutateHazardCache(t *testing.T, cache *jsonCache, id string,
	mutate func(*hazardAuthorityRecord),
) {
	t.Helper()
	key := hazardCachePrefix + id
	var record hazardAuthorityRecord
	if err := json.Unmarshal(cache.data[key], &record); err != nil {
		t.Fatal(err)
	}
	mutate(&record)
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	cache.data[key] = payload
}

type mutableClock struct{ value time.Time }

func (c *mutableClock) Now() time.Time { return c.value }
