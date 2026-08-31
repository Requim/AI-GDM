package collection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestLHASACollectorCollectsAndSavesFreshAnalysis(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	snapshot, zones := lhasaFixtureAnalysis(artifact, "transform-v1")
	reader := &lhasaAnalysisStoreStub{latestErr: domain.ErrNotFound}
	fetcher := &lhasaFetcherStub{artifact: artifact}
	processor := &lhasaProcessorStub{snapshot: snapshot, zones: zones}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: artifact},
		fetcher, processor, reader, now)

	got, gotZones, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != snapshot.ID || len(gotZones) != 1 || reader.saveCalls != 1 {
		t.Fatalf("Collect() = %+v, zones=%d, saves=%d", got, len(gotZones), reader.saveCalls)
	}
	if fetcher.calls != 1 || processor.calls != 1 || reader.saved.ID != snapshot.ID {
		t.Fatalf("calls fetch=%d process=%d saved=%s", fetcher.calls, processor.calls, reader.saved.ID)
	}
	if reader.lockCalls != 1 || reader.releaseCalls != 1 || reader.reconcileCalls != 1 {
		t.Fatalf("刷新协调次数错误: lock=%d release=%d reconcile=%d",
			reader.lockCalls, reader.releaseCalls, reader.reconcileCalls)
	}
}

func TestLHASACollectorReportsRefreshLockReleaseFailure(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	snapshot, zones := lhasaFixtureAnalysis(artifact, "transform-v1")
	sentinel := errors.New("unlock failed")
	store := &lhasaAnalysisStoreStub{latestErr: domain.ErrNotFound, releaseErr: sentinel}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: artifact},
		&lhasaFetcherStub{artifact: artifact}, &lhasaProcessorStub{snapshot: snapshot, zones: zones}, store, now)
	got, gotZones, err := collector.Collect(context.Background())
	if !errors.Is(err, sentinel) || got.ID != "" || gotZones != nil || store.releaseCalls != 1 {
		t.Fatalf("释放锁失败未上报: snapshot=%+v zones=%v err=%v releases=%d",
			got, gotZones, err, store.releaseCalls)
	}
}

func TestLHASACollectorReusesSameCompleteAnalysis(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	snapshot, zones := lhasaFixtureAnalysis(artifact, "transform-v1")
	store := &lhasaAnalysisStoreStub{latest: snapshot, latestZones: zones}
	fetcher := &lhasaFetcherStub{}
	processor := &lhasaProcessorStub{}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: artifact},
		fetcher, processor, store, now)

	got, _, err := collector.Collect(context.Background())
	if err != nil || got.ID != snapshot.ID {
		t.Fatalf("Collect() = %+v, error=%v", got, err)
	}
	if fetcher.calls != 0 || processor.calls != 0 || store.saveCalls != 0 {
		t.Fatalf("重复制品仍执行：fetch=%d process=%d save=%d", fetcher.calls, processor.calls, store.saveCalls)
	}
}

func TestLHASACollectorPreservesFirstSeenTimeForSameRevision(t *testing.T) {
	now := lhasaNow()
	oldArtifact := lhasaFixtureArtifact(now.Add(-12*time.Hour - time.Nanosecond))
	snapshot, zones := lhasaFixtureAnalysis(oldArtifact, "transform-v1")
	discovered := lhasaFixtureArtifact(now)
	store := &lhasaAnalysisStoreStub{latest: snapshot, latestZones: zones}
	fetcher := &lhasaFetcherStub{artifact: discovered}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: discovered},
		fetcher, &lhasaProcessorStub{}, store, now)

	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("Collect() error = %v", err)
	}
	if fetcher.calls != 0 || store.saveCalls != 0 {
		t.Fatalf("过期的同一修订仍执行，fetch=%d save=%d", fetcher.calls, store.saveCalls)
	}
}

