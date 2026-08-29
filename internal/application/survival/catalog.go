package survival

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
)

var replayIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// MaxCatalogCases 是目录在应用和 HTTP 边界允许返回的最大案例数。
const MaxCatalogCases = 1000

// CaseScenarioReader 按历史事件读取唯一的合成回放场景。
type CaseScenarioReader interface {
	ScenarioForEvent(context.Context, string) (survivaldomain.Scenario, error)
}

// CaseCatalogReader 是历史案例目录所需的最小组合读取端口。
type CaseCatalogReader interface {
	ports.HistoricalEventReader
	CaseScenarioReader
}

// HistoricalCase 是列表使用的历史事件摘要，不下发完整场景输入。
type HistoricalCase struct {
	Event      survivaldomain.HistoricalEvent `json:"event"`
	ScenarioID string                         `json:"scenarioId"`
}

// Validate 校验目录摘要不携带无效事件或场景绑定。
func (c HistoricalCase) Validate() error {
	if err := c.Event.Validate(); err != nil {
		return err
	}
	if err := ValidateIdentifier(c.Event.ID); err != nil {
		return err
	}
	return ValidateIdentifier(c.ScenarioID)
}

// HistoricalCaseDetail 把公开事件与唯一合成场景绑定为可审计详情。
type HistoricalCaseDetail struct {
	Event          survivaldomain.HistoricalEvent `json:"event"`
	Scenario       survivaldomain.Scenario        `json:"scenario"`
	ScenarioDigest string                         `json:"scenarioDigest"`
	Usage          survivaldomain.ReplayUsage     `json:"usage"`
}

// Validate 校验详情中的事件、场景、摘要和用途声明保持一致。
func (d HistoricalCaseDetail) Validate() error {
	if err := d.Event.Validate(); err != nil {
		return err
	}
	if err := ValidateIdentifier(d.Event.ID); err != nil {
		return err
	}
	if err := validateScenarioBinding(d.Event.ID, d.Scenario); err != nil {
		return err
	}
	if d.Scenario.AsOf.Before(d.Event.EventDate) {
		return fmt.Errorf("%w: 历史案例场景时刻早于事件日期", domain.ErrInvalidInput)
	}
	digest, err := d.Scenario.Digest()
	if err != nil || digest != d.ScenarioDigest {
		return fmt.Errorf("%w: 历史案例场景摘要不一致", domain.ErrInvalidInput)
	}
	if d.Usage != survivaldomain.HistoricalReplayUsage() {
		return fmt.Errorf("%w: 历史案例用途声明无效", domain.ErrInvalidInput)
	}
	return nil
}

// CatalogService 是案例列表和详情驱动适配器使用的最小端口。
type CatalogService interface {
	ListCases(context.Context) ([]HistoricalCase, error)
	GetCase(context.Context, string) (HistoricalCaseDetail, error)
}

// CatalogServiceImpl 编排公开事件与合成场景的一一关联。
type CatalogServiceImpl struct {
	source CaseCatalogReader
}

var _ CatalogService = (*CatalogServiceImpl)(nil)

// NewCatalogService 创建并验证具备场景关联能力的历史案例目录用例。
func NewCatalogService(source CaseCatalogReader) (*CatalogServiceImpl, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: 历史案例目录读取器为空", domain.ErrInvalidInput)
	}
	return &CatalogServiceImpl{source: source}, nil
}

// ListCases 返回稳定排序且不包含完整场景输入的历史案例摘要。
func (s *CatalogServiceImpl) ListCases(ctx context.Context) ([]HistoricalCase, error) {
	entries, err := s.entries(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]HistoricalCase, 0, len(entries))
	for _, entry := range entries {
		result = append(result, HistoricalCase{Event: entry.event, ScenarioID: entry.scenario.ID})
	}
	return result, nil
}

// GetCase 返回公开事件、完整合成输入、摘要和固定用途声明。
func (s *CatalogServiceImpl) GetCase(ctx context.Context, id string) (HistoricalCaseDetail, error) {
	if err := ValidateIdentifier(id); err != nil {
		return HistoricalCaseDetail{}, err
	}
	entries, err := s.entries(ctx)
	if err != nil {
		return HistoricalCaseDetail{}, err
	}
	for _, entry := range entries {
		if entry.event.ID == id {
			return entry.detail(), nil
		}
	}
	return HistoricalCaseDetail{}, fmt.Errorf("%w: 历史事件 %s 不存在", domain.ErrNotFound, id)
}

