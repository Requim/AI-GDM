package risk

import (
	"sort"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func deriveConfidence(snapshot hazard.Snapshot, status DataStatus) Confidence {
	if status == DataFallback {
		return Confidence{
			Level: ConfidenceLow, Scope: "primary_input_quality", Semantics: ConfidenceSemantics,
			Reasons: []string{"实时刷新失败，当前结果来自未超过有效期的最后成功完整分析"},
		}
	}
	reasons := make([]string, 0, 3)
	if snapshot.Source.ObservedAt.IsZero() && snapshot.Source.PublishedAt.IsZero() {
		reasons = append(reasons, "供应商未提供可信的模型观测或发布时间")
	}
	if snapshot.Source.SourceRevision == "" || snapshot.Source.SHA256 == "" {
		reasons = append(reasons, "主模型来源缺少修订标识或内容校验和")
	}
	if len(snapshot.Source.QualityFlags) > 0 {
		reasons = append(reasons, "主模型来源包含质量标志")
	}
	if len(reasons) > 0 {
		return Confidence{
			Level: ConfidenceMedium, Scope: "primary_input_quality",
			Semantics: ConfidenceSemantics, Reasons: reasons,
		}
	}
	return Confidence{
		Level: ConfidenceHigh, Scope: "primary_input_quality", Semantics: ConfidenceSemantics,
		Reasons: []string{"主模型数据处于有效期内，并具有可信时间、来源修订和内容校验和"},
	}
}

func confidenceUnavailable(reason string) Confidence {
	return Confidence{
		Level: ConfidenceUnavailable, Scope: "primary_input_quality",
		Semantics: ConfidenceSemantics, Reasons: []string{reason},
	}
}

func expiredPrimaryFactor(snapshot hazard.Snapshot) Factor {
	values := []string{
		snapshot.RasterReference, snapshot.Source.SourceURI,
		snapshot.Source.SourceRevision, snapshot.Source.SHA256,
	}
	return Factor{
		Code: "primary_data_expired", Role: FactorDataQuality, AffectsLevel: false,
		DataStatus: DataExpired, Description: "主模型数据已超过有效期，未生成可操作风险等级",
		InputReferences: compactReferences(values...),
	}
}

func collectInputReferences(input Input) []string {
	values := []string{
		input.Snapshot.RasterReference, input.Snapshot.Source.SourceURI,
		input.Snapshot.Source.SourceRevision, input.Snapshot.Source.SHA256,
	}
	for _, zone := range input.Zones {
		values = append(values, zone.ID)
		values = append(values, zone.InputReferences...)
	}
	for _, context := range input.WeatherContexts {
		values = append(values, weatherReferences(context.Snapshot)...)
	}
	return compactReferences(values...)
}

func weatherReferences(snapshot hazard.WeatherSnapshot) []string {
	return compactReferences(snapshot.Source.SourceURI, snapshot.Source.SourceRevision,
		snapshot.Source.SHA256)
}

func compactReferences(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func assessmentLimitations(input Input, expired bool, additional ...string) []string {
	values := []string{
		"辅助研判结果，不是中国官方预警",
		"风险等级使用 AI-GDM 派生规则，不是 NASA 或中国官方风险分级",
		"数据质量置信度不等同于灾害发生概率",
	}
	values = append(values, input.Snapshot.Limitations...)
	values = append(values, input.Snapshot.Source.Limitations...)
	for _, context := range input.WeatherContexts {
		values = append(values, context.Snapshot.Source.Limitations...)
	}
	if len(input.WeatherContexts) == 0 {
		values = append(values, "未提供由调用方完成空间选择的气象上下文")
	} else {
		values = append(values, "气象上下文只用于解释，不参与等级或主模型数据置信度计算")
	}
	if expired {
		values = append(values, "主模型数据已过期，结果不包含可操作风险等级")
	}
	values = append(values, additional...)
	return compactReferences(values...)
}
