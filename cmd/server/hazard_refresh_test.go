package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestSpatialRefreshReusesPersistedAuthority(t *testing.T) {
	snapshot := hazard.Snapshot{ID: "snapshot-reuse"}
	upstreamZones := []hazard.RiskZone{{ID: "zone-1", SnapshotID: snapshot.ID}}
	persistedZones := []hazard.RiskZone{{ID: "zone-1", SnapshotID: snapshot.ID, AreaSquareM: 42, AreaCalculated: true}}
	analyzer := &countingSpatialAnalyzer{}
	evaluator := &countingRiskEvaluator{}
	writer := &countingAssessmentWriter{}
	reader := &fixedZoneReader{zones: persistedZones}
	analyses := &fixedSpatialAnalysisReader{value: spatialanalysis.Analysis{
		ID: "spatial-reuse", SnapshotID: snapshot.ID,
	}}
	exposureState := &fixedExposureProjectionChecker{exists: true}
	exposures := &countingExposureCollector{}
	refresh := &spatialRefresh{
		upstream: fixedHazardRefresher{snapshot: snapshot, zones: upstreamZones}, analyzer: analyzer,
		analyses: analyses,
		zones:    reader, evaluator: evaluator, assessments: writer, authority: &fixedRiskAuthorityReuser{reused: true},
		exposureState: exposureState, exposures: exposures, clock: fixedTestClock{value: testRefreshTime()},
		logger: testRefreshLogger(),
	}
	gotSnapshot, gotZones, err := refresh.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotSnapshot.ID != snapshot.ID || len(gotZones) != 1 || !gotZones[0].AreaCalculated || gotZones[0].AreaSquareM != 42 {
		t.Fatalf("复用结果=%+v zones=%+v", gotSnapshot, gotZones)
	}
	if analyzer.calls != 0 || evaluator.calls != 0 || writer.calls != 0 || reader.calls != 1 ||
		analyses.calls != 1 || exposureState.calls != 1 || exposures.calls != 0 {
		t.Fatalf("调用次数 analyzer=%d evaluator=%d writer=%d reader=%d exposureCheck=%d exposure=%d",
			analyzer.calls, evaluator.calls, writer.calls, reader.calls, exposureState.calls, exposures.calls)
	}
}

func TestSpatialRefreshCollectsMissingExposureProjection(t *testing.T) {
	snapshot := hazard.Snapshot{ID: "snapshot-exposure"}
	checker := &fixedExposureProjectionChecker{}
	collector := &countingExposureCollector{}
	refresh := &spatialRefresh{
		upstream: fixedHazardRefresher{snapshot: snapshot}, zones: &fixedZoneReader{},
		analyses: &fixedSpatialAnalysisReader{value: spatialanalysis.Analysis{
			ID: "spatial-exposure", SnapshotID: snapshot.ID,
		}},
		authority: &fixedRiskAuthorityReuser{reused: true}, exposureState: checker,
		exposures: collector, clock: fixedTestClock{value: testRefreshTime()}, logger: testRefreshLogger(),
	}
	if _, _, err := refresh.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 || collector.calls != 1 || collector.snapshotID != snapshot.ID ||
		checker.analysisID != "spatial-exposure" || collector.analysisID != "spatial-exposure" {
		t.Fatalf("暴露投影调用 checker=%d collector=%d snapshot=%q analysis=%q/%q",
			checker.calls, collector.calls, collector.snapshotID, checker.analysisID, collector.analysisID)
	}
}

func TestSpatialRefreshKeepsRiskWhenExposureProviderFails(t *testing.T) {
	want := errors.New("overpass unavailable")
	refresh := reusableSpatialRefresh(&countingExposureCollector{err: want})
	if _, _, err := refresh.Refresh(context.Background()); err != nil {
		t.Fatalf("普通暴露供应商失败不应中断风险刷新: %v", err)
	}
}

func TestSpatialRefreshPropagatesOuterCancellationDuringExposure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refresh := reusableSpatialRefresh(&countingExposureCollector{err: context.Canceled})
	if _, _, err := refresh.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error=%v, want context canceled", err)
	}
}

