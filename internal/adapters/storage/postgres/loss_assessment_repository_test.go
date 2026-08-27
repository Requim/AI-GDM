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
)

func TestLossAssessmentRepositorySQLContracts(t *testing.T) {
	if !strings.Contains(insertLossAssessmentSQL, "ON CONFLICT (id) DO NOTHING") {
		t.Fatal("评估写入未声明同标识幂等策略")
	}
	if !strings.Contains(selectLossAssessmentSQL, "WHERE id=$1") {
		t.Fatal("评估读取未按标识查询")
	}
	content, err := migrationFiles.ReadFile("migrations/006_loss_assessments.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(content))
	for _, fragment := range []string{
		"create table loss_assessments",
		"assessment jsonb not null",
		"source_references jsonb not null",
		"assessment->>'id' = id",
		"assessment->>'snapshotid' = snapshot_id",
		"loss_assessments_snapshot_idx",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("损失评估迁移缺少契约片段 %q", fragment)
		}
	}
}

func TestLossAssessmentRepositoryRejectsInvalidInputsBeforeDatabaseAccess(t *testing.T) {
	repository := NewLossAssessmentRepository(nil)
	if err := repository.SaveAssessment(context.Background(), loss.Assessment{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("SaveAssessment() error = %v", err)
	}
	if _, err := repository.GetAssessment(context.Background(), " "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("GetAssessment() error = %v", err)
	}
}

func TestLossAssessmentRepositoryIntegration(t *testing.T) {
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
	value := lossAssessmentFixture(time.Now().UTC())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", value.ID)
	})
	repository := NewLossAssessmentRepository(pool)
	if err = repository.SaveAssessment(ctx, value); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAssessment(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != value.ID || stored.SnapshotID != value.SnapshotID ||
		stored.ConditionalMidCents != value.ConditionalMidCents {
		t.Fatalf("保存后读取结果不一致: %+v", stored)
	}
	if err = repository.SaveAssessment(ctx, value); err != nil {
		t.Fatalf("相同内容重复保存失败: %v", err)
	}
	conflict := value
	conflict.ConditionalMidCents++
	if err = repository.SaveAssessment(ctx, conflict); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同标识不同内容未被拒绝: %v", err)
	}
	if _, err = repository.GetAssessment(ctx, value.ID+"-missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺失评估错误 = %v", err)
	}
}

func lossAssessmentFixture(now time.Time) loss.Assessment {
	return loss.Assessment{
		ID:         "integration-loss-assessment-" + now.Format("20060102150405.000000000"),
		SnapshotID: "snapshot-integration-loss", FormulaVersion: loss.FormulaVersion,
		ScenarioMethod: "集成测试确定性公式", HazardType: "landslide", RegionCode: "CN-11",
		ConditionalLowCents: 100, ConditionalMidCents: 200, ConditionalHighCents: 300,
		ImpactAreaSquareM: 1000, AffectedPopulation: 10, AffectedRoadMeters: 20,
		AffectedFacilities: 1, InputReferences: []string{"https://example.test/loss-source"},
		IncludedAssets: []loss.AssetType{loss.AssetBuilding}, Status: loss.AssessmentAvailable,
		Confidence: 0.8, ConfidenceBand: "high", CalculatedAt: now.UTC(),
	}
}
