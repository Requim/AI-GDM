package survival

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

// DefaultModelCard 返回可直接展示给评估人员的模型卡。
func DefaultModelCard() ModelCard {
	return ModelCard{
		ModelVersion: ModelVersion,
		Name:         "AI-GDM 失联人员生还可能性辅助评估",
		Purpose:      "根据匿名化搜救场景生成宽概率区间和人工搜救优先级",
		Scope:        "仅用于历史案例回放、演示和人工决策辅助，不用于个体医疗或自动放弃搜救",
		Inputs:       []string{"失联后经过分钟数", "输入完整度", "环境与受困状态键值"},
		Outputs:      []string{"确定性评分", "生还可能性宽区间", "搜救优先级", "主要因素"},
		Limitations: []string{
			"规则未经过个体层面的临床校准",
			"历史案例存在报告偏差，不能外推到具体个人",
			"风险变化、伤情和现场核验必须由专业人员确认",
		},
		Review: "任何现场行动必须由有资质的指挥员和搜救人员复核",
	}
}