func TestSpatialRefreshFreshPathPersistsBeforeExposure(t *testing.T) {
	events := []string{}
	refresh, analyzer, evaluator, writer, checker, collector := freshSpatialRefresh(&events, nil)
	snapshot, zones, err := refresh.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"reuse", "analyze", "zones", "evaluate", "save", "check", "collect"}
	if snapshot.ID != "snapshot-fresh" || len(zones) != 1 || !zones[0].AreaCalculated ||
		!equalEvents(events, wantEvents) {
		t.Fatalf("fresh result=%+v zones=%+v events=%v", snapshot, zones, events)
	}
	if analyzer.calls != 1 || evaluator.calls != 1 || writer.calls != 1 || checker.calls != 1 || collector.calls != 1 {
		t.Fatalf("fresh calls analyzer=%d evaluator=%d writer=%d checker=%d collector=%d",
			analyzer.calls, evaluator.calls, writer.calls, checker.calls, collector.calls)
	}
	if !checker.now.Equal(testRefreshTime()) || collector.snapshotID != snapshot.ID ||
		checker.analysisID != "spatial-fresh" || collector.analysisID != "spatial-fresh" {
		t.Fatalf("fresh exposure binding now=%s snapshot=%q analysis=%q/%q",
			checker.now, collector.snapshotID, checker.analysisID, collector.analysisID)
	}
}

func TestSpatialRefreshFreshPathKeepsPersistedRiskWhenExposureFails(t *testing.T) {
	events := []string{}
	refresh, _, _, writer, _, _ := freshSpatialRefresh(&events, errors.New("overpass unavailable"))
	snapshot, zones, err := refresh.Refresh(context.Background())
	if err != nil {
		t.Fatalf("普通暴露失败不应回滚已固化风险结果: %v", err)
	}
	if snapshot.ID != "snapshot-fresh" || len(zones) != 1 || writer.calls != 1 ||
		!equalEvents(events, []string{"reuse", "analyze", "zones", "evaluate", "save", "check", "collect"}) {
		t.Fatalf("fresh degradation snapshot=%+v zones=%+v events=%v writer=%d", snapshot, zones, events, writer.calls)
	}
}

