package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

func TestLossAssessmentStoreRoundTripsIndependentCopy(t *testing.T) {
	store := NewLossAssessmentStore()
	value := validAssessment()
	if err := store.SaveAssessment(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.InputReferences[0] = "https://mutated.test"
	value.ExpectedLowCents = ptrInt64(999)
	got, err := store.GetAssessment(context.Background(), "loss-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.InputReferences[0] != "https://example.test/source" || got.ExpectedLowCents == nil || *got.ExpectedLowCents != 10 {
		t.Fatalf("仓储返回了共享引用: %+v", got)
	}
	got.InputReferences[0] = "https://changed.test"
	again, err := store.GetAssessment(context.Background(), "loss-1")
	if err != nil || again.InputReferences[0] != "https://example.test/source" {
		t.Fatalf("读取副本未隔离: value=%+v err=%v", again, err)
	}
}

func TestLossAssessmentStoreRejectsInvalidAndMissingValues(t *testing.T) {
	store := NewLossAssessmentStore()
	if err := store.SaveAssessment(context.Background(), lossdomain.Assessment{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法评估错误 = %v", err)
	}
	if _, err := store.GetAssessment(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺失评估错误 = %v", err)
	}
	if _, err := store.GetAssessment(context.Background(), "bad id"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("非法标识错误 = %v", err)
	}
}

func TestLossAssessmentStoreRejectsConflictingSameID(t *testing.T) {
	store := NewLossAssessmentStore()
	value := validAssessment()
	if err := store.SaveAssessment(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.ConditionalHighCents++
	if err := store.SaveAssessment(context.Background(), value); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同标识不同内容未被拒绝: %v", err)
	}
}

func TestLossAssessmentStoreHonorsContextCancellation(t *testing.T) {
	store := NewLossAssessmentStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.SaveAssessment(ctx, validAssessment()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消保存错误 = %v", err)
	}
	if _, err := store.GetAssessment(ctx, "loss-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消读取错误 = %v", err)
	}
}

func validAssessment() lossdomain.Assessment {
	return lossdomain.Assessment{
		ID: "loss-1", SnapshotID: "snapshot-1", FormulaVersion: lossdomain.FormulaVersion,
		ScenarioMethod: "测试公式", HazardType: "landslide", RegionCode: "CN",
		ConditionalLowCents: 10, ConditionalMidCents: 20, ConditionalHighCents: 30,
		ExpectedLowCents: ptrInt64(10), ExpectedMidCents: ptrInt64(20), ExpectedHighCents: ptrInt64(30),
		InputReferences: []string{"https://example.test/source"}, IncludedAssets: []lossdomain.AssetType{lossdomain.AssetBuilding},
		Status: lossdomain.AssessmentAvailable, Confidence: 0.8, ConfidenceBand: "high", CalculatedAt: time.Now().UTC(),
	}
}

func ptrInt64(value int64) *int64 { return &value }