func TestLHASACollectorProcessesChangedRevisionAtStableURL(t *testing.T) {
	now := lhasaNow()
	oldArtifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(oldArtifact, "transform-v1")
	current := lhasaFixtureArtifact(now)
	current.Provenance.SourceRevision = `"revision-2"`
	processed, processedZones := lhasaFixtureAnalysis(current, "transform-v1")
	store := &lhasaAnalysisStoreStub{latest: latest, latestZones: latestZones}
	fetcher := &lhasaFetcherStub{artifact: current}
	processor := &lhasaProcessorStub{snapshot: processed, zones: processedZones}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: current},
		fetcher, processor, store, now)

	got, _, err := collector.Collect(context.Background())
	if err != nil || got.Source.SourceRevision != `"revision-2"` {
		t.Fatalf("Collect() = %+v, error=%v", got, err)
	}
	if fetcher.calls != 1 || processor.calls != 1 || store.saveCalls != 1 {
		t.Fatalf("calls fetch=%d process=%d save=%d", fetcher.calls, processor.calls, store.saveCalls)
	}
}

func TestLHASACollectorFallsBackForPipelineFailures(t *testing.T) {
	sentinel := errors.New("provider failed")
	cases := []struct {
		name  string
		stage string
	}{
		{name: "发现失败", stage: "discover"}, {name: "下载失败", stage: "fetch"},
		{name: "处理失败", stage: "process"}, {name: "保存失败", stage: "save"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			snapshot, zones, collector, store := lhasaFailureFixture(t, item.stage, sentinel)
			got, gotZones, err := collector.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != snapshot.ID || len(gotZones) != len(zones) || got.Status != hazard.SnapshotStale {
				t.Fatalf("fallback = %+v, zones=%d", got, len(gotZones))
			}
			if !got.Source.Stale || !contains(got.Source.QualityFlags, lhasaFallbackQualityFlag) {
				t.Fatalf("fallback provenance = %+v", got.Source)
			}
			if store.latest.Status != hazard.SnapshotAvailable || store.latest.Source.Stale {
				t.Fatal("回退标记污染了仓储原值")
			}
			if store.reconcileCalls != 1 {
				t.Fatalf("相同边界失败仍应完成范围协调: %d", store.reconcileCalls)
			}
		})
	}
}

func TestLHASACollectorFallbackAgeBoundary(t *testing.T) {
	now := lhasaNow()
	for _, item := range []struct {
		name    string
		age     time.Duration
		wantErr bool
	}{
		{name: "恰好十二小时", age: 12 * time.Hour},
		{name: "超过十二小时", age: 12*time.Hour + time.Nanosecond, wantErr: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			artifact := lhasaFixtureArtifact(now.Add(-item.age))
			snapshot, zones := lhasaFixtureAnalysis(artifact, "transform-v1")
			store := &lhasaAnalysisStoreStub{latest: snapshot, latestZones: zones}
			collector := newLHASATestCollector(t, &lhasaDiscoveryStub{err: errors.New("offline")},
				&lhasaFetcherStub{}, &lhasaProcessorStub{}, store, now)
			_, _, err := collector.Collect(context.Background())
			if item.wantErr != errors.Is(err, domain.ErrInsufficientData) {
				t.Fatalf("Collect() error = %v", err)
			}
		})
	}
}

func TestLHASACollectorRejectsInvalidRemoteDataset(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	artifact.Provenance.Dataset = "unexpected"
	store := &lhasaAnalysisStoreStub{latestErr: domain.ErrNotFound}
	collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: artifact},
		&lhasaFetcherStub{}, &lhasaProcessorStub{}, store, now)
	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Collect() error = %v", err)
	}
}

