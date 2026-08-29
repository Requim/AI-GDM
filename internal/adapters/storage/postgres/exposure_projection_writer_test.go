package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

func TestExposureProjectionPersistsMergedProviderReferences(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-provider-refs", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer-provider-refs", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "writer-provider-refs", now.Add(-time.Minute))
	value.Input.Analysis.InputReferences = sortedExposureStrings(append(
		value.Input.Analysis.InputReferences, "https://api.worldpop.org/task/provider-ref"))
	value.Input.Analysis.DatasetReferences = sortedExposureStrings(append(
		value.Input.Analysis.DatasetReferences, "WorldPop Global 2 Population Data", "osm://2026-08-29"))
	if err := applicationloss.BindRiskProjectionIdentity(&value.Input); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatal(err)
	}
	got, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Analysis.InputReferences, value.Input.Analysis.InputReferences) ||
		!slices.Equal(got.Analysis.DatasetReferences, value.Input.Analysis.DatasetReferences) ||
		got.Analysis.ProjectionDigest != value.Input.Analysis.ProjectionDigest {
		t.Fatalf("合并后的供应商引用回读漂移: got=%+v want=%+v", got.Analysis, value.Input.Analysis)
	}
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, true)
}

func TestExposureProjectionSelectionBindsLatestSpatialAnalysis(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-analysis-binding", 1)
	analysisA := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-10*time.Minute), "analysis-a", false)
	projectionA := exposureProjectionFixture(t, snapshot, analysisA, zones, "analysis-a", now.Add(-5*time.Minute))
	if err := repository.SaveExposureProjection(ctx, projectionA); err != nil {
		t.Fatal(err)
	}
	analysisB := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-2*time.Minute), "analysis-b", false)
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysisB.ID, now, false)
	if _, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("最新空间分析 B 缺投影时错误复用 A: %v", err)
	}
	projectionB := exposureProjectionFixture(t, snapshot, analysisB, zones, "analysis-b", now.Add(-time.Minute))
	if err := repository.SaveExposureProjection(ctx, projectionB); err != nil {
		t.Fatal(err)
	}
	got, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil || got.Analysis.ID != analysisB.ID {
		t.Fatalf("新空间分析 B 未绑定自身投影: analysis=%q error=%v", got.Analysis.ID, err)
	}
}

