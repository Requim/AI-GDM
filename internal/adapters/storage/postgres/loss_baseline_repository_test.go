package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestLossBaselineRepositorySQLContracts(t *testing.T) {
	for name, query := range map[string]string{
		"exposure":      exposureBaselinesSQL,
		"cost":          costBaselinesSQL,
		"vulnerability": vulnerabilitiesSQL,
	} {
		if !strings.Contains(query, "latest_valid_from DESC") || !strings.Contains(query, "dataset_version DESC") {
			t.Errorf("%s 查询未按有效期和版本选择最新基线", name)
		}
		if strings.Contains(query, "NOW()") {
			t.Errorf("%s 查询仍依赖数据库当前时间", name)
		}
	}
	content, err := migrationFiles.ReadFile("migrations/005_loss_baselines.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
		if !strings.Contains(sql, "CREATE TABLE "+table) {
			t.Errorf("迁移缺少 %s", table)
		}
	}
	if !strings.Contains(sql, "source->>'datasetVersion' = dataset_version") {
		t.Error("迁移未约束来源版本一致性")
	}
	for _, fragment := range []string{
		"required_costs(asset_type,unit)",
		"required_vulnerabilities(asset_type,intensity_band)",
		"FROM loss_cost_baselines cost",
		"FROM loss_vulnerabilities vulnerability",
		"COUNT(DISTINCT (cost.asset_type,cost.unit))",
		"COUNT(DISTINCT (vulnerability.asset_type,vulnerability.intensity_band))",
		"population.exposure_kind='population'",
		"road.exposure_kind='road'",
		"HAVING BOOL_AND(cost.status='approved')",
		"HAVING BOOL_AND(vulnerability.status='approved')",
		"calibration_region IN ($1,'CN')",
		"JOIN vulnerability_versions vulnerability USING (dataset_version)",
		"LIMIT 1",
	} {
		if !strings.Contains(baselineSetVersionSQL, fragment) {
			t.Errorf("完整基线版本查询缺少约束 %q", fragment)
		}
	}
	for name, query := range map[string]string{
		"exposure":      exposureBaselinesByVersionSQL,
		"cost":          costBaselinesByVersionSQL,
		"vulnerability": vulnerabilitiesByVersionSQL,
	} {
		if !strings.Contains(query, "baseline.dataset_version=$1") {
			t.Errorf("%s 查询未绑定事务内选定版本", name)
		}
		if strings.Contains(query, "NOW()") {
			t.Errorf("%s 查询仍依赖数据库当前时间", name)
		}
	}
	if !strings.Contains(costBaselinesByVersionSQL, "UNNEST($4::TEXT[],$5::TEXT[])") ||
		!strings.Contains(vulnerabilitiesByVersionSQL, "UNNEST($5::TEXT[],$6::TEXT[])") {
		t.Error("事务内基线读取未绑定实际资产、单位和强度需求")
	}
	for _, query := range []string{costBaselinesSQL, vulnerabilitiesSQL,
		costBaselinesByVersionSQL, vulnerabilitiesByVersionSQL} {
		if !strings.Contains(query, "status='approved'") {
			t.Errorf("基线查询未限制 approved: %s", query)
		}
	}
}

func TestLossBaselineRepositoryRejectsInvalidLookupsBeforeDatabaseAccess(t *testing.T) {
	repository := NewLossBaselineRepository(nil)
	for _, value := range []string{"", " CN", "CN ", strings.Repeat("x", 129)} {
		if _, err := repository.CostBaselines(context.Background(), value); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("CostBaselines(%q) error = %v", value, err)
		}
	}
	if _, err := repository.ExposureBaselines(context.Background(), "CN", loss.ExposureKind("unknown")); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ExposureBaselines() error = %v", err)
	}
	if err := repository.ReplaceBaselineSet(context.Background(), loss.BaselineSet{Version: "v1"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ReplaceBaselineSet() error = %v", err)
	}
	for _, input := range [][2]string{{"", "landslide"}, {"CN-11", " landslide"}, {"CN-11", ""}} {
		query := baselineQuery(input[0], input[1], time.Now())
		if _, err := repository.BaselineSet(context.Background(), query); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("BaselineSet(%q, %q) error = %v", input[0], input[1], err)
		}
	}
	query := baselineQuery("CN-11", "landslide", time.Time{})
	if _, err := repository.BaselineSet(context.Background(), query); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("BaselineSet() zero time error = %v", err)
	}
	query = baselineQuery("CN-11", "landslide", time.Now())
	if _, err := repository.BaselineSet(context.Background(), query); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("BaselineSet() nil store error = %v", err)
	}
}

