package spatialanalysis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Service 编排一次确定性风险区域空间分析。
type Service struct {
	executor ports.SpatialAnalysisExecutor
	clock    ports.Clock
}

// New 创建空间分析应用服务。
func New(executor ports.SpatialAnalysisExecutor, clock ports.Clock) (*Service, error) {
	if executor == nil || clock == nil {
		return nil, fmt.Errorf("%w: 空间分析执行器或时钟为空", domain.ErrInvalidInput)
	}
	return &Service{executor: executor, clock: clock}, nil
}

// Analyze 对指定完整风险快照执行空间分析。
func (s *Service) Analyze(ctx context.Context, snapshotID string) (spatialdomain.Analysis, error) {
	if snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) {
		return spatialdomain.Analysis{}, fmt.Errorf("%w: 灾害快照标识无效", domain.ErrInvalidInput)
	}
	calculatedAt := s.clock.Now()
	if calculatedAt.IsZero() || !isUTC(calculatedAt) {
		return spatialdomain.Analysis{}, fmt.Errorf("%w: 空间分析时间必须是 UTC", domain.ErrInvalidInput)
	}
	value, err := s.executor.Execute(ctx, snapshotID, calculatedAt)
	if err != nil {
		return spatialdomain.Analysis{}, fmt.Errorf("执行快照 %s 空间分析: %w", snapshotID, err)
	}
	if value.SnapshotID != snapshotID {
		return spatialdomain.Analysis{}, fmt.Errorf("%w: 空间分析返回了其他快照", domain.ErrInvalidInput)
	}
	if err = value.Validate(); err != nil {
		return spatialdomain.Analysis{}, fmt.Errorf("校验空间分析结果: %w", err)
	}
	return value, nil
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