func TestLHASACollectorChecksChangedBoundaryBeforeArtifactValidation(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	invalidArtifact := artifact
	invalidArtifact.Provenance.Dataset = "unexpected"
	for _, item := range []struct {
		name     string
		artifact provenance.Artifact
		discover error
	}{
		{name: "制品发现失败", artifact: artifact, discover: errors.New("offline")},
		{name: "制品描述无效", artifact: invalidArtifact},
	} {
		t.Run(item.name, func(t *testing.T) {
			assertChangedBoundaryRejectsEarlyArtifactFailure(t, now, item.artifact, item.discover)
		})
	}
}

func assertChangedBoundaryRejectsEarlyArtifactFailure(t *testing.T, now time.Time,
	artifact provenance.Artifact, discoveryErr error,
) {
	t.Helper()
	latest, latestZones := lhasaFixtureAnalysis(lhasaFixtureArtifact(now.Add(-time.Hour)), "transform-v1")
	changed := lhasaFixtureBoundary()
	changed.Coverage.SHA256 = strings.Repeat("b", 64)
	store := &lhasaAnalysisStoreStub{latest: latest, latestZones: latestZones}
	discovery := &lhasaDiscoveryStub{artifact: artifact, err: discoveryErr}
	fetcher, processor := &lhasaFetcherStub{}, &lhasaProcessorStub{}
	collector := newLHASATestCollectorWithBoundary(t, discovery, fetcher,
		&lhasaBoundaryStub{value: changed}, processor, store, now)
	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) || !strings.Contains(err.Error(), "禁止回退到旧范围") {
		t.Fatalf("Collect() error=%v", err)
	}
	if store.reconcileCalls != 1 || discovery.calls != 1 || fetcher.calls != 0 ||
		processor.calls != 0 || store.saveCalls != 0 {
		t.Fatalf("calls reconcile=%d discover=%d fetch=%d process=%d save=%d",
			store.reconcileCalls, discovery.calls, fetcher.calls, processor.calls, store.saveCalls)
	}
}

func TestLHASACollectorFailsClosedWhenBoundaryVersionChanges(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(artifact, "transform-v1")
	changed := lhasaFixtureBoundary()
	changed.Coverage.SHA256 = strings.Repeat("b", 64)
	store := &lhasaAnalysisStoreStub{latest: latest, latestZones: latestZones}
	fetcher := &lhasaFetcherStub{artifact: artifact}
	processor := &lhasaProcessorStub{err: errors.New("clip failed")}
	collector := newLHASATestCollectorWithBoundary(t, &lhasaDiscoveryStub{artifact: artifact},
		fetcher, &lhasaBoundaryStub{value: changed}, processor, store, now)

	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) ||
		!strings.Contains(err.Error(), "禁止回退到旧范围") {
		t.Fatalf("Collect() error=%v", err)
	}
	if fetcher.calls != 1 || processor.calls != 1 || store.saveCalls != 0 || store.reconcileCalls != 1 {
		t.Fatalf("calls fetch=%d process=%d save=%d reconcile=%d", fetcher.calls, processor.calls,
			store.saveCalls, store.reconcileCalls)
	}
	if store.reconciledSelector != collector.analysisSelector() ||
		store.reconciledCoverage.Identity() != changed.Coverage.Identity() ||
		!store.reconciledAt.Equal(now) {
		t.Fatalf("范围协调记录错误: coverage=%s at=%s",
			store.reconciledCoverage.Identity(), store.reconciledAt)
	}
}

func TestLHASACollectorStopsWhenChangedCoverageCannotBeReconciled(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(artifact, "transform-v1")
	changed := lhasaFixtureBoundary()
	changed.Coverage.SHA256 = strings.Repeat("b", 64)
	sentinel := errors.New("database unavailable")
	store := &lhasaAnalysisStoreStub{
		latest: latest, latestZones: latestZones, reconcileErr: sentinel,
	}
	fetcher := &lhasaFetcherStub{artifact: artifact}
	processor := &lhasaProcessorStub{err: errors.New("不应处理")}
	collector := newLHASATestCollectorWithBoundary(t, &lhasaDiscoveryStub{artifact: artifact},
		fetcher, &lhasaBoundaryStub{value: changed}, processor, store, now)

	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, sentinel) || !errors.Is(err, domain.ErrInsufficientData) ||
		!strings.Contains(err.Error(), "协调 LHASA 行政边界范围") {
		t.Fatalf("Collect() error=%v", err)
	}
	if store.reconcileCalls != 1 || fetcher.calls != 0 || processor.calls != 0 || store.saveCalls != 0 {
		t.Fatalf("calls reconcile=%d fetch=%d process=%d save=%d", store.reconcileCalls,
			fetcher.calls, processor.calls, store.saveCalls)
	}
}