func TestLossBaselineRepositoryReadsOneAtomicVersion(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	want := baselineSetFixture("atomic-v1", now, 100, 1000, 0.3)
	tx := newFakeBaselineReadTransaction(t, want)
	store := &fakeBaselineReadStore{tx: tx}
	repository := &LossBaselineRepository{readStore: store}

	readAt := now.Add(999 * time.Nanosecond)
	query := baselineQuery("CN-11", "landslide", readAt)
	got, err := repository.BaselineSet(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BaselineSet() = %#v, want %#v", got, want)
	}
	if len(store.options) != 1 || store.options[0].IsoLevel != pgx.RepeatableRead ||
		store.options[0].AccessMode != pgx.ReadOnly {
		t.Fatalf("BeginTx options = %#v", store.options)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("transaction committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	wantReadAt := now.Truncate(time.Microsecond)
	wantVersionArgs := baselineVersionArgs("CN-11", "landslide", wantReadAt, query.Requirements)
	if tx.versionSQL != baselineSetVersionSQL || !reflect.DeepEqual(tx.versionArgs, wantVersionArgs) {
		t.Fatalf("version query = %q args=%#v", tx.versionSQL, tx.versionArgs)
	}
	assertSelectedVersionUsedByEveryQuery(t, tx.calls, want.Version, wantReadAt)
}

func TestLossBaselineRepositoryNormalizesDatabaseTimesToUTC(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	want := baselineSetFixture("atomic-utc-v1", now, 100, 1000, 0.3)
	setBaselineValidTo(&want, now.Add(time.Hour))
	tx := newFakeBaselineReadTransaction(t, want)
	driverLocation := time.FixedZone("driver-local", 0)
	validFrom, validTo := now.In(driverLocation), now.Add(time.Hour).In(driverLocation)
	tx.rows[exposureQueryKey(loss.ExposurePopulation)][0][8] = validFrom
	tx.rows[exposureQueryKey(loss.ExposurePopulation)][0][9] = &validTo
	tx.rows[exposureQueryKey(loss.ExposureRoad)][0][8] = validFrom
	tx.rows[exposureQueryKey(loss.ExposureRoad)][0][9] = &validTo
	tx.rows[costBaselinesByVersionSQL][0][9] = now.In(driverLocation)
	tx.rows[costBaselinesByVersionSQL][0][12] = validFrom
	tx.rows[costBaselinesByVersionSQL][0][13] = &validTo
	tx.rows[vulnerabilitiesByVersionSQL][0][14] = validFrom
	tx.rows[vulnerabilitiesByVersionSQL][0][15] = &validTo
	repository := &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}

	got, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UTC 基线 = %#v, want %#v", got, want)
	}
	if got.Costs[0].PriceBaseDate.Location() != time.UTC || got.Population[0].Source.ValidFrom.Location() != time.UTC ||
		got.Vulnerabilities[0].Source.ValidTo.Location() != time.UTC {
		t.Fatalf("基线时间未规范化为 UTC: %+v", got)
	}
}

func TestLossBaselineRepositoryFailsWhenSelectedVersionIsIncomplete(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	for name, query := range map[string]string{
		"population":    exposureQueryKey(loss.ExposurePopulation),
		"road":          exposureQueryKey(loss.ExposureRoad),
		"cost":          costBaselinesByVersionSQL,
		"vulnerability": vulnerabilitiesByVersionSQL,
	} {
		t.Run(name, func(t *testing.T) {
			tx := newFakeBaselineReadTransaction(t, baselineSetFixture("atomic-v1", now, 100, 1000, 0.3))
			tx.rows[query] = nil
			repository := &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}
			_, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("BaselineSet() error = %v", err)
			}
			if tx.committed || !tx.rolledBack {
				t.Fatalf("transaction committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
			}
		})
	}
}

func TestLossBaselineRepositoryRejectsVersionMismatch(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	set := baselineSetFixture("atomic-v1", now, 100, 1000, 0.3)
	tx := newFakeBaselineReadTransaction(t, set)
	tx.rows[costBaselinesByVersionSQL][0][0] = "atomic-v2"
	repository := &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}

	_, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("BaselineSet() error = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("transaction committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
}

func TestLossBaselineRepositoryReturnsNotFoundWithoutCompleteVersion(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	tx := &fakeBaselineReadTransaction{versionErr: pgx.ErrNoRows}
	repository := &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}
	_, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("BaselineSet() error = %v", err)
	}
	if tx.committed || !tx.rolledBack || len(tx.calls) != 0 {
		t.Fatalf("transaction committed=%v rolledBack=%v queryCount=%d", tx.committed, tx.rolledBack, len(tx.calls))
	}
}

func TestLossBaselineRepositoryPreservesTransactionErrors(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	beginErr := errors.New("begin failed")
	repository := &LossBaselineRepository{readStore: &fakeBaselineReadStore{beginErr: beginErr}}
	if _, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now)); !errors.Is(err, beginErr) {
		t.Fatalf("begin error = %v", err)
	}

	commitErr := errors.New("commit failed")
	rollbackErr := errors.New("rollback failed")
	tx := newFakeBaselineReadTransaction(t, baselineSetFixture("atomic-v1", now, 100, 1000, 0.3))
	tx.commitErr, tx.rollbackErr = commitErr, rollbackErr
	repository = &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}
	_, err := repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
	if !errors.Is(err, commitErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("commit/rollback error = %v", err)
	}

	queryErr := errors.New("query failed")
	tx = newFakeBaselineReadTransaction(t, baselineSetFixture("atomic-v1", now, 100, 1000, 0.3))
	tx.queryErrors[costBaselinesByVersionSQL], tx.rollbackErr = queryErr, rollbackErr
	repository = &LossBaselineRepository{readStore: &fakeBaselineReadStore{tx: tx}}
	_, err = repository.BaselineSet(context.Background(), baselineQuery("CN-11", "landslide", now))
	if !errors.Is(err, queryErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("query/rollback error = %v", err)
	}
}

func TestLossBaselineRepositoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewLossBaselineRepository(pool)
	prefix := time.Now().UTC().Format("20060102150405.000000000")
	version1 := "integration-loss-v1-" + prefix
	version2 := "integration-loss-v2-" + prefix
	cleanupLossIntegration(t, ctx, pool, version1, version2)
	now := time.Now().UTC().Add(-time.Hour)
	set1 := baselineSetFixture(version1, now, 100, 1000, 0.3)
	replaceAndAssertComplete(t, ctx, repository, set1)
	assertLegacyBaselineReaders(t, ctx, repository)
	set1 = baselineSetFixture(version1, now.Add(time.Second), 110, 1100, 0.31)
	replaceAndAssertComplete(t, ctx, repository, set1)
	nextSet1 := baselineSetFixture(version1, now.Add(2*time.Second), 120, 1200, 0.32)
	assertConcurrentReplacementDoesNotMix(t, ctx, pool, repository, set1, nextSet1)
	set1 = nextSet1
	set2 := baselineSetFixture(version2, now.Add(time.Minute), 200, 2000, 0.4)
	replaceAndAssertComplete(t, ctx, repository, set2)
	population, err := repository.ExposureBaselines(ctx, "CN-11", loss.ExposurePopulation)
	if err != nil || len(population) != 1 || population[0].Quantity != 200 {
		t.Fatalf("最新人口基线 = %+v, err=%v", population, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE loss_exposure_baselines SET valid_to=$1 WHERE dataset_version=$2", now.Add(30*time.Minute), version2); err != nil {
		t.Fatal(err)
	}
	population, err = repository.ExposureBaselines(ctx, "CN-11", loss.ExposurePopulation)
	if err != nil || len(population) != 1 || population[0].Quantity != set1.Population[0].Quantity {
		t.Fatalf("过期后回退人口基线 = %+v, err=%v, want=%v", population, err,
			set1.Population[0].Quantity)
	}
	assertCompleteBaseline(t, ctx, repository, set1)
}

func TestLossBaselineRepositoryApprovedSelectionIntegration(t *testing.T) {
	ctx, repository, pool := openLossBaselineIntegration(t)
	prefix := time.Now().UTC().Format("20060102150405.000000000")
	versions := []string{"integration-loss-approved-" + prefix, "integration-loss-demo-" + prefix,
		"integration-loss-mixed-" + prefix}
	cleanupLossIntegration(t, ctx, pool, versions...)
	readAt := time.Now().UTC().Truncate(time.Microsecond)
	approved := baselineSetFixtureForRegion(versions[0], readAt.Add(-2*time.Hour), "CN-11")
	demo := baselineSetFixtureForRegion(versions[1], readAt.Add(-time.Hour), "CN-11")
	setBaselineStatus(&demo, loss.BaselineDemoOnly, "")
	mustReplaceBaselineSet(t, ctx, repository, approved)
	mustReplaceBaselineSet(t, ctx, repository, demo)
	assertBaselineVersion(t, ctx, repository, "CN-11", readAt, approved.Version)
	assertLegacyApprovedVersion(t, ctx, repository, approved.Version)

	mixed := mergeBaselineSets(
		baselineSetFixtureForRegion(versions[2], readAt.Add(-30*time.Minute), "CN-11"),
		baselineSetFixtureForRegion(versions[2], readAt.Add(-30*time.Minute), "CN"),
	)
	setBaselineStatusPart(&mixed, 1, loss.BaselineDemoOnly, "")
	mustReplaceBaselineSet(t, ctx, repository, mixed)
	assertBaselineVersion(t, ctx, repository, "CN-11", readAt, approved.Version)
	assertLegacyApprovedVersion(t, ctx, repository, approved.Version)

	deleteLossBaselineVersions(t, ctx, pool, approved.Version)
	if _, err := repository.BaselineSet(ctx, baselineQuery("CN-11", "landslide", readAt)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("仅剩 demo/mixed 版本时 BaselineSet() error = %v", err)
	}
}

func TestLossBaselineRepositoryRegionFallbackIntegration(t *testing.T) {
	ctx, repository, pool := openLossBaselineIntegration(t)
	prefix := time.Now().UTC().Format("20060102150405.000000000")
	versions := []string{"integration-loss-national-" + prefix, "integration-loss-regional-" + prefix,
		"integration-loss-duplicate-" + prefix}
	cleanupLossIntegration(t, ctx, pool, versions...)
	readAt := time.Now().UTC().Truncate(time.Microsecond)

	national := baselineSetFixtureForRegion(versions[0], readAt.Add(-time.Hour), "CN")
	mustReplaceBaselineSet(t, ctx, repository, national)
	selected := mustReadBaselineSet(t, ctx, repository, "CN-11", readAt)
	assertBaselineRegions(t, selected, "CN")
	deleteLossBaselineVersions(t, ctx, pool, national.Version)

	regional := baselineSetFixtureForRegion(versions[1], readAt.Add(-time.Hour), "CN-11")
	combined := mergeBaselineSets(regional,
		baselineSetFixtureForRegion(versions[1], readAt.Add(-time.Hour), "CN"))
	mustReplaceBaselineSet(t, ctx, repository, combined)
	selected = mustReadBaselineSet(t, ctx, repository, "CN-11", readAt)
	assertBaselineRegions(t, selected, "CN-11")
	deleteLossBaselineVersions(t, ctx, pool, combined.Version)

	duplicate := baselineSetFixtureForRegion(versions[2], readAt.Add(-time.Hour), "CN-11")
	duplicateCost := duplicate.Costs[0]
	duplicateCost.ID += "-duplicate"
	duplicate.Costs = append(duplicate.Costs, duplicateCost)
	mustReplaceBaselineSet(t, ctx, repository, duplicate)
	if _, err := repository.BaselineSet(ctx,
		baselineQuery("CN-11", "landslide", readAt)); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("重复区域成本候选未 fail-closed: %v", err)
	}
}

