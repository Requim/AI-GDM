package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestLossBaselineRepositorySQLContracts(t *testing.T) {
	for name, query := range map[string]string{
		"exposure":      exposureBaselinesSQL,
		"cost":          costBaselinesSQL,
		"vulnerability": vulnerabilitiesSQL,
	} {
		if !strings.Contains(query, "valid_from DESC") || !strings.Contains(query, "dataset_version DESC") {
			t.Errorf("%s 查询未按有效期和版本选择最新基线", name)
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
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewLossBaselineRepository(pool)
	for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
		if _, err = pool.Exec(ctx, "DELETE FROM "+table+" WHERE dataset_version LIKE 'integration-loss-%'"); err != nil {
			t.Fatal(err)
		}
	}
	prefix := time.Now().UTC().Format("20060102150405.000000000")
	version1 := "integration-loss-v1-" + prefix
	version2 := "integration-loss-v2-" + prefix
	cleanup := func() {
		for _, table := range []string{"loss_exposure_baselines", "loss_cost_baselines", "loss_vulnerabilities"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE dataset_version IN ($1,$2)", version1, version2)
		}
	}
	t.Cleanup(cleanup)
	now := time.Now().UTC().Add(-time.Hour)
	set1 := baselineSetFixture(version1, now, 100, 1000, 0.3)
	if err = repository.ReplaceBaselineSet(ctx, set1); err != nil {
		t.Fatal(err)
	}
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
	set2 := baselineSetFixture(version2, now.Add(time.Minute), 200, 2000, 0.4)
	if err = repository.ReplaceBaselineSet(ctx, set2); err != nil {
		t.Fatal(err)
	}
	population, err = repository.ExposureBaselines(ctx, "CN-11", loss.ExposurePopulation)
	if err != nil || len(population) != 1 || population[0].Quantity != 200 {
		t.Fatalf("最新人口基线 = %+v, err=%v", population, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE loss_exposure_baselines SET valid_to=$1 WHERE dataset_version=$2", now.Add(30*time.Minute), version2); err != nil {
		t.Fatal(err)
	}
	population, err = repository.ExposureBaselines(ctx, "CN-11", loss.ExposurePopulation)
	if err != nil || len(population) != 1 || population[0].Quantity != 100 {
		t.Fatalf("过期后回退人口基线 = %+v, err=%v", population, err)
	}
}

func baselineSetFixture(version string, validFrom time.Time, population, cost, vulnerability float64) loss.BaselineSet {
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
			PriceBaseDate: validFrom, Status: loss.BaselineDemoOnly, Source: source,
		}},
		Vulnerabilities: []loss.Vulnerability{{
			ID: "landslide-building-cn", AssetType: loss.AssetBuilding, HazardType: "landslide",
			IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
			DamageRatioLow: vulnerability - 0.1, DamageRatioMid: vulnerability, DamageRatioHigh: vulnerability + 0.1,
			CalibrationRegion: "CN", Status: loss.BaselineDemoOnly, Source: source,
		}},
	}
}
