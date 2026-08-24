package collection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
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

func lhasaFailureFixture(t *testing.T, stage string, sentinel error) (
	hazard.Snapshot, []hazard.RiskZone, *LHASACollector, *lhasaAnalysisStoreStub,
) {
	t.Helper()
	now := lhasaNow()
	oldArtifact := lhasaFixtureArtifact(now.Add(-time.Hour))
	latest, latestZones := lhasaFixtureAnalysis(oldArtifact, "transform-v1")
	newArtifact := lhasaFixtureArtifact(now.Add(-30 * time.Minute))
	newArtifact.Reference = "https://example.test/new.tif"
	newArtifact.Provenance.SourceURI = newArtifact.Reference
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
	collector, err := NewLHASACollector(discovery, fetcher, processor,
		store, store, fixedClock{value: now}, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func lhasaFixtureArtifact(observed time.Time) provenance.Artifact {
	return provenance.Artifact{
		Reference: "https://example.test/20260824T0600.tif", MediaType: "image/tiff",
		LocalPath: "/data/lhasa.tif", SizeBytes: 10,
		Provenance: provenance.Provenance{
			Provider: lhasaProviderName, Dataset: lhasaDatasetName, DatasetVersion: "2.1.1",
			SourceURI: "https://example.test/20260824T0600.tif", DataKind: provenance.DataKindNowcast,
			ObservedAt: observed, FetchedAt: observed.Add(time.Minute), ValidFrom: observed,
			ValidTo: observed.Add(12 * time.Hour), SHA256: "checksum",
		},
	}
}

func lhasaFixtureAnalysis(artifact provenance.Artifact,
	version string,
) (hazard.Snapshot, []hazard.RiskZone) {
	source := artifact.Provenance
	source.TransformVersion = version
	snapshot := hazard.Snapshot{
		ID: "lhasa-fixture", HazardType: hazard.TypeLandslide, ModelName: "NASA LHASA", ModelVersion: "2.1.1",
		RunAt: source.ObservedAt, ValidFrom: source.ValidFrom, ValidTo: source.ValidTo,
		RasterReference: artifact.Reference + "#sha256=checksum", ProbabilitySemantics: "测试概率",
		Thresholds: []hazard.RiskThreshold{{Level: hazard.RiskLow, Minimum: 0, Maximum: 1}},
		Status:     hazard.SnapshotAvailable, Source: source, Limitations: []string{"辅助研判"},
	}
	zones := []hazard.RiskZone{{ID: "zone-1", SnapshotID: snapshot.ID, Level: hazard.RiskLow}}
	return snapshot, zones
}

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
}

func (s *lhasaDiscoveryStub) DiscoverLatest(context.Context) (provenance.Artifact, error) {
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
	provenance.Artifact,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	s.calls++
	return s.snapshot, s.zones, s.err
}

type lhasaAnalysisStoreStub struct {
	latest      hazard.Snapshot
	latestZones []hazard.RiskZone
	latestErr   error
	saved       hazard.Snapshot
	savedZones  []hazard.RiskZone
	saveErr     error
	saveCalls   int
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