func TestLossServiceFallsBackFromNewerApprovedButIncompleteBaselineIntegration(t *testing.T) {
	ctx, riskRepository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	collectedAt := now.Add(-time.Minute)
	snapshot, zones := saveLossProjectionRiskWithWindow(t, ctx, riskRepository, now,
		"baseline-requirements", 1, collectedAt.Add(-time.Hour), collectedAt.Add(24*time.Hour))
	analysis := insertLossSpatialAnalysis(t, ctx, riskRepository, snapshot, zones,
		now.Add(-5*time.Minute), "baseline-requirements", false)
	projection := insertLossExposureProjection(t, ctx, riskRepository, snapshot, analysis, zones,
		"baseline-requirements", 3, collectedAt, true)
	assertLossAssessmentWindows(t, now, snapshot, projection)

	prefix := time.Now().UTC().Format("20060102150405.000000000")
	olderVersion, newerVersion := "integration-loss-service-complete-"+prefix,
		"integration-loss-service-partial-"+prefix
	cleanupLossIntegration(t, ctx, riskRepository.pool, olderVersion, newerVersion)
	baselines := NewLossBaselineRepository(riskRepository.pool)
	older := lossServiceBaselineSet(olderVersion, now.Add(-2*time.Hour), true)
	newer := lossServiceBaselineSet(newerVersion, now.Add(-time.Hour), false)
	mustReplaceBaselineSet(t, ctx, baselines, older)
	mustReplaceBaselineSet(t, ctx, baselines, newer)

	service, err := applicationloss.NewService(riskRepository, baselines, baselineServiceClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := service.Estimate(ctx, applicationloss.EstimateInput{SnapshotID: snapshot.ID})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Evidence.BaselineSet.Version != olderVersion || len(assessment.Evidence.Costs) != 2 ||
		len(assessment.Evidence.Vulnerabilities) != 2 {
		t.Fatalf("公开评估未回退到可计算 approved 版本: %+v", assessment.Evidence)
	}
}

func assertLossAssessmentWindows(t *testing.T, now time.Time, snapshot hazard.Snapshot,
	projection applicationloss.LossInputProjection,
) {
	t.Helper()
	spatial := projection.Analysis
	if snapshot.ValidFrom.After(now) || !snapshot.ValidTo.After(now) ||
		snapshot.Source.ValidFrom.After(now) || !snapshot.Source.ValidTo.After(now) {
		t.Fatalf("风险快照或来源窗口未覆盖评估时刻: snapshot=%+v source=%+v now=%s",
			snapshot, snapshot.Source, now)
	}
	if spatial.ProjectionCollectedAt.After(now) || spatial.ProjectionValidFrom.After(now) ||
		!spatial.ProjectionValidTo.After(now) {
		t.Fatalf("空间投影窗口未覆盖评估时刻: projection=%+v now=%s", spatial, now)
	}
	if spatial.ProjectionValidFrom.Before(snapshot.ValidFrom) ||
		spatial.ProjectionValidTo.After(snapshot.ValidTo) ||
		spatial.ProjectionValidFrom.Before(snapshot.Source.ValidFrom) ||
		spatial.ProjectionValidTo.After(snapshot.Source.ValidTo) {
		t.Fatalf("空间投影窗口超出风险快照或来源窗口: projection=%+v snapshot=%+v", spatial, snapshot)
	}
}

func TestLossServiceBaselineSetUsesUniqueRecordIdentifiers(t *testing.T) {
	for _, includeRoad := range []bool{false, true} {
		value := lossServiceBaselineSet("unique-records", time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC), includeRoad)
		if err := value.Validate(); err != nil {
			t.Fatalf("includeRoad=%t: %v", includeRoad, err)
		}
	}
}

func TestLossBaselineRepositoryReplayTimeIntegration(t *testing.T) {
	ctx, repository, pool := openLossBaselineIntegration(t)
	prefix := time.Now().UTC().Format("20060102150405.000000000")
	versions := []string{"integration-loss-replay-old-" + prefix, "integration-loss-replay-new-" + prefix}
	cleanupLossIntegration(t, ctx, pool, versions...)
	readAt := time.Date(2025, 1, 1, 0, 0, 0, 999, time.UTC)
	old := baselineSetFixtureForRegion(versions[0], readAt, "CN-11")
	setBaselineValidTo(&old, readAt.Add(30*time.Minute))
	newer := baselineSetFixtureForRegion(versions[1], readAt.Add(time.Hour), "CN-11")
	setBaselineValidTo(&newer, readAt.Add(2*time.Hour))
	mustReplaceBaselineSet(t, ctx, repository, old)
	mustReplaceBaselineSet(t, ctx, repository, newer)

	assertBaselineVersion(t, ctx, repository, "CN-11", readAt, old.Version)
	assertBaselineVersion(t, ctx, repository, "CN-11", readAt, old.Version)
	if _, err := repository.BaselineSet(ctx,
		baselineQuery("CN-11", "landslide", readAt.Add(30*time.Minute))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("validTo 边界未按开区间处理: %v", err)
	}
	assertBaselineVersion(t, ctx, repository, "CN-11", readAt.Add(time.Hour), newer.Version)
}

