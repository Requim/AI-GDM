package hazard

import (
	"context"
	"fmt"
	"reflect"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

// RefreshFunc 把现有采集用例适配为单一灾种刷新端口。
type RefreshFunc func(context.Context) (hazarddomain.Snapshot, []hazarddomain.RiskZone, error)

// Refresh 调用被适配的采集用例。
func (f RefreshFunc) Refresh(ctx context.Context) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	return f(ctx)
}

// HazardProvider 将一个灾种绑定到独立的刷新器和确定性研判器。
type HazardProvider struct {
	hazardType  hazarddomain.Type
	refresher   ports.HazardRefresher
	evaluator   ports.RiskEvaluator
	refreshGate chan struct{}
}

// NewHazardProvider 创建可注册的灾种能力。
func NewHazardProvider(hazardType hazarddomain.Type, refresher ports.HazardRefresher,
	evaluator ports.RiskEvaluator,
) (*HazardProvider, error) {
	if err := validateHazardType(hazardType); err != nil {
		return nil, err
	}
	if nilDependency(refresher) || nilDependency(evaluator) {
		return nil, fmt.Errorf("%w: 灾种刷新器或研判器为空", domain.ErrInvalidInput)
	}
	return &HazardProvider{
		hazardType:  hazardType,
		refresher:   refresher,
		evaluator:   evaluator,
		refreshGate: make(chan struct{}, 1),
	}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) &&
		reflect.ValueOf(value).IsNil()
}

// Type 返回该能力唯一负责的灾种。
func (p *HazardProvider) Type() hazarddomain.Type {
	return p.hazardType
}

// Refresh 串行执行刷新，避免定时任务和人工请求并发写入同一灾种。
func (p *HazardProvider) Refresh(ctx context.Context) (
	hazarddomain.Snapshot, []hazarddomain.RiskZone, error,
) {
	if err := ctx.Err(); err != nil {
		return hazarddomain.Snapshot{}, nil, fmt.Errorf("刷新灾种 %s 前请求已取消: %w", p.hazardType, err)
	}
	select {
	case p.refreshGate <- struct{}{}:
		defer func() { <-p.refreshGate }()
	case <-ctx.Done():
		return hazarddomain.Snapshot{}, nil, fmt.Errorf("等待灾种 %s 刷新锁: %w", p.hazardType, ctx.Err())
	}
	return p.refresher.Refresh(ctx)
}

func (p *HazardProvider) evaluate(input risk.Input) (risk.Assessment, error) {
	return p.evaluator.Evaluate(input)
}
