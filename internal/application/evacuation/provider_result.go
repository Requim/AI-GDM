package evacuation

import (
	"errors"
	"fmt"
)

const (
	// MaxFacilityProviderCandidates 限制单次设施供应商结果进入逐项空间处理的数量。
	MaxFacilityProviderCandidates = 25
	// MaxRouteProviderCandidates 限制单次路线供应商结果进入克隆、校验和排序的数量。
	MaxRouteProviderCandidates = 1_000
	// MaxRouteProviderGeometryBytes 限制单条供应商路线在解析前的几何原始字节数。
	MaxRouteProviderGeometryBytes = 512 * 1024
)

// ErrUnsafeProviderResult 表示上游地图供应商返回了不满足安全处理契约的数据。
var ErrUnsafeProviderResult = errors.New("地图供应商结果不满足安全处理契约")

func validateProviderResultCount(subject string, count, maximum int) error {
	if count <= maximum {
		return nil
	}
	return fmt.Errorf("%w: %s返回 %d 条，超过 %d 条上限",
		ErrUnsafeProviderResult, subject, count, maximum)
}

func validateProviderGeometrySize(size int) error {
	if size <= MaxRouteProviderGeometryBytes {
		return nil
	}
	return fmt.Errorf("%w: 单条路线几何为 %d 字节，超过 %d 字节上限",
		ErrUnsafeProviderResult, size, MaxRouteProviderGeometryBytes)
}

func wrapUnsafeProviderResult(context string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrUnsafeProviderResult, context, err)
}