func openLossBaselineIntegration(t *testing.T) (context.Context, *LossBaselineRepository, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, NewLossBaselineRepository(pool), pool
}

func assertLegacyBaselineReaders(t *testing.T, ctx context.Context, repository *LossBaselineRepository) {
	t.Helper()
	population, err := repository.ExposureBaselines(ctx, "CN-11", loss.ExposurePopulation)
	if err != nil || len(population) != 1 || population[0].Quantity != 100 {
		t.Fatalf("人口基线 = %+v, err=%v", population, err)
	}
	costs, err := repository.CostBaselines(ctx, "CN-11")
	if err != nil || len(costs) != 1 || costs[0].CentralCents != 1000 {
		t.Fatalf("成本基线 = %+v, err=%v", costs, err)
	}
	vulnerabilities, err := repository.Vulnerabilities(ctx, "landslide")
	if err != nil || len(vulnerabilities) != 1 || vulnerabilities[0].DamageRatioMid != 0.3 {
		t.Fatalf("脆弱性基线 = %+v, err=%v", vulnerabilities, err)
	}
}

func assertLegacyApprovedVersion(t *testing.T, ctx context.Context, repository *LossBaselineRepository,
	wantVersion string,
) {
	t.Helper()
	costs, err := repository.CostBaselines(ctx, "CN-11")
	if err != nil || len(costs) == 0 {
		t.Fatalf("读取 approved 成本基线: values=%+v error=%v", costs, err)
	}
	for _, value := range costs {
		if value.Source.DatasetVersion != wantVersion || value.Status != loss.BaselineApproved {
			t.Fatalf("成本基线被 demo/mixed 污染: %+v", value)
		}
	}
	vulnerabilities, err := repository.Vulnerabilities(ctx, "landslide")
	if err != nil || len(vulnerabilities) == 0 {
		t.Fatalf("读取 approved 脆弱性基线: values=%+v error=%v", vulnerabilities, err)
	}
	for _, value := range vulnerabilities {
		if value.Source.DatasetVersion != wantVersion || value.Status != loss.BaselineApproved {
			t.Fatalf("脆弱性基线被 demo/mixed 污染: %+v", value)
		}
	}
}

func assertBaselineVersion(t *testing.T, ctx context.Context, repository *LossBaselineRepository,
	region string, readAt time.Time, wantVersion string,
) {
	t.Helper()
	value := mustReadBaselineSet(t, ctx, repository, region, readAt)
	if value.Version != wantVersion {
		t.Fatalf("BaselineSet().Version = %s, want %s", value.Version, wantVersion)
	}
}

func mustReadBaselineSet(t *testing.T, ctx context.Context, repository *LossBaselineRepository,
	region string, readAt time.Time,
) loss.BaselineSet {
	t.Helper()
	value, err := repository.BaselineSet(ctx, baselineQuery(region, "landslide", readAt))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func baselineQuery(region, hazard string, readAt time.Time) applicationloss.BaselineQuery {
	return applicationloss.BaselineQuery{RegionCode: region, HazardType: hazard, At: readAt,
		Requirements: applicationloss.BaselineRequirements{
			Costs: []applicationloss.CostBaselineRequirement{{
				AssetType: loss.AssetBuilding, Unit: "平方米",
			}},
			Vulnerabilities: []applicationloss.VulnerabilityBaselineRequirement{{
				AssetType: loss.AssetBuilding, IntensityBand: "high",
			}},
		}}
}

func baselineVersionArgs(region, hazard string, readAt time.Time,
	requirements applicationloss.BaselineRequirements,
) []any {
	costAssets, costUnits, vulnerabilityAssets, intensities := baselineRequirementColumns(requirements)
	return []any{region, hazard, readAt, costAssets, costUnits, vulnerabilityAssets, intensities}
}

func assertBaselineRegions(t *testing.T, value loss.BaselineSet, want string) {
	t.Helper()
	for _, item := range value.Population {
		if item.RegionCode != want {
			t.Fatalf("人口基线区域 = %s, want %s", item.RegionCode, want)
		}
	}
	for _, item := range value.Roads {
		if item.RegionCode != want {
			t.Fatalf("道路基线区域 = %s, want %s", item.RegionCode, want)
		}
	}
	for _, item := range value.Costs {
		if item.RegionCode != want {
			t.Fatalf("成本基线区域 = %s, want %s", item.RegionCode, want)
		}
	}
	for _, item := range value.Vulnerabilities {
		if item.CalibrationRegion != want {
			t.Fatalf("脆弱性基线区域 = %s, want %s", item.CalibrationRegion, want)
		}
	}
}

func mustReplaceBaselineSet(t *testing.T, ctx context.Context, repository *LossBaselineRepository,
	value loss.BaselineSet,
) {
	t.Helper()
	if err := repository.ReplaceBaselineSet(ctx, value); err != nil {
		t.Fatal(err)
	}
}

func deleteLossBaselineVersions(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	versions ...string,
) {
	t.Helper()
	for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE dataset_version = ANY($1)", versions); err != nil {
			t.Fatal(err)
		}
	}
}

