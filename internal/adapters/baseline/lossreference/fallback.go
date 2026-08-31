package lossreference

import (
	"context"
	"errors"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

var _ applicationloss.BaselineSetReader = (*FallbackReader)(nil)

// FallbackReader 仅在正式基线明确不存在时使用研究参考，不掩盖存储故障。
type FallbackReader struct {
	primary   applicationloss.BaselineSetReader
	reference applicationloss.BaselineSetReader
}

// NewFallback 创建正式基线优先、研究参考兜底的读取器。
func NewFallback(primary applicationloss.BaselineSetReader) *FallbackReader {
	return &FallbackReader{primary: primary, reference: New()}
}

// BaselineSet 返回正式基线；仅 ErrNotFound 会触发研究参考读取。
func (r *FallbackReader) BaselineSet(ctx context.Context,
	query applicationloss.BaselineQuery,
) (lossdomain.BaselineSet, error) {
	set, err := r.primary.BaselineSet(ctx, query)
	if err == nil || !errors.Is(err, domain.ErrNotFound) {
		return set, err
	}
	return r.reference.BaselineSet(ctx, query)
}