func TestSpatialRefreshFreshPathPropagatesExposureCancellation(t *testing.T) {
	events := []string{}
	refresh, _, _, writer, _, _ := freshSpatialRefresh(&events, context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := refresh.Refresh(ctx)
	if !errors.Is(err, context.Canceled) || writer.calls != 1 {
		t.Fatalf("fresh cancellation error=%v writer=%d events=%v", err, writer.calls, events)
	}
}

func freshSpatialRefresh(events *[]string, exposureErr error) (*spatialRefresh, *countingSpatialAnalyzer,
	*countingRiskEvaluator, *countingAssessmentWriter, *fixedExposureProjectionChecker,
	*countingExposureCollector,
) {
	snapshot := hazard.Snapshot{ID: "snapshot-fresh"}
	zones := []hazard.RiskZone{{ID: "zone-fresh", SnapshotID: snapshot.ID, AreaSquareM: 42, AreaCalculated: true}}
	analyzer := &countingSpatialAnalyzer{events: events}
	evaluator := &countingRiskEvaluator{events: events}
	writer := &countingAssessmentWriter{events: events}
	checker := &fixedExposureProjectionChecker{events: events}
	collector := &countingExposureCollector{err: exposureErr, events: events}
	refresh := &spatialRefresh{upstream: fixedHazardRefresher{snapshot: snapshot}, analyzer: analyzer,
		zones: &fixedZoneReader{zones: zones, events: events}, evaluator: evaluator, assessments: writer,
		authority: &fixedRiskAuthorityReuser{events: events}, exposureState: checker, exposures: collector,
		clock: fixedTestClock{value: testRefreshTime()}, logger: testRefreshLogger()}
	return refresh, analyzer, evaluator, writer, checker, collector
}

func equalEvents(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func reusableSpatialRefresh(collector exposureCollector) *spatialRefresh {
	snapshot := hazard.Snapshot{ID: "snapshot-exposure-failure"}
	return &spatialRefresh{
		upstream: fixedHazardRefresher{snapshot: snapshot}, zones: &fixedZoneReader{},
		analyses: &fixedSpatialAnalysisReader{value: spatialanalysis.Analysis{
			ID: "spatial-reused-failure", SnapshotID: snapshot.ID,
		}},
		authority:     &fixedRiskAuthorityReuser{reused: true},
		exposureState: &fixedExposureProjectionChecker{}, exposures: collector,
		clock: fixedTestClock{value: testRefreshTime()}, logger: testRefreshLogger(),
	}
}

func TestSpatialRefreshPreservesReuseError(t *testing.T) {
	want := errors.New("authority conflict")
	refresh := &spatialRefresh{
		upstream:  fixedHazardRefresher{snapshot: hazard.Snapshot{ID: "snapshot-conflict"}},
		authority: &fixedRiskAuthorityReuser{err: want},
	}
	_, _, err := refresh.Refresh(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("Refresh() error=%v, want wrapped conflict", err)
	}
}

type fixedHazardRefresher struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
}

func (f fixedHazardRefresher) Refresh(context.Context) (hazard.Snapshot, []hazard.RiskZone, error) {
	return f.snapshot, f.zones, nil
}

type fixedRiskAuthorityReuser struct {
	reused bool
	err    error
	events *[]string
}

func (f *fixedRiskAuthorityReuser) ReuseRiskAuthority(context.Context, hazard.Snapshot,
	[]hazard.RiskZone,
) (bool, error) {
	recordTestEvent(f.events, "reuse")
	return f.reused, f.err
}

type fixedZoneReader struct {
	zones  []hazard.RiskZone
	calls  int
	events *[]string
}

func (f *fixedZoneReader) ZonesBySnapshot(context.Context, string) ([]hazard.RiskZone, error) {
	f.calls++
	recordTestEvent(f.events, "zones")
	return f.zones, nil
}

type countingSpatialAnalyzer struct {
	calls  int
	events *[]string
}

func (f *countingSpatialAnalyzer) Analyze(context.Context, string) (spatialanalysis.Analysis, error) {
	f.calls++
	recordTestEvent(f.events, "analyze")
	return spatialanalysis.Analysis{ID: "spatial-fresh", SnapshotID: "snapshot-fresh"}, nil
}

type fixedSpatialAnalysisReader struct {
	value spatialanalysis.Analysis
	err   error
	calls int
}

func (f *fixedSpatialAnalysisReader) Get(context.Context, string) (spatialanalysis.Analysis, error) {
	return f.value, f.err
}

func (f *fixedSpatialAnalysisReader) LatestBySnapshot(context.Context,
	string,
) (spatialanalysis.Analysis, error) {
	f.calls++
	return f.value, f.err
}

type countingRiskEvaluator struct {
	calls  int
	events *[]string
}

func (f *countingRiskEvaluator) Evaluate(risk.Input) (risk.Assessment, error) {
	f.calls++
	recordTestEvent(f.events, "evaluate")
	return risk.Assessment{}, nil
}

type countingAssessmentWriter struct {
	calls  int
	events *[]string
}

func (f *countingAssessmentWriter) SaveRiskAssessment(context.Context, hazard.Snapshot, risk.Assessment) error {
	f.calls++
	recordTestEvent(f.events, "save")
	return nil
}

type fixedExposureProjectionChecker struct {
	exists     bool
	err        error
	calls      int
	now        time.Time
	analysisID string
	events     *[]string
}

func (f *fixedExposureProjectionChecker) HasCurrentExposureProjection(_ context.Context, _ string,
	analysisID string, now time.Time,
) (bool, error) {
	f.calls++
	f.now = now
	f.analysisID = analysisID
	recordTestEvent(f.events, "check")
	return f.exists, f.err
}

type countingExposureCollector struct {
	err        error
	calls      int
	snapshotID string
	analysisID string
	events     *[]string
}

func (f *countingExposureCollector) Collect(_ context.Context,
	snapshotID, analysisID string,
) (exposurecollection.ExposureProjection, error) {
	f.calls++
	f.snapshotID = snapshotID
	f.analysisID = analysisID
	recordTestEvent(f.events, "collect")
	return exposurecollection.ExposureProjection{}, f.err
}

func recordTestEvent(events *[]string, value string) {
	if events != nil {
		*events = append(*events, value)
	}
}

type fixedTestClock struct{ value time.Time }

func (f fixedTestClock) Now() time.Time { return f.value }

func testRefreshTime() time.Time {
	return time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
}

func testRefreshLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