func cleanupLossIntegration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versions ...string) {
	t.Helper()
	for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE dataset_version LIKE 'integration-loss-%'"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE dataset_version = ANY($1)", versions)
		}
	})
}

func replaceAndAssertComplete(t *testing.T, ctx context.Context, repository *LossBaselineRepository, want loss.BaselineSet) {
	t.Helper()
	if err := repository.ReplaceBaselineSet(ctx, want); err != nil {
		t.Fatal(err)
	}
	assertCompleteBaseline(t, ctx, repository, want)
}

func assertCompleteBaseline(t *testing.T, ctx context.Context, repository *LossBaselineRepository, want loss.BaselineSet) {
	t.Helper()
	got, err := repository.BaselineSet(ctx, baselineQuery("CN-11", "landslide", baselineReadTime()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("完整基线 = %#v, want %#v", got, want)
	}
}

func assertConcurrentReplacementDoesNotMix(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	writer *LossBaselineRepository, before, after loss.BaselineSet,
) {
	t.Helper()
	selected := make(chan struct{})
	proceed := make(chan struct{})
	reader := &LossBaselineRepository{readStore: pausingBaselineReadStore{
		delegate: pgxBaselineReadStore{pool: pool}, selected: selected, proceed: proceed,
	}}
	result := make(chan baselineSetResult, 1)
	go func() {
		value, err := reader.BaselineSet(ctx, baselineQuery("CN-11", "landslide", baselineReadTime()))
		result <- baselineSetResult{value: value, err: err}
	}()
	waitForBaselineSignal(t, ctx, selected, "等待基线版本选定")
	if err := writer.ReplaceBaselineSet(ctx, after); err != nil {
		close(proceed)
		t.Fatal(err)
	}
	close(proceed)
	read := waitForBaselineResult(t, ctx, result)
	if read.err != nil || !reflect.DeepEqual(read.value, before) {
		t.Fatalf("并发替换期间读取 = %#v, err=%v, want %#v", read.value, read.err, before)
	}
	assertCompleteBaseline(t, ctx, writer, after)
}

type baselineSetResult struct {
	value loss.BaselineSet
	err   error
}

func waitForBaselineSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("%s: %v", label, ctx.Err())
	}
}

func waitForBaselineResult(t *testing.T, ctx context.Context, result <-chan baselineSetResult) baselineSetResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatalf("等待完整基线读取结果: %v", ctx.Err())
		return baselineSetResult{}
	}
}

func baselineSetFixture(version string, validFrom time.Time, population, cost, vulnerability float64) loss.BaselineSet {
	validFrom = validFrom.Truncate(time.Microsecond)
	source := provenance.Provenance{
		Provider: "integration", Dataset: "loss-baseline", DatasetVersion: version,
		SourceRevision: "revision-1", SourceURI: "https://example.test/loss-baseline",
		Citation: "集成测试基线引用", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline,
		FetchedAt: validFrom, ValidFrom: validFrom, SHA256: strings.Repeat("a", 64),
		TransformVersion: "baseline-import-v1", QualityFlags: []string{"integration_fixture"},
	}
	return loss.BaselineSet{
		Version: version,
		Population: []loss.ExposureBaseline{{
			ID: "population-cn-11", RegionCode: "CN-11", Kind: loss.ExposurePopulation, Quantity: population,
			Unit: "people", DataYear: 2025, CoverageRatio: 1, Source: source,
		}},
		Roads: []loss.ExposureBaseline{{
			ID: "road-cn-11", RegionCode: "CN-11", Kind: loss.ExposureRoad, Quantity: 50,
			Unit: "meters", DataYear: 2025, CoverageRatio: 1, Source: source,
		}},
		Costs: []loss.CostBaseline{{
			ID: "building-cn-11", AssetType: loss.AssetBuilding, RegionCode: "CN-11", Unit: "平方米",
			LowCents: int64(cost / 2), CentralCents: int64(cost), HighCents: int64(cost * 2), Currency: "CNY",
			PriceBaseDate: validFrom, Status: loss.BaselineApproved, ApprovedBy: "integration-reviewer", Source: source,
		}},
		Vulnerabilities: []loss.Vulnerability{{
			ID: "landslide-building-cn", AssetType: loss.AssetBuilding, HazardType: "landslide",
			IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
			DamageRatioLow: vulnerability - 0.1, DamageRatioMid: vulnerability, DamageRatioHigh: vulnerability + 0.1,
			CalibrationRegion: "CN", Status: loss.BaselineApproved, ApprovedBy: "integration-reviewer", Source: source,
		}},
	}
}

func baselineSetFixtureForRegion(version string, validFrom time.Time, region string) loss.BaselineSet {
	value := baselineSetFixture(version, validFrom, 100, 1000, 0.3)
	suffix := strings.ToLower(region)
	value.Population[0].ID, value.Population[0].RegionCode = "population-"+suffix, region
	value.Roads[0].ID, value.Roads[0].RegionCode = "road-"+suffix, region
	value.Costs[0].ID, value.Costs[0].RegionCode = "building-"+suffix, region
	value.Vulnerabilities[0].ID = "landslide-building-" + suffix
	value.Vulnerabilities[0].CalibrationRegion = region
	return value
}

