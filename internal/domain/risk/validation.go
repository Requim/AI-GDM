package risk

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

const fallbackQualityFlag = "fallback_last_success"

func validateCapability(value ModelCapability) error {
	if value.HazardType != hazard.TypeLandslide && value.HazardType != hazard.TypeDebrisFlow {
		return fmt.Errorf("%w: 风险引擎不支持灾种 %q", domain.ErrInvalidInput, value.HazardType)
	}
	if strings.TrimSpace(value.ModelName) == "" || strings.TrimSpace(value.Dataset) == "" {
		return fmt.Errorf("%w: 风险引擎模型能力不完整", domain.ErrInvalidInput)
	}
	return nil
}

func validateInput(input Input, capability ModelCapability) error {
	if input.Snapshot.HazardType != capability.HazardType ||
		input.Snapshot.ModelName != capability.ModelName ||
		input.Snapshot.Source.Dataset != capability.Dataset {
		return fmt.Errorf("%w: 风险快照与引擎模型能力不一致", domain.ErrInvalidInput)
	}
	if input.EvaluatedAt.IsZero() || !isUTC(input.EvaluatedAt) {
		return fmt.Errorf("%w: 风险评估时间必须是 UTC", domain.ErrInvalidInput)
	}
	if err := validateSnapshot(input.Snapshot, input.EvaluatedAt); err != nil {
		return err
	}
	if err := validateZones(input.Snapshot, input.Zones); err != nil {
		return err
	}
	return validateWeatherContexts(input.WeatherContexts, input.EvaluatedAt)
}

func validateSnapshot(snapshot hazard.Snapshot, evaluatedAt time.Time) error {
	if snapshot.ID == "" || snapshot.ModelName == "" || snapshot.ModelVersion == "" ||
		snapshot.RasterReference == "" || snapshot.ProbabilitySemantics == "" {
		return fmt.Errorf("%w: 风险快照标识、模型或概率语义不完整", domain.ErrInvalidInput)
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return fmt.Errorf("%w: 风险快照状态 %q 不可评估", domain.ErrInsufficientData, snapshot.Status)
	}
	if snapshot.RunAt.IsZero() || snapshot.ValidFrom.IsZero() || snapshot.ValidTo.IsZero() ||
		!isUTC(snapshot.RunAt) || !isUTC(snapshot.ValidFrom) || !isUTC(snapshot.ValidTo) ||
		snapshot.ValidTo.Before(snapshot.ValidFrom) {
		return fmt.Errorf("%w: 风险快照时间窗口无效", domain.ErrInvalidInput)
	}
	if evaluatedAt.Before(snapshot.ValidFrom) {
		return fmt.Errorf("%w: 风险快照尚未进入有效期", domain.ErrInsufficientData)
	}
	if err := snapshot.Source.Validate(); err != nil {
		return fmt.Errorf("校验风险快照来源: %w", err)
	}
	if snapshot.Source.DataKind != provenance.DataKindNowcast {
		return fmt.Errorf("%w: 风险快照必须来自近实时模型结果", domain.ErrInvalidInput)
	}
	if !snapshot.Source.ValidFrom.IsZero() && evaluatedAt.Before(snapshot.Source.ValidFrom) {
		return fmt.Errorf("%w: 风险快照来源尚未进入有效期", domain.ErrInsufficientData)
	}
	if snapshot.Source.SHA256 != "" && !validSHA256(snapshot.Source.SHA256) {
		return fmt.Errorf("%w: 风险快照来源校验和无效", domain.ErrInvalidInput)
	}
	if err := validateThresholdScheme(snapshot.Thresholds); err != nil {
		return err
	}
	return validateFallbackMarkers(snapshot, evaluatedAt)
}

