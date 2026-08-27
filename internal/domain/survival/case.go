package survival

import (
	"fmt"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// Validate 校验历史事件的来源、时间、位置和匿名化边界。
func (e HistoricalEvent) Validate() error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.DatasetEventID) == "" {
		return fmt.Errorf("%w: 历史事件标识不能为空", domain.ErrInvalidInput)
	}
	if e.EventDate.IsZero() || !isUTC(e.EventDate) {
		return fmt.Errorf("%w: 历史事件日期必须使用 UTC", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(e.Category) == "" || strings.TrimSpace(e.Country) == "" {
		return fmt.Errorf("%w: 历史事件分类和国家不能为空", domain.ErrInvalidInput)
	}
	if e.Fatalities < 0 || e.Injuries < 0 {
		return fmt.Errorf("%w: 历史事件伤亡数不能为负数", domain.ErrInvalidInput)
	}
	if err := e.Location.Validate(); err != nil {
		return fmt.Errorf("历史事件位置: %w", err)
	}
	if strings.TrimSpace(e.LocationAccuracy) == "" {
		return fmt.Errorf("%w: 历史事件位置精度不能为空", domain.ErrInvalidInput)
	}
	if err := e.Source.Validate(); err != nil {
		return fmt.Errorf("校验历史事件来源: %w", err)
	}
	if e.Source.DataKind != provenance.DataKindHistorical {
		return fmt.Errorf("%w: 历史事件来源必须标记为 historical", domain.ErrInvalidInput)
	}
	return nil
}