func lossServiceBaselineSet(version string, validFrom time.Time, includeRoad bool) loss.BaselineSet {
	value := baselineSetFixtureForRegion(version, validFrom, "CN")
	setBaselineValidTo(&value, validFrom.Add(24*time.Hour))
	value.Costs[0].ID, value.Costs[0].AssetType, value.Costs[0].Unit =
		"cost-facility-cn", loss.AssetFacility, "count"
	value.Vulnerabilities[0].ID = "landslide-facility-moderate-cn"
	value.Vulnerabilities[0].AssetType, value.Vulnerabilities[0].IntensityBand = loss.AssetFacility, "moderate"
	if !includeRoad {
		return value
	}
	roadCost := value.Costs[0]
	roadCost.ID, roadCost.AssetType, roadCost.Unit = "cost-road-cn", loss.AssetRoad, "meters"
	roadVulnerability := value.Vulnerabilities[0]
	roadVulnerability.ID, roadVulnerability.AssetType = "landslide-road-moderate-cn", loss.AssetRoad
	value.Costs = append(value.Costs, roadCost)
	value.Vulnerabilities = append(value.Vulnerabilities, roadVulnerability)
	return value
}

type baselineServiceClock struct{ now time.Time }

func (c baselineServiceClock) Now() time.Time { return c.now }

func mergeBaselineSets(left, right loss.BaselineSet) loss.BaselineSet {
	return loss.BaselineSet{Version: left.Version,
		Population: append(append([]loss.ExposureBaseline{}, left.Population...), right.Population...),
		Roads:      append(append([]loss.ExposureBaseline{}, left.Roads...), right.Roads...),
		Costs:      append(append([]loss.CostBaseline{}, left.Costs...), right.Costs...),
		Vulnerabilities: append(append([]loss.Vulnerability{}, left.Vulnerabilities...),
			right.Vulnerabilities...)}
}

func setBaselineStatus(value *loss.BaselineSet, status loss.BaselineStatus, approvedBy string) {
	for index := range value.Costs {
		value.Costs[index].Status, value.Costs[index].ApprovedBy = status, approvedBy
	}
	for index := range value.Vulnerabilities {
		value.Vulnerabilities[index].Status, value.Vulnerabilities[index].ApprovedBy = status, approvedBy
	}
}

func setBaselineStatusPart(value *loss.BaselineSet, index int, status loss.BaselineStatus,
	approvedBy string,
) {
	value.Costs[index].Status, value.Costs[index].ApprovedBy = status, approvedBy
	value.Vulnerabilities[index].Status, value.Vulnerabilities[index].ApprovedBy = status, approvedBy
}

func setBaselineValidTo(value *loss.BaselineSet, validTo time.Time) {
	validTo = validTo.UTC().Truncate(time.Microsecond)
	for index := range value.Population {
		value.Population[index].Source.ValidTo = validTo
	}
	for index := range value.Roads {
		value.Roads[index].Source.ValidTo = validTo
	}
	for index := range value.Costs {
		value.Costs[index].Source.ValidTo = validTo
	}
	for index := range value.Vulnerabilities {
		value.Vulnerabilities[index].Source.ValidTo = validTo
	}
}

type fakeBaselineReadStore struct {
	tx       *fakeBaselineReadTransaction
	beginErr error
	options  []pgx.TxOptions
}

func (s *fakeBaselineReadStore) BeginTx(_ context.Context, options pgx.TxOptions) (baselineReadTransaction, error) {
	s.options = append(s.options, options)
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.tx, nil
}

type pausingBaselineReadStore struct {
	delegate baselineReadStore
	selected chan<- struct{}
	proceed  <-chan struct{}
}

func (s pausingBaselineReadStore) BeginTx(ctx context.Context, options pgx.TxOptions) (baselineReadTransaction, error) {
	tx, err := s.delegate.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return pausingBaselineReadTransaction{baselineReadTransaction: tx, selected: s.selected, proceed: s.proceed}, nil
}

type pausingBaselineReadTransaction struct {
	baselineReadTransaction
	selected chan<- struct{}
	proceed  <-chan struct{}
}

func (t pausingBaselineReadTransaction) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return pausingBaselineRow{row: t.baselineReadTransaction.QueryRow(ctx, sql, args...),
		selected: t.selected, proceed: t.proceed}
}

type pausingBaselineRow struct {
	row      rowScanner
	selected chan<- struct{}
	proceed  <-chan struct{}
}

func (r pausingBaselineRow) Scan(dest ...any) error {
	if err := r.row.Scan(dest...); err != nil {
		return err
	}
	close(r.selected)
	<-r.proceed
	return nil
}

type baselineQueryCall struct {
	sql  string
	args []any
}

type fakeBaselineReadTransaction struct {
	version     string
	versionErr  error
	versionSQL  string
	versionArgs []any
	rows        map[string][][]any
	queryErrors map[string]error
	calls       []baselineQueryCall
	commitErr   error
	rollbackErr error
	committed   bool
	rolledBack  bool
}

func (t *fakeBaselineReadTransaction) QueryRow(_ context.Context, sql string, args ...any) rowScanner {
	t.versionSQL = sql
	t.versionArgs = append([]any(nil), args...)
	return fakeBaselineRow{values: []any{t.version}, err: t.versionErr}
}

