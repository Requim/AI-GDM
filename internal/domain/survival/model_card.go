package survival

import (
	"fmt"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
)

// ModelCard 描述模型用途、输入边界和科学限制。
type ModelCard struct {
	ModelVersion string   `json:"modelVersion"`
	Name         string   `json:"name"`
	Purpose      string   `json:"purpose"`
	Scope        string   `json:"scope"`
	Inputs       []string `json:"inputs"`
	Outputs      []string `json:"outputs"`
	Limitations  []string `json:"limitations"`
	Review       string   `json:"review"`
}

// Validate 校验模型卡与当前确定性评估版本和安全边界一致。
func (m ModelCard) Validate() error {
	if err := m.validateBounds(); err != nil {
		return err
	}
	if m.ModelVersion != ModelVersion || strings.TrimSpace(m.Name) == "" ||
		strings.TrimSpace(m.Purpose) == "" || strings.TrimSpace(m.Scope) == "" {
		return fmt.Errorf("%w: 生还评估模型卡身份或用途无效", domain.ErrInvalidInput)
	}
	if len(m.Inputs) == 0 || len(m.Outputs) == 0 || len(m.Limitations) == 0 || strings.TrimSpace(m.Review) == "" {
		return fmt.Errorf("%w: 生还评估模型卡安全说明不完整", domain.ErrInvalidInput)
	}
	return nil
}

func (m ModelCard) validateBounds() error {
	fields := []struct {
		name    string
		value   string
		maximum int
	}{
		{"模型卡版本", m.ModelVersion, 128}, {"模型卡名称", m.Name, maxShortTextBytes},
		{"模型卡用途", m.Purpose, maxLongTextBytes}, {"模型卡范围", m.Scope, maxLongTextBytes},
		{"模型卡复核说明", m.Review, maxLongTextBytes},
	}
	for _, field := range fields {
		if err := validateOptionalText(field.name, field.value, field.maximum); err != nil {
			return err
		}
	}
	if err := validateTextList("模型卡输入", m.Inputs, maxTextItems, maxLongTextBytes); err != nil {
		return err
	}
	if err := validateTextList("模型卡输出", m.Outputs, maxTextItems, maxLongTextBytes); err != nil {
		return err
	}
	return validateTextList("模型卡限制", m.Limitations, maxTextItems, maxLongTextBytes)
}

// DefaultModelCard 返回可直接展示给评估人员的模型卡。
func DefaultModelCard() ModelCard {
	return ModelCard{
		ModelVersion: ModelVersion,
		Name:         "AI-GDM 失联人员生还可能性辅助评估",
		Purpose:      "根据匿名化搜救场景生成宽概率区间和人工搜救优先级",
		Scope:        "仅用于历史案例回放、演示和人工决策辅助，不用于个体医疗或自动放弃搜救",
		Inputs:       []string{"失联后经过分钟数", "已知字段覆盖率", "环境与受困状态枚举信号"},
		Outputs:      []string{"确定性评分", "生还可能性宽区间", "搜救优先级", "主要因素"},
		Limitations: []string{
			"规则未经过个体层面的临床校准",
			"历史案例存在报告偏差，不能外推到具体个人",
			"风险变化、伤情和现场核验必须由专业人员确认",
		},
		Review: "任何现场行动必须由有资质的指挥员和搜救人员复核",
	}
}