func TestLHASACollectorFallsBackWhenBoundaryProviderIsUnavailable(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(artifact, "transform-v1")
	store := &lhasaAnalysisStoreStub{latest: latest, latestZones: latestZones}
	collector := newLHASATestCollectorWithBoundary(t, &lhasaDiscoveryStub{artifact: artifact},
		&lhasaFetcherStub{}, &lhasaBoundaryStub{err: errors.New("offline")},
		&lhasaProcessorStub{}, store, now)

	snapshot, _, err := collector.Collect(context.Background())
	if err != nil || snapshot.Status != hazard.SnapshotStale || !snapshot.Source.Stale ||
		!contains(snapshot.Source.QualityFlags, lhasaBoundaryFallbackFlag) ||
		!contains(snapshot.Limitations, lhasaBoundaryFallbackLimitation) {
		t.Fatalf("Collect() snapshot=%+v error=%v", snapshot, err)
	}
}

func TestLHASACollectorRejectsFutureBoundaryCollectionTime(t *testing.T) {
	now := lhasaNow()
	artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	boundary := lhasaFixtureBoundary()
	boundary.Coverage.CollectedAt = now.Add(time.Minute)
	store := &lhasaAnalysisStoreStub{latestErr: domain.ErrNotFound}
	collector := newLHASATestCollectorWithBoundary(t, &lhasaDiscoveryStub{artifact: artifact},
		&lhasaFetcherStub{}, &lhasaBoundaryStub{value: boundary}, &lhasaProcessorStub{}, store, now)

	_, _, err := collector.Collect(context.Background())
	if !errors.Is(err, domain.ErrInsufficientData) ||
		!strings.Contains(err.Error(), "采集时间晚于当前时间") {
		t.Fatalf("Collect() error=%v", err)
	}
}

func lhasaFailureFixture(t *testing.T, stage string, sentinel error) (
	hazard.Snapshot, []hazard.RiskZone, *LHASACollector, *lhasaAnalysisStoreStub,
) {
	t.Helper()
	now := lhasaNow()
	oldArtifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(oldArtifact, "transform-v1")
	newArtifact := lhasaFixtureArtifact(now.Add(-30 * time.Minute))
	newArtifact.Provenance.SourceRevision = `"revision-2"`
	processed, processedZones := lhasaFixtureAnalysis(newArtifact, "transform-v1")
	discovery := &lhasaDiscoveryStub{artifact: newArtifact}
	fetcher := &lhasaFetcherStub{artifact: newArtifact}
	processor := &lhasaProcessorStub{snapshot: processed, zones: processedZones}
	store := &lhasaAnalysisStoreStub{latest: latest, latestZones: latestZones}
	setLHASAFailure(stage, sentinel, discovery, fetcher, processor, store)
	return latest, latestZones, newLHASATestCollector(t, discovery, fetcher, processor, store, now), store
}

func setLHASAFailure(stage string, err error, discovery *lhasaDiscoveryStub,
	fetcher *lhasaFetcherStub, processor *lhasaProcessorStub, store *lhasaAnalysisStoreStub,
) {
	switch stage {
	case "discover":
		discovery.err = err
	case "fetch":
		fetcher.err = err
	case "process":
		processor.err = err
	case "save":
		store.saveErr = err
	}
}