func (t *fakeBaselineReadTransaction) Query(_ context.Context, sql string, args ...any) (baselineRows, error) {
	t.calls = append(t.calls, baselineQueryCall{sql: sql, args: append([]any(nil), args...)})
	key := baselineQueryKey(sql, args)
	if err := t.queryErrors[key]; err != nil {
		return nil, err
	}
	return &fakeBaselineRows{values: t.rows[key]}, nil
}

func (t *fakeBaselineReadTransaction) Commit(context.Context) error {
	t.committed = true
	return t.commitErr
}

func (t *fakeBaselineReadTransaction) Rollback(context.Context) error {
	t.rolledBack = true
	if t.rollbackErr != nil {
		return t.rollbackErr
	}
	if t.committed {
		return pgx.ErrTxClosed
	}
	return nil
}

type fakeBaselineRow struct {
	values []any
	err    error
}

func (r fakeBaselineRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignBaselineValues(dest, r.values)
}

type fakeBaselineRows struct {
	values  [][]any
	current int
	err     error
	closed  bool
}

func (r *fakeBaselineRows) Next() bool {
	if r.current >= len(r.values) {
		return false
	}
	r.current++
	return true
}

func (r *fakeBaselineRows) Scan(dest ...any) error {
	if r.current == 0 || r.current > len(r.values) {
		return errors.New("没有可扫描的基线行")
	}
	return assignBaselineValues(dest, r.values[r.current-1])
}

func (r *fakeBaselineRows) Close()     { r.closed = true }
func (r *fakeBaselineRows) Err() error { return r.err }

func assignBaselineValues(dest, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destinations=%d values=%d", len(dest), len(values))
	}
	for index, value := range values {
		target := reflect.ValueOf(dest[index])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return fmt.Errorf("scan destination %d is %T", index, dest[index])
		}
		if value == nil {
			target.Elem().SetZero()
			continue
		}
		source := reflect.ValueOf(value)
		if !source.Type().AssignableTo(target.Elem().Type()) {
			return fmt.Errorf("scan value %d type %T into %T", index, value, dest[index])
		}
		target.Elem().Set(source)
	}
	return nil
}

func newFakeBaselineReadTransaction(t *testing.T, set loss.BaselineSet) *fakeBaselineReadTransaction {
	t.Helper()
	return &fakeBaselineReadTransaction{
		version: set.Version,
		rows: map[string][][]any{
			exposureQueryKey(loss.ExposurePopulation): {exposureBaselineRow(t, set.Version, set.Population[0])},
			exposureQueryKey(loss.ExposureRoad):       {exposureBaselineRow(t, set.Version, set.Roads[0])},
			costBaselinesByVersionSQL:                 {costBaselineRow(t, set.Version, set.Costs[0])},
			vulnerabilitiesByVersionSQL:               {vulnerabilityRow(t, set.Version, set.Vulnerabilities[0])},
		},
		queryErrors: make(map[string]error),
	}
}

func exposureBaselineRow(t *testing.T, version string, value loss.ExposureBaseline) []any {
	t.Helper()
	return []any{version, value.ID, value.RegionCode, value.Kind, value.Quantity, value.Unit,
		value.DataYear, value.CoverageRatio, value.Source.ValidFrom, (*time.Time)(nil), baselineSourceJSON(t, value.Source)}
}

func costBaselineRow(t *testing.T, version string, value loss.CostBaseline) []any {
	t.Helper()
	return []any{version, value.ID, value.AssetType, value.RegionCode, value.Unit, value.LowCents,
		value.CentralCents, value.HighCents, value.Currency, value.PriceBaseDate, value.Status, value.ApprovedBy,
		value.Source.ValidFrom, (*time.Time)(nil), baselineSourceJSON(t, value.Source)}
}

func vulnerabilityRow(t *testing.T, version string, value loss.Vulnerability) []any {
	t.Helper()
	return []any{version, value.ID, value.AssetType, value.HazardType, value.IntensityBand,
		value.ImpactFractionLow, value.ImpactFractionMid, value.ImpactFractionHigh, value.DamageRatioLow,
		value.DamageRatioMid, value.DamageRatioHigh, value.CalibrationRegion, value.Status, value.ApprovedBy,
		value.Source.ValidFrom, (*time.Time)(nil), baselineSourceJSON(t, value.Source)}
}

func baselineSourceJSON(t *testing.T, source provenance.Provenance) []byte {
	t.Helper()
	value, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func baselineQueryKey(sql string, args []any) string {
	if sql == exposureBaselinesByVersionSQL && len(args) == 4 {
		return exposureQueryKey(args[2].(loss.ExposureKind))
	}
	return sql
}

func exposureQueryKey(kind loss.ExposureKind) string {
	return exposureBaselinesByVersionSQL + "|" + string(kind)
}

func assertSelectedVersionUsedByEveryQuery(t *testing.T, calls []baselineQueryCall, version string,
	readAt time.Time,
) {
	t.Helper()
	if len(calls) != 4 {
		t.Fatalf("query count = %d, want 4", len(calls))
	}
	for _, call := range calls {
		if len(call.args) == 0 || call.args[0] != version || !baselineArgsContainTime(call.args, readAt) {
			t.Fatalf("query did not bind selected version %q: %q args=%#v", version, call.sql, call.args)
		}
	}
}

func baselineArgsContainTime(values []any, wanted time.Time) bool {
	for _, value := range values {
		if timestamp, ok := value.(time.Time); ok && timestamp.Equal(wanted) {
			return true
		}
	}
	return false
}