func validateThresholdScheme(values []hazard.RiskThreshold) error {
	if err := hazard.ValidateThresholds(values); err != nil {
		return err
	}
	expected := []hazard.RiskThreshold{
		{Level: hazard.RiskLow, Minimum: 0, Maximum: 0.1},
		{Level: hazard.RiskModerate, Minimum: 0.1, Maximum: 0.5},
		{Level: hazard.RiskHigh, Minimum: 0.5, Maximum: 0.9},
		{Level: hazard.RiskVeryHigh, Minimum: 0.9, Maximum: 1},
	}
	if len(values) != len(expected) {
		return fmt.Errorf("%w: v1 风险规则必须包含四档阈值", domain.ErrInvalidInput)
	}
	for index, value := range values {
		want := expected[index]
		if value.Level != want.Level || value.Minimum != want.Minimum ||
			value.Maximum != want.Maximum || strings.TrimSpace(value.Description) == "" {
			return fmt.Errorf("%w: 第 %d 个阈值不符合 v1 固定规则", domain.ErrInvalidInput, index)
		}
	}
	return nil
}

func validateFallbackMarkers(snapshot hazard.Snapshot, evaluatedAt time.Time) error {
	if evaluatedAt.After(snapshot.ValidTo) ||
		(!snapshot.Source.ValidTo.IsZero() && evaluatedAt.After(snapshot.Source.ValidTo)) {
		return nil
	}
	markers := []bool{
		snapshot.Status == hazard.SnapshotStale,
		snapshot.Source.Stale,
		hasQualityFlag(snapshot.Source.QualityFlags, fallbackQualityFlag),
	}
	if (markers[0] || markers[1] || markers[2]) && !(markers[0] && markers[1] && markers[2]) {
		return fmt.Errorf("%w: 最后成功数据回退标记不一致", domain.ErrInvalidInput)
	}
	return nil
}

func validateZones(snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	seen := make(map[string]struct{}, len(zones))
	for index, zone := range zones {
		if _, exists := seen[zone.ID]; exists {
			return fmt.Errorf("%w: 风险区标识重复", domain.ErrInvalidInput)
		}
		if err := validateZone(snapshot, zone); err != nil {
			return fmt.Errorf("风险区 %d: %w", index, err)
		}
		seen[zone.ID] = struct{}{}
	}
	return nil
}

func validateZone(snapshot hazard.Snapshot, zone hazard.RiskZone) error {
	if strings.TrimSpace(zone.ID) == "" || zone.SnapshotID != snapshot.ID ||
		len(compactReferences(zone.InputReferences...)) == 0 {
		return fmt.Errorf("%w: 风险区标识、快照引用或输入引用无效", domain.ErrInvalidInput)
	}
	if err := zone.Geometry.Validate(); err != nil {
		return err
	}
	values := []float64{zone.Minimum, zone.Mean, zone.Maximum}
	if !finite(values[0]) || !finite(values[1]) || !finite(values[2]) ||
		values[0] < 0 || values[0] > values[1] || values[1] > values[2] || values[2] > 1 {
		return fmt.Errorf("%w: 风险区概率统计无效", domain.ErrInvalidInput)
	}
	for _, value := range values {
		level, ok := classifyProbability(snapshot.Thresholds, value)
		if !ok || level != zone.Level {
			return fmt.Errorf("%w: 风险区概率统计跨等级或与等级不一致", domain.ErrInvalidInput)
		}
	}
	return nil
}

func classifyProbability(values []hazard.RiskThreshold, probability float64) (hazard.RiskLevel, bool) {
	for index, value := range values {
		inside := probability <= value.Maximum
		if index == 0 {
			inside = inside && probability >= value.Minimum
		} else {
			inside = inside && probability > value.Minimum
		}
		if inside {
			return value.Level, true
		}
	}
	return "", false
}

func riskLevelRank(value hazard.RiskLevel) int {
	switch value {
	case hazard.RiskLow:
		return 1
	case hazard.RiskModerate:
		return 2
	case hazard.RiskHigh:
		return 3
	case hazard.RiskVeryHigh:
		return 4
	default:
		return 0
	}
}

func deriveDataStatus(snapshot hazard.Snapshot, evaluatedAt time.Time) DataStatus {
	if evaluatedAt.After(snapshot.ValidTo) ||
		(!snapshot.Source.ValidTo.IsZero() && evaluatedAt.After(snapshot.Source.ValidTo)) {
		return DataExpired
	}
	if snapshot.Status == hazard.SnapshotStale || snapshot.Source.Stale ||
		hasQualityFlag(snapshot.Source.QualityFlags, fallbackQualityFlag) {
		return DataFallback
	}
	return DataCurrent
}

func hasQualityFlag(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
