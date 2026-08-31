package lossreference

import (
	"context"
	"errors"
	"testing"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

type baselineReaderFunc func(context.Context, applicationloss.BaselineQuery) (lossdomain.BaselineSet, error)

func (f baselineReaderFunc) BaselineSet(ctx context.Context,
	query applicationloss.BaselineQuery,
) (lossdomain.BaselineSet, error) {
	return f(ctx, query)
}

func TestFallbackReaderPrefersPrimaryBaseline(t *testing.T) {
	want := lossdomain.BaselineSet{Version: "approved-v1"}
	reader := NewFallback(baselineReaderFunc(func(context.Context,
		applicationloss.BaselineQuery,
	) (lossdomain.BaselineSet, error) {
		return want, nil
	}))
	got, err := reader.BaselineSet(context.Background(), applicationloss.BaselineQuery{})
	if err != nil || got.Version != want.Version {
		t.Fatalf("正式基线读取结果错误: got=%+v err=%v", got, err)
	}
}

func TestFallbackReaderUsesReferenceOnlyForNotFound(t *testing.T) {
	reader := NewFallback(baselineReaderFunc(func(context.Context,
		applicationloss.BaselineQuery,
	) (lossdomain.BaselineSet, error) {
		return lossdomain.BaselineSet{}, domain.ErrNotFound
	}))
	got, err := reader.BaselineSet(context.Background(), mixedQuery())
	if err != nil {
		t.Fatalf("读取研究参考失败: %v", err)
	}
	if got.Costs[0].Status != lossdomain.BaselineDemoOnly {
		t.Fatalf("研究参考状态错误: %s", got.Costs[0].Status)
	}
}

func TestFallbackReaderForcesReferenceForLastSuccessData(t *testing.T) {
	primaryCalled := false
	reader := NewFallback(baselineReaderFunc(func(context.Context,
		applicationloss.BaselineQuery,
	) (lossdomain.BaselineSet, error) {
		primaryCalled = true
		return lossdomain.BaselineSet{Version: "approved-v1"}, nil
	}))
	query := mixedQuery()
	query.ReferenceOnly = true
	got, err := reader.BaselineSet(context.Background(), query)
	if err != nil {
		t.Fatalf("强制读取研究参考失败: %v", err)
	}
	if primaryCalled || got.Costs[0].Status != lossdomain.BaselineDemoOnly {
		t.Fatalf("最后成功数据未绕过正式基线: primary=%t status=%s", primaryCalled, got.Costs[0].Status)
	}
}

func TestFallbackReaderDoesNotMaskPrimaryFailure(t *testing.T) {
	want := errors.New("database unavailable")
	reader := NewFallback(baselineReaderFunc(func(context.Context,
		applicationloss.BaselineQuery,
	) (lossdomain.BaselineSet, error) {
		return lossdomain.BaselineSet{}, want
	}))
	_, err := reader.BaselineSet(context.Background(), mixedQuery())
	if !errors.Is(err, want) {
		t.Fatalf("正式基线故障被错误降级: %v", err)
	}
}