func TestExposureProjectionWriterIsConcurrentIdempotentAndAppendOnly(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-idempotent", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer", false)
	first := exposureProjectionFixture(t, snapshot, analysis, zones, "first", now.Add(-time.Minute))

	errorsByCall := make(chan error, 4)
	var wait sync.WaitGroup
	for call := 0; call < 4; call++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByCall <- repository.SaveExposureProjection(ctx, first)
		}()
	}
	wait.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatalf("并发幂等保存失败: %v", err)
		}
	}
	assertExposureRowCounts(t, ctx, repository, first.Input.Analysis.ProjectionID, 1, 3, 3)

	second := exposureProjectionFixture(t, snapshot, analysis, zones, "second", now)
	if first.Input.Analysis.ProjectionID == second.Input.Analysis.ProjectionID {
		t.Fatal("不同真实内容生成了相同暴露投影标识")
	}
	if err := repository.SaveExposureProjection(ctx, second); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.pool.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_exposure_projections
		WHERE analysis_id=$1`, analysis.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("append-only projection count=%d error=%v", count, err)
	}
	assertExposureRowCounts(t, ctx, repository, first.Input.Analysis.ProjectionID, 1, 3, 3)
}

func TestExposureProjectionWriterCanonicalizesPostgresTimesAndReplays(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-time-roundtrip", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer-time-roundtrip", false)
	input := newLossExposureProjection(t, snapshot, analysis, zones, "sub-microsecond", 3,
		now.Add(-time.Minute))
	addSubMicrosecondProjectionTimes(&input)
	if err := applicationloss.BindRiskProjectionIdentity(&input); err != nil {
		t.Fatal(err)
	}
	value := exposurecollection.ExposureProjection{Input: input,
		ValidFrom: input.Analysis.ProjectionValidFrom, ValidTo: input.Analysis.ProjectionValidTo}
	if err := repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatal(err)
	}
	got, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Analysis.ProjectionID != input.Analysis.ProjectionID ||
		got.Analysis.ProjectionDigest != input.Analysis.ProjectionDigest ||
		!got.Analysis.ProjectionCollectedAt.Equal(input.Analysis.ProjectionCollectedAt) {
		t.Fatalf("PostgreSQL 时间往返导致投影身份漂移: got=%+v want=%+v", got.Analysis, input.Analysis)
	}
	if err = applicationloss.ValidateRiskProjectionIdentity(got); err != nil {
		t.Fatalf("回读投影身份无效: %v", err)
	}
	if err = repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatalf("亚微秒原对象重复保存不幂等: %v", err)
	}
	assertExposureRowCounts(t, ctx, repository, input.Analysis.ProjectionID, 1, 3, 3)
}

func TestExposureProjectionWriterRecoversMatchingIncompleteProjection(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-incomplete", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer-incomplete", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "writer-incomplete", now.Add(-time.Minute))
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertLossProjectionHeader(t, ctx, tx, value.Input)
	if _, err = tx.Exec(ctx, testInsertLossProjectionZoneSQL, value.Input.Analysis.ProjectionID,
		analysis.ID, zones[0].ID, value.Input.Zones[0].AreaSquareM,
		mustLossJSON(t, value.Input.Zones[0].AdminCodes)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.pool.Exec(ctx, `UPDATE spatial_exposure_projections SET complete=TRUE WHERE id=$1`,
		value.Input.Analysis.ProjectionID); err == nil {
		t.Fatal("缺少子行的投影被错误标记为 complete")
	}
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, false)
	if err = repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatalf("同内容未完成投影未能安全恢复: %v", err)
	}
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, true)
	assertExposureRowCounts(t, ctx, repository, value.Input.Analysis.ProjectionID, 1, 3, 3)
}

func TestExposureProjectionWriterRejectsImpossibleUnionArea(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-area", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer-area", false)
	valid := exposureProjectionFixture(t, snapshot, analysis, zones, "writer-area-valid", now.Add(-time.Minute))
	if valid.Input.Analysis.TotalAreaSquareMeters != 150 {
		t.Fatalf("双区合法重叠面积夹具异常: %v", valid.Input.Analysis.TotalAreaSquareMeters)
	}
	if err := repository.SaveExposureProjection(ctx, valid); err != nil {
		t.Fatalf("合法重叠 union=150 被拒绝: %v", err)
	}
	invalid := exposureProjectionFixture(t, snapshot, analysis, zones, "writer-area-invalid", now)
	invalid.Input.Analysis.TotalAreaSquareMeters = 1
	if err := applicationloss.BindRiskProjectionIdentity(&invalid.Input); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveExposureProjection(ctx, invalid); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("双区 union=1 未 fail-closed: %v", err)
	}
}

func TestExposureProjectionSchemaRejectsMalformedDigestsAndUnionCompletion(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	validHash, nextHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	values := []struct {
		name    string
		payload any
		valid   bool
	}{
		{name: "valid", payload: []string{validHash, nextHash}, valid: true},
		{name: "empty", payload: []string{}, valid: false},
		{name: "duplicate", payload: []string{validHash, validHash}, valid: false},
		{name: "unsorted", payload: []string{nextHash, validHash}, valid: false},
		{name: "uppercase", payload: []string{strings.Repeat("A", 64)}, valid: false},
		{name: "non-string", payload: []any{1}, valid: false},
		{name: "object", payload: map[string]string{"digest": validHash}, valid: false},
		{name: "null", payload: nil, valid: false},
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			var got bool
			if err := repository.pool.QueryRow(ctx, `SELECT valid_exposure_sha256_array($1::JSONB)`,
				mustLossJSON(t, value.payload)).Scan(&got); err != nil || got != value.valid {
				t.Fatalf("SHA-256 数组校验=%v error=%v", got, err)
			}
		})
	}
	for _, value := range []struct {
		name    string
		payload []string
		valid   bool
	}{
		{name: "bytewise-order", payload: []string{"Z", "中"}, valid: true},
		{name: "reverse-bytewise-order", payload: []string{"中", "Z"}, valid: false},
	} {
		t.Run("limitations-"+value.name, func(t *testing.T) {
			var got bool
			if err := repository.pool.QueryRow(ctx, `SELECT valid_exposure_projection_limitations($1::JSONB)`,
				mustLossJSON(t, value.payload)).Scan(&got); err != nil || got != value.valid {
				t.Fatalf("limitations 字节序校验=%v error=%v", got, err)
			}
		})
	}
	assertImpossibleUnionCannotComplete(t, ctx, repository)
}

func assertImpossibleUnionCannotComplete(t *testing.T, ctx context.Context,
	repository *HazardRepository,
) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "schema-union", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "schema-union", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "schema-union", now.Add(-time.Minute))
	value.Input.Analysis.TotalAreaSquareMeters = 1
	if err := applicationloss.BindRiskProjectionIdentity(&value.Input); err != nil {
		t.Fatal(err)
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := insertExposureHeader(ctx, tx, value)
	if err != nil || !inserted {
		t.Fatalf("插入畸形 union 头失败: inserted=%v error=%v", inserted, err)
	}
	if err = insertExposureZones(ctx, tx, value); err != nil {
		t.Fatal(err)
	}
	if err = insertExposureFeatures(ctx, tx, value); err != nil {
		t.Fatal(err)
	}
	if err = completeExposureProjection(ctx, tx, value); err == nil {
		t.Fatal("union 小于任一风险区面积的投影被错误完成")
	}
}

func TestHasCurrentExposureProjectionRecalculatesStoredIdentity(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "status-identity", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "status-identity", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "status-identity", now.Add(-time.Minute))
	value.Input.Analysis.Features[0].Quantity++
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if inserted, insertErr := insertExposureHeader(ctx, tx, value); insertErr != nil || !inserted {
		t.Fatalf("插入摘要错绑头失败: inserted=%v error=%v", inserted, insertErr)
	}
	if err = insertExposureZones(ctx, tx, value); err != nil {
		t.Fatal(err)
	}
	if err = insertExposureFeatures(ctx, tx, value); err != nil {
		t.Fatal(err)
	}
	if err = completeExposureProjection(ctx, tx, value); err != nil {
		t.Fatalf("结构合法的摘要错绑投影未能建立回归夹具: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, false)
}

func TestExposureProjectionWriterRejectsSameIDContentMutation(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-conflict", 2)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "stable", now.Add(-time.Minute))
	if err := repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*exposurecollection.ExposureProjection){
		"feature-quantity": func(mutated *exposurecollection.ExposureProjection) {
			mutated.Input.Analysis.Features[0].Quantity++
		},
		"zone-binding": func(mutated *exposurecollection.ExposureProjection) {
			mutated.Input.Analysis.Features[0].ZoneIDs = mutated.Input.Analysis.Features[0].ZoneIDs[:1]
		},
		"source-digest": func(mutated *exposurecollection.ExposureProjection) {
			mutated.Input.Analysis.SourceReferenceDigests[0] = strings.Repeat("f", 64)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := cloneExposureProjection(value)
			mutate(&mutated)
			if err := repository.SaveExposureProjection(ctx, mutated); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("同 ID 内容变化 error=%v", err)
			}
		})
	}
	assertExposureRowCounts(t, ctx, repository, value.Input.Analysis.ProjectionID, 2, 3, 4)
}

func TestExposureProjectionWriterRollsBackChildFailure(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "writer-rollback", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "rollback", now.Add(-time.Minute))
	missingZone := zones[0].ID + "-missing"
	value.Input.Zones[0].ID = missingZone
	for index := range value.Input.Analysis.Features {
		value.Input.Analysis.Features[index].ZoneIDs = []string{missingZone}
	}
	if err := applicationloss.BindRiskProjectionIdentity(&value.Input); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveExposureProjection(ctx, value); err == nil {
		t.Fatal("SaveExposureProjection() 未拒绝缺失的 projection zone FK")
	}
	var headers int
	if err := repository.pool.QueryRow(ctx, `SELECT COUNT(*) FROM spatial_exposure_projections WHERE id=$1`,
		value.Input.Analysis.ProjectionID).Scan(&headers); err != nil || headers != 0 {
		t.Fatalf("子行失败后残留 header=%d error=%v", headers, err)
	}
}

func TestHasCurrentExposureProjectionUsesApplicationClockAndIntegrity(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, "current-probe", 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), "writer", false)
	value := exposureProjectionFixture(t, snapshot, analysis, zones, "current", now.Add(-time.Minute))
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, false)
	if err := repository.SaveExposureProjection(ctx, value); err != nil {
		t.Fatal(err)
	}
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, now, true)
	assertCurrentExposure(t, ctx, repository, snapshot.ID+"-other", analysis.ID, now, false)
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID,
		value.ValidFrom.Add(-time.Microsecond), false)
	assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, value.ValidTo, false)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.HasCurrentExposureProjection(canceled, snapshot.ID, analysis.ID, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误链丢失: %v", err)
	}
	nonUTC := now.In(time.FixedZone("UTC+8", 8*60*60))
	if _, err := repository.HasCurrentExposureProjection(ctx, snapshot.ID, analysis.ID, nonUTC); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非 UTC now 未拒绝: %v", err)
	}
}

func TestHasCurrentExposureProjectionRejectsFutureAndIncomplete(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, scenario := range []struct {
		name       string
		collected  time.Time
		complete   bool
		queryClock time.Time
	}{
		{name: "future-collected", collected: now.Add(30 * time.Minute), complete: true, queryClock: now},
		{name: "incomplete", collected: now.Add(-time.Minute), complete: false, queryClock: now},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, scenario.name, 1)
			analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
				now.Add(-5*time.Minute), scenario.name, false)
			insertLossExposureProjection(t, ctx, repository, snapshot, analysis, zones,
				scenario.name, 3, scenario.collected, scenario.complete)
			assertCurrentExposure(t, ctx, repository, snapshot.ID, analysis.ID, scenario.queryClock, false)
		})
	}
}

func exposureProjectionFixture(t *testing.T, snapshot hazard.Snapshot,
	analysis spatialanalysis.Analysis, zones []hazard.RiskZone, revision string, collectedAt time.Time,
) exposurecollection.ExposureProjection {
	t.Helper()
	input := newLossExposureProjection(t, snapshot, analysis, zones, revision, 3,
		collectedAt.UTC().Truncate(time.Microsecond))
	return exposurecollection.ExposureProjection{Input: input,
		ValidFrom: input.Analysis.ProjectionValidFrom, ValidTo: input.Analysis.ProjectionValidTo}
}

func cloneExposureProjection(value exposurecollection.ExposureProjection) exposurecollection.ExposureProjection {
	result := value
	result.Input.Zones = append([]applicationloss.LossRiskZone(nil), value.Input.Zones...)
	result.Input.Analysis.Features = append([]applicationloss.LossExposureFeature(nil),
		value.Input.Analysis.Features...)
	result.Input.Analysis.SourceReferenceDigests = append([]string(nil),
		value.Input.Analysis.SourceReferenceDigests...)
	result.Input.Analysis.ProjectionLimitations = append([]string(nil),
		value.Input.Analysis.ProjectionLimitations...)
	for index := range result.Input.Analysis.Features {
		result.Input.Analysis.Features[index].ZoneIDs = append([]string(nil),
			value.Input.Analysis.Features[index].ZoneIDs...)
		result.Input.Analysis.Features[index].InputReferences = append([]string(nil),
			value.Input.Analysis.Features[index].InputReferences...)
	}
	return result
}

func addSubMicrosecondProjectionTimes(value *applicationloss.LossInputProjection) {
	add := func(input time.Time) time.Time {
		if input.IsZero() {
			return input
		}
		return input.Add(789 * time.Nanosecond)
	}
	value.Snapshot.RunAt, value.Snapshot.ValidFrom, value.Snapshot.ValidTo =
		add(value.Snapshot.RunAt), add(value.Snapshot.ValidFrom), add(value.Snapshot.ValidTo)
	value.Snapshot.Source.ObservedAt = add(value.Snapshot.Source.ObservedAt)
	value.Snapshot.Source.PublishedAt = add(value.Snapshot.Source.PublishedAt)
	value.Snapshot.Source.RevisionFirstSeenAt = add(value.Snapshot.Source.RevisionFirstSeenAt)
	value.Snapshot.Source.FetchedAt = add(value.Snapshot.Source.FetchedAt)
	value.Snapshot.Source.ValidFrom = add(value.Snapshot.Source.ValidFrom)
	value.Snapshot.Source.ValidTo = add(value.Snapshot.Source.ValidTo)
	value.Analysis.CalculatedAt = add(value.Analysis.CalculatedAt)
	value.Analysis.ProjectionCollectedAt = add(value.Analysis.ProjectionCollectedAt)
	value.Analysis.ProjectionValidFrom = add(value.Analysis.ProjectionValidFrom)
	value.Analysis.ProjectionValidTo = add(value.Analysis.ProjectionValidTo)
}

func assertExposureRowCounts(t *testing.T, ctx context.Context, repository *HazardRepository,
	projectionID string, zones, features, bindings int,
) {
	t.Helper()
	var gotZones, gotFeatures, gotBindings int
	err := repository.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM spatial_exposure_projection_zones WHERE projection_id=$1),
		(SELECT COUNT(*) FROM spatial_exposure_features WHERE projection_id=$1),
		(SELECT COUNT(*) FROM spatial_exposure_feature_zones WHERE projection_id=$1)`, projectionID).
		Scan(&gotZones, &gotFeatures, &gotBindings)
	if err != nil || gotZones != zones || gotFeatures != features || gotBindings != bindings {
		t.Fatalf("projection rows zones=%d features=%d bindings=%d error=%v",
			gotZones, gotFeatures, gotBindings, err)
	}
}

func assertCurrentExposure(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshotID, analysisID string, now time.Time, expected bool,
) {
	t.Helper()
	got, err := repository.HasCurrentExposureProjection(ctx, snapshotID, analysisID, now)
	if err != nil || got != expected {
		t.Fatalf("HasCurrentExposureProjection(%q,%q,%s)=%v error=%v",
			snapshotID, analysisID, now, got, err)
	}
}