type catalogEntry struct {
	event    survivaldomain.HistoricalEvent
	scenario survivaldomain.Scenario
	digest   string
}

func (s *CatalogServiceImpl) entries(ctx context.Context) ([]catalogEntry, error) {
	values, err := s.source.ListEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取历史事件目录: %w", err)
	}
	if len(values) > MaxCatalogCases {
		return nil, fmt.Errorf("%w: 历史事件目录超过 %d 条", domain.ErrInsufficientData, MaxCatalogCases)
	}
	result := make([]catalogEntry, 0, len(values))
	seenScenarios := make(map[string]string, len(values))
	for index, event := range values {
		entry, entryErr := s.entryForEvent(ctx, event, index)
		if entryErr != nil {
			return nil, entryErr
		}
		if owner, exists := seenScenarios[entry.scenario.ID]; exists {
			return nil, fmt.Errorf("%w: 场景 %s 同时关联事件 %s 和 %s",
				domain.ErrInsufficientData, entry.scenario.ID, owner, event.ID)
		}
		seenScenarios[entry.scenario.ID] = event.ID
		result = append(result, entry)
	}
	sortEntries(result)
	return result, nil
}

func (s *CatalogServiceImpl) entryForEvent(ctx context.Context, event survivaldomain.HistoricalEvent,
	index int,
) (catalogEntry, error) {
	if err := event.Validate(); err != nil {
		joined := errors.Join(domain.ErrInsufficientData, err)
		return catalogEntry{}, fmt.Errorf("校验历史事件 %d: %w", index, joined)
	}
	if err := ValidateIdentifier(event.ID); err != nil {
		joined := errors.Join(domain.ErrInsufficientData, err)
		return catalogEntry{}, fmt.Errorf("校验历史事件 %d 标识: %w", index, joined)
	}
	scenario, err := s.source.ScenarioForEvent(ctx, event.ID)
	if err != nil {
		joined := errors.Join(domain.ErrInsufficientData, err)
		return catalogEntry{}, fmt.Errorf("历史事件 %s 缺少回放场景: %w", event.ID, joined)
	}
	if err = validateScenarioBinding(event.ID, scenario); err != nil {
		return catalogEntry{}, err
	}
	if scenario.AsOf.Before(event.EventDate) {
		return catalogEntry{}, fmt.Errorf("%w: 场景 %s 时刻早于历史事件日期",
			domain.ErrInsufficientData, scenario.ID)
	}
	digest, err := scenario.Digest()
	if err != nil {
		return catalogEntry{}, fmt.Errorf("生成事件 %s 场景摘要: %w", event.ID, err)
	}
	return catalogEntry{event: event, scenario: scenario, digest: digest}, nil
}

func validateScenarioBinding(caseID string, scenario survivaldomain.Scenario) error {
	if err := scenario.Validate(); err != nil {
		joined := errors.Join(domain.ErrInsufficientData, err)
		return fmt.Errorf("事件 %s 的回放场景无效: %w", caseID, joined)
	}
	if err := ValidateIdentifier(scenario.ID); err != nil {
		joined := errors.Join(domain.ErrInsufficientData, err)
		return fmt.Errorf("事件 %s 的场景标识无效: %w", caseID, joined)
	}
	if scenario.CaseID != caseID {
		return fmt.Errorf("%w: 场景 %s 绑定了错误事件 %s",
			domain.ErrInsufficientData, scenario.ID, scenario.CaseID)
	}
	return nil
}

func sortEntries(values []catalogEntry) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].event.EventDate.Equal(values[j].event.EventDate) {
			return values[i].event.ID < values[j].event.ID
		}
		return values[i].event.EventDate.After(values[j].event.EventDate)
	})
}

func (e catalogEntry) detail() HistoricalCaseDetail {
	return HistoricalCaseDetail{
		Event: e.event, Scenario: e.scenario, ScenarioDigest: e.digest,
		Usage: survivaldomain.HistoricalReplayUsage(),
	}
}

// ValidateIdentifier 统一校验案例和场景在应用及 HTTP 边界使用的标识。
func ValidateIdentifier(value string) error {
	if value != strings.TrimSpace(value) || !replayIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%w: 回放标识无效", domain.ErrInvalidInput)
	}
	return nil
}
