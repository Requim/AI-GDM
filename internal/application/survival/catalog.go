package survival

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
)

// HistoricalCase 是面向回放的事件目录条目，包含确定的场景引用。
type HistoricalCase struct {
	Event      survivaldomain.HistoricalEvent `json:"event"`
	ScenarioID string                         `json:"scenarioId"`
}

// CatalogService 是案例列表和详情驱动适配器使用的最小端口。
type CatalogService interface {
	ListCases(context.Context) ([]HistoricalCase, error)
	GetCase(context.Context, string) (HistoricalCase, error)
}

// CatalogServiceImpl 编排匿名历史事件目录。
type CatalogServiceImpl struct {
	events ports.HistoricalEventReader
}

var _ CatalogService = (*CatalogServiceImpl)(nil)

// NewCatalogService 创建历史案例目录用例。
func NewCatalogService(events ports.HistoricalEventReader) (*CatalogServiceImpl, error) {
	if events == nil {
		return nil, fmt.Errorf("%w: 历史事件读取器为空", domain.ErrInvalidInput)
	}
	return &CatalogServiceImpl{events: events}, nil
}

// ListCases 返回稳定排序且经过领域校验的历史事件。
func (s *CatalogServiceImpl) ListCases(ctx context.Context) ([]HistoricalCase, error) {
	values, err := s.events.ListEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取历史事件目录: %w", err)
	}
	result := make([]HistoricalCase, 0, len(values))
	for index, value := range values {
		if err = value.Validate(); err != nil {
			return nil, fmt.Errorf("校验历史事件 %d: %w", index, err)
		}
		result = append(result, HistoricalCase{Event: value, ScenarioID: scenarioIDForCase(value.ID)})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Event.EventDate.Equal(result[j].Event.EventDate) {
			return result[i].Event.ID < result[j].Event.ID
		}
		return result[i].Event.EventDate.After(result[j].Event.EventDate)
	})
	return result, nil
}

// GetCase 按事件标识返回一个可回放案例。
func (s *CatalogServiceImpl) GetCase(ctx context.Context, id string) (HistoricalCase, error) {
	if err := validateID(id); err != nil {
		return HistoricalCase{}, err
	}
	values, err := s.ListCases(ctx)
	if err != nil {
		return HistoricalCase{}, err
	}
	for _, value := range values {
		if value.Event.ID == id {
			return value, nil
		}
	}
	return HistoricalCase{}, fmt.Errorf("%w: 历史事件 %s 不存在", domain.ErrNotFound, id)
}

func scenarioIDForCase(caseID string) string {
	value := strings.TrimPrefix(caseID, "case-")
	return "replay-" + value
}

func validateID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return fmt.Errorf("%w: 回放标识无效", domain.ErrInvalidInput)
	}
	return nil
}