func newLHASATestCollector(t *testing.T, discovery *lhasaDiscoveryStub,
	fetcher *lhasaFetcherStub, processor *lhasaProcessorStub,
	store *lhasaAnalysisStoreStub, now time.Time,
) *LHASACollector {
	t.Helper()
	return newLHASATestCollectorWithBoundary(t, discovery, fetcher,
		&lhasaBoundaryStub{value: lhasaFixtureBoundary()}, processor, store, now)
}

func newLHASATestCollectorWithBoundary(t *testing.T, discovery *lhasaDiscoveryStub,
	fetcher *lhasaFetcherStub, boundary *lhasaBoundaryStub, processor *lhasaProcessorStub,
	store *lhasaAnalysisStoreStub, now time.Time,
) *LHASACollector {
	t.Helper()
	collector, err := NewLHASACollector(discovery, fetcher, boundary, processor, store, store, store,
		fixedClock{value: now}, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func lhasaFixtureArtifact(firstSeen time.Time) provenance.Artifact {
	return provenance.Artifact{
		Reference: "https://example.test/ImageServer/exportImage?bbox=china", MediaType: "image/tiff",
		LocalPath: "/data/lhasa.tif", SizeBytes: 10,
		Provenance: provenance.Provenance{
			Provider: lhasaProviderName, Dataset: lhasaDatasetName, DatasetVersion: "2.1",
			SourceRevision: `"revision-1"`, SourceURI: "https://example.test/ImageServer/exportImage?bbox=china",
			DataKind:            provenance.DataKindNowcast,
			RevisionFirstSeenAt: firstSeen, FetchedAt: firstSeen.Add(time.Minute), ValidFrom: firstSeen,
			ValidTo: firstSeen.Add(12 * time.Hour), SHA256: "checksum",
		},
	}
}

func lhasaFixtureAnalysis(artifact provenance.Artifact,
	version string,
) (hazard.Snapshot, []hazard.RiskZone) {
	source := artifact.Provenance
	source.TransformVersion = version
	coverage := lhasaFixtureBoundary().Coverage
	if coverage.CollectedAt.After(source.FetchedAt) {
		coverage.CollectedAt = source.FetchedAt
	}
	snapshot := hazard.Snapshot{
		ID: "lhasa-fixture", HazardType: hazard.TypeLandslide, ModelName: "NASA LHASA",
		ModelVersion: source.DatasetVersion,
		RunAt:        source.FetchedAt, ValidFrom: source.ValidFrom, ValidTo: source.ValidTo,
		RasterReference: artifact.Reference + "#sha256=checksum", ProbabilitySemantics: "测试概率",
		Thresholds: []hazard.RiskThreshold{{Level: hazard.RiskLow, Minimum: 0, Maximum: 1}},
		Status:     hazard.SnapshotAvailable, Source: source, Coverage: coveragePointer(coverage),
		Limitations: []string{"辅助研判"},
	}
	zones := []hazard.RiskZone{{ID: "zone-1", SnapshotID: snapshot.ID, Level: hazard.RiskLow}}
	return snapshot, zones
}

func lhasaFixtureBoundary() hazard.ProcessingBoundary {
	value := hazard.ProcessingBoundary{
		Coverage: hazard.Coverage{
			Mode: hazard.CoverageAdministrativeBoundary, RegionCode: "CN",
			BoundaryID: "CHN-ADM0-1", BoundaryType: "ADM0", BoundaryVersion: "2024",
			Source: "fixture", License: "Public Domain", Reference: "https://example.test/china.geojson",
			SHA256: strings.Repeat("a", 64), CollectedAt: lhasaNow().Add(-time.Hour),
		},
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: json.RawMessage(
			`[[[73.5,18],[135.1,18],[135.1,53.6],[73.5,53.6],[73.5,18]]]`)},
		InputReferences: []string{"https://example.test/china.geojson"},
	}
	value.Coverage.GeometrySHA256, _ = hazard.BoundaryGeometryDigest(value.Geometry)
	return value
}

func coveragePointer(value hazard.Coverage) *hazard.Coverage { return &value }

func lhasaNow() time.Time {
	return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type lhasaDiscoveryStub struct {
	artifact provenance.Artifact
	err      error
	calls    int
}

func (s *lhasaDiscoveryStub) DiscoverLatest(context.Context) (provenance.Artifact, error) {
	s.calls++
	return s.artifact, s.err
}

type lhasaFetcherStub struct {
	artifact provenance.Artifact
	err      error
	calls    int
}

func (s *lhasaFetcherStub) Fetch(context.Context, provenance.Artifact) (provenance.Artifact, error) {
	s.calls++
	return s.artifact, s.err
}

type lhasaProcessorStub struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
	calls    int
}

func (s *lhasaProcessorStub) ModelName() string { return "NASA LHASA" }
func (s *lhasaProcessorStub) Version() string   { return "transform-v1" }
func (s *lhasaProcessorStub) Process(context.Context,
	provenance.Artifact, hazard.ProcessingBoundary,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	s.calls++
	return s.snapshot, s.zones, s.err
}

type lhasaBoundaryStub struct {
	value hazard.ProcessingBoundary
	err   error
	calls int
}

func (s *lhasaBoundaryStub) RiskBoundary(context.Context) (hazard.ProcessingBoundary, error) {
	s.calls++
	return s.value, s.err
}

type lhasaAnalysisStoreStub struct {
	latest             hazard.Snapshot
	latestZones        []hazard.RiskZone
	latestErr          error
	saved              hazard.Snapshot
	savedZones         []hazard.RiskZone
	saveErr            error
	saveCalls          int
	reconciledSelector hazard.AnalysisSelector
	reconciledCoverage hazard.Coverage
	reconciledAt       time.Time
	reconcileErr       error
	reconcileCalls     int
	lockedSelector     hazard.AnalysisSelector
	lockErr            error
	lockCalls          int
	releaseErr         error
	releaseCalls       int
}

func (s *lhasaAnalysisStoreStub) LatestAnalysis(context.Context, hazard.AnalysisSelector) (
	hazard.Snapshot, []hazard.RiskZone, error,
) {
	return s.latest, s.latestZones, s.latestErr
}

func (s *lhasaAnalysisStoreStub) SaveAnalysis(_ context.Context,
	snapshot hazard.Snapshot, zones []hazard.RiskZone,
) error {
	s.saveCalls++
	s.saved, s.savedZones = snapshot, zones
	return s.saveErr
}

func (s *lhasaAnalysisStoreStub) ReconcileAnalysisCoverage(_ context.Context,
	selector hazard.AnalysisSelector, replacement hazard.Coverage, observedAt time.Time,
) error {
	s.reconcileCalls++
	s.reconciledSelector, s.reconciledCoverage, s.reconciledAt = selector, replacement, observedAt
	if s.reconcileErr != nil {
		return s.reconcileErr
	}
	if s.latestErr == nil && !sameCoverage(s.latest.Coverage, replacement) {
		s.latest, s.latestZones, s.latestErr = hazard.Snapshot{}, nil, domain.ErrNotFound
	}
	return nil
}

func (s *lhasaAnalysisStoreStub) LockAnalysisRefresh(_ context.Context,
	selector hazard.AnalysisSelector,
) (ports.HazardAnalysisRefreshLease, error) {
	s.lockCalls++
	s.lockedSelector = selector
	if s.lockErr != nil {
		return nil, s.lockErr
	}
	return &lhasaRefreshLeaseStub{store: s}, nil
}

type lhasaRefreshLeaseStub struct{ store *lhasaAnalysisStoreStub }

func (l *lhasaRefreshLeaseStub) Release() error {
	l.store.releaseCalls++
	return l.store.releaseErr
}
