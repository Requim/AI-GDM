package memory

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// LossAssessmentStore 保存经过领域校验的损失评估结果。
// 它实现与数据库无关的仓储端口，适合开发、演示和 HTTP 契约测试。
type LossAssessmentStore struct {
	mu     sync.RWMutex
	values map[string]lossdomain.Assessment
}

var _ ports.LossAssessmentWriter = (*LossAssessmentStore)(nil)
var _ ports.LossAssessmentReader = (*LossAssessmentStore)(nil)

// NewLossAssessmentStore 创建空的损失评估内存仓储。
func NewLossAssessmentStore() *LossAssessmentStore {
	return &LossAssessmentStore{values: make(map[string]lossdomain.Assessment)}
}

// SaveAssessment 校验并覆盖同一评估标识的结果。
func (s *LossAssessmentStore) SaveAssessment(ctx context.Context, value lossdomain.Assessment) error {
	if err := contextError(ctx, "保存损失评估"); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("%w: 损失评估内存仓储为空", domain.ErrInvalidInput)
	}
	if err := value.Validate(); err != nil {
		return fmt.Errorf("校验待保存损失评估: %w", err)
	}
	if err := validateID(value.ID); err != nil {
		return err
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = make(map[string]lossdomain.Assessment)
	}
	if existing, ok := s.values[value.ID]; ok {
		if !reflect.DeepEqual(existing, value) {
			s.mu.Unlock()
			return fmt.Errorf("%w: 损失评估标识 %s 已绑定其他内容", domain.ErrInvalidInput, value.ID)
		}
		s.mu.Unlock()
		return nil
	}
	s.values[value.ID] = cloneAssessment(value)
	s.mu.Unlock()
	return nil
}

// GetAssessment 按标识读取评估，并返回独立副本避免调用方修改仓储状态。
func (s *LossAssessmentStore) GetAssessment(ctx context.Context, id string) (lossdomain.Assessment, error) {
	if err := contextError(ctx, "读取损失评估"); err != nil {
		return lossdomain.Assessment{}, err
	}
	if s == nil {
		return lossdomain.Assessment{}, fmt.Errorf("%w: 损失评估内存仓储为空", domain.ErrInvalidInput)
	}
	if err := validateID(id); err != nil {
		return lossdomain.Assessment{}, err
	}
	s.mu.RLock()
	value, ok := s.values[id]
	s.mu.RUnlock()
	if !ok {
		return lossdomain.Assessment{}, fmt.Errorf("%w: 损失评估 %s 不存在", domain.ErrNotFound, id)
	}
	return cloneAssessment(value), nil
}

func contextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%w: %s上下文为空", domain.ErrInvalidInput, operation)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func validateID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 ||
		strings.IndexFunc(value, unicode.IsSpace) >= 0 || !validIDChars(value) {
		return fmt.Errorf("%w: 损失评估标识无效", domain.ErrInvalidInput)
	}
	return nil
}

func validIDChars(value string) bool {
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func cloneAssessment(value lossdomain.Assessment) lossdomain.Assessment {
	value.InputReferences = append([]string(nil), value.InputReferences...)
	value.IncludedAssets = append([]lossdomain.AssetType(nil), value.IncludedAssets...)
	value.ExcludedLosses = append([]string(nil), value.ExcludedLosses...)
	value.Limitations = append([]string(nil), value.Limitations...)
	value.ExpectedLowCents = cloneInt64(value.ExpectedLowCents)
	value.ExpectedMidCents = cloneInt64(value.ExpectedMidCents)
	value.ExpectedHighCents = cloneInt64(value.ExpectedHighCents)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
