package spatialanalysis

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

// Validate 校验空间分析的完整性、规范顺序和确定性标识。
func (a Analysis) Validate() error {
	if err := validateAnalysisContent(a); err != nil {
		return err
	}
	if strings.TrimSpace(a.ID) == "" {
		return invalid("空间分析标识为空")
	}
	expected, err := analysisID(a)
	if err != nil {
		return err
	}
	if a.ID != expected {
		return invalid("空间分析标识与规范化结果不一致")
	}
	return nil
}

func validateAnalysisContent(value Analysis) error {
	if value.SnapshotID == "" || strings.TrimSpace(value.SnapshotID) != value.SnapshotID {
		return invalid("空间分析快照标识无效")
	}
	if value.Version != AnalysisVersion {
		return invalid("空间分析版本无效")
	}
	if !validUTC(value.CalculatedAt) {
		return invalid("空间分析时间必须是 UTC")
	}
	if err := validateArea(value.Area, value.Zones); err != nil {
		return err
	}
	if err := validateZones(value.Zones); err != nil {
		return err
	}
	if value.Status != deriveAnalysisStatus(value.Zones) {
		return invalid("空间分析整体状态与分项状态不一致")
	}
	return validateAnalysisMetadata(value)
}

func validateAnalysisMetadata(value Analysis) error {
	if err := validateStringList("空间分析输入引用", value.InputReferences, true); err != nil {
		return err
	}
	if err := validateStringList("空间分析数据集引用", value.DatasetReferences, false); err != nil {
		return err
	}
	if hasDatasetBackedResult(value.Zones) && len(value.DatasetReferences) == 0 {
		return invalid("可用或部分空间数据必须包含数据集引用")
	}
	if err := validateStringList("空间分析限制", value.Limitations, false); err != nil {
		return err
	}
	if len(value.Zones) == 0 && len(value.Limitations) == 0 {
		return invalid("零风险区的已完成空间分析必须说明空分析语义")
	}
	if !sameStrings(value.InputReferences, collectReferences(value)) {
		return invalid("空间分析输入引用未完整包含分项来源")
	}
	return nil
}

func validateArea(value AreaCalculation, zones []ZoneResult) error {
	if value.Method != AreaMethod {
		return invalid("面积计算方法无效")
	}
	if !finite(value.TotalSquareMeters) || value.TotalSquareMeters < 0 {
		return invalid("总体面积不是有效非负数")
	}
	if err := validateStringList("总体面积输入引用", value.InputReferences, true); err != nil {
		return err
	}
	return validateAreaRelation(value.TotalSquareMeters, zones)
}

func validateAreaRelation(total float64, zones []ZoneResult) error {
	if len(zones) == 0 {
		if total != 0 {
			return invalid("零风险区的总体面积必须为零")
		}
		return nil
	}
	var sum, maximum float64
	for _, zone := range zones {
		sum += zone.Area.SquareMeters
		maximum = math.Max(maximum, zone.Area.SquareMeters)
	}
	if !finite(sum) {
		return invalid("风险区面积之和不是有限数值")
	}
	tolerance := math.Max(1e-6, sum*1e-9)
	if total <= 0 || total > sum+tolerance || total+tolerance < maximum {
		return invalid("合并面积与风险区面积之和不一致")
	}
	return nil
}

func hasDatasetBackedResult(zones []ZoneResult) bool {
	for _, zone := range zones {
		if zone.Population.Status != MetricUnavailable || zone.Roads.Status != MetricUnavailable ||
			zone.POIs.Status != MetricUnavailable || zone.Administration.Status != AdminMatchUnavailable {
			return true
		}
	}
	return false
}

func validateZones(values []ZoneResult) error {
	previous := ""
	for index, value := range values {
		if value.ZoneID == "" || strings.TrimSpace(value.ZoneID) != value.ZoneID {
			return invalid("第 %d 个风险区标识无效", index)
		}
		if previous != "" && value.ZoneID <= previous {
			return invalid("风险区必须按标识排序且不能重复")
		}
		if err := validateZone(value); err != nil {
			return fmt.Errorf("风险区 %s: %w", value.ZoneID, err)
		}
		previous = value.ZoneID
	}
	return nil
}

func validateZone(value ZoneResult) error {
	if !finite(value.Area.SquareMeters) || value.Area.SquareMeters <= 0 {
		return invalid("风险区面积必须是有限正数")
	}
	if err := validateStringList("面积输入引用", value.Area.InputReferences, true); err != nil {
		return err
	}
	if err := validateZoneMetrics(value); err != nil {
		return err
	}
	return validateStringList("风险区限制", value.Limitations, false)
}

func validateZoneMetrics(value ZoneResult) error {
	if err := value.Population.Validate(); err != nil {
		return err
	}
	if err := value.Roads.Validate(); err != nil {
		return err
	}
	if err := value.POIs.Validate(); err != nil {
		return err
	}
	return value.Administration.Validate()
}

func validateStringList(name string, values []string, required bool) error {
	if required && len(values) == 0 {
		return invalid("%s不能为空", name)
	}
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return invalid("%s包含空白项", name)
		}
		if index > 0 && value <= values[index-1] {
			return invalid("%s必须排序且不能重复", name)
		}
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validUTC(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func invalid(format string, values ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidInput, fmt.Sprintf(format, values...))
}
