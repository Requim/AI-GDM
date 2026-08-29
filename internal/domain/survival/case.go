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
	if err := e.validateBounds(); err != nil {
		return err
	}
	if e.EventDate.IsZero() || !isUTC(e.EventDate) {
		return fmt.Errorf("%w: 历史事件日期必须使用 UTC", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(e.Category) == "" || strings.TrimSpace(e.Country) == "" {
		return fmt.Errorf("%w: 历史事件分类和国家不能为空", domain.ErrInvalidInput)
	}
	if negativeCount(e.Fatalities) || negativeCount(e.Injuries) {
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
	if err := validateHistoricalSourceBounds(e.Source); err != nil {
		return err
	}
	if e.Source.DataKind != provenance.DataKindHistorical {
		return fmt.Errorf("%w: 历史事件来源必须标记为 historical", domain.ErrInvalidInput)
	}
	return nil
}

func (e HistoricalEvent) validateBounds() error {
	fields := []struct {
		name    string
		value   string
		maximum int
	}{
		{"历史事件标识", e.ID, maxIdentifierBytes}, {"历史数据集事件标识", e.DatasetEventID, maxIdentifierBytes},
		{"历史事件时间精度", e.TimePrecision, 32}, {"历史事件分类", e.Category, 64},
		{"历史事件触发因素", e.Trigger, maxShortTextBytes}, {"历史事件规模", e.Size, 128},
		{"历史事件位置精度", e.LocationAccuracy, 128}, {"历史事件国家", e.Country, 128},
		{"历史事件行政区", e.AdminArea, maxShortTextBytes},
	}
	for _, field := range fields {
		if err := validateOptionalText(field.name, field.value, field.maximum); err != nil {
			return err
		}
	}
	return validateTextList("历史事件限制", e.Limitations, maxTextItems, maxLongTextBytes)
}

func validateHistoricalSourceBounds(value provenance.Provenance) error {
	fields := []struct {
		name    string
		value   string
		maximum int
	}{
		{"历史来源供应商", value.Provider, maxShortTextBytes}, {"历史来源数据集", value.Dataset, maxShortTextBytes},
		{"历史来源版本", value.DatasetVersion, 128}, {"历史来源修订", value.SourceRevision, maxShortTextBytes},
		{"历史来源地址", value.SourceURI, 2048}, {"历史来源引文", value.Citation, maxLongTextBytes},
		{"历史来源许可", value.License, maxShortTextBytes}, {"历史来源空间分辨率", value.SpatialResolution, maxShortTextBytes},
		{"历史来源时间分辨率", value.TemporalResolution, 128}, {"历史来源坐标系", value.CRS, 64},
		{"历史来源摘要", value.SHA256, 128}, {"历史来源转换版本", value.TransformVersion, 128},
		{"历史来源请求标识", value.ProviderRequestID, maxShortTextBytes}, {"历史来源模型", value.Model, 128},
	}
	for _, field := range fields {
		if err := validateOptionalText(field.name, field.value, field.maximum); err != nil {
			return err
		}
	}
	if err := validateTextList("历史来源质量标记", value.QualityFlags, maxTextItems, 512); err != nil {
		return err
	}
	if err := validateTextList("历史来源限制", value.Limitations, maxTextItems, maxLongTextBytes); err != nil {
		return err
	}
	return validateHistoricalSourceParts(value.SourceParts)
}

func validateHistoricalSourceParts(values []provenance.SourcePart) error {
	if len(values) > maxTextItems {
		return fmt.Errorf("%w: 历史来源分片超过 %d 项", domain.ErrInvalidInput, maxTextItems)
	}
	for index, value := range values {
		if err := validateRequiredText(fmt.Sprintf("历史来源分片 %d 引用", index+1), value.Reference, 2048); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("历史来源分片 %d 修订", index+1), value.Revision, maxShortTextBytes); err != nil {
			return err
		}
		if err := validateOptionalText(fmt.Sprintf("历史来源分片 %d 摘要", index+1), value.SHA256, 128); err != nil {
			return err
		}
	}
	return nil
}

func negativeCount(value *int) bool { return value != nil && *value < 0 }
