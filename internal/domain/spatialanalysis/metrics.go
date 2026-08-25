package spatialanalysis

import "math"

type exposureMetric struct {
	status          MetricStatus
	quantity        *float64
	unit            string
	coverage        *float64
	inputReferences []string
	limitations     []string
}

// Validate 校验人口暴露量的状态、数值、单位和覆盖率。
func (m PopulationExposureMetric) Validate() error {
	return validateExposure("人口", PopulationUnit, populationValues(m))
}

// Validate 校验道路暴露量的状态、数值、单位和覆盖率。
func (m RoadExposureMetric) Validate() error {
	return validateExposure("道路", RoadUnit, roadValues(m))
}

// Validate 校验 POI 暴露量的状态、数值、单位和覆盖率。
func (m POIExposureMetric) Validate() error {
	return validateExposure("POI", POIUnit, poiValues(m))
}

// Validate 校验行政匹配状态、代码、覆盖率和输入引用。
func (m AdministrativeMatch) Validate() error {
	return validateAdministration(m)
}

func populationValues(value PopulationExposureMetric) exposureMetric {
	return exposureMetric{
		status: value.Status, quantity: value.Quantity, unit: value.Unit,
		coverage: value.CoverageRatio, inputReferences: value.InputReferences,
		limitations: value.Limitations,
	}
}

func roadValues(value RoadExposureMetric) exposureMetric {
	return exposureMetric{
		status: value.Status, quantity: value.Quantity, unit: value.Unit,
		coverage: value.CoverageRatio, inputReferences: value.InputReferences,
		limitations: value.Limitations,
	}
}

func poiValues(value POIExposureMetric) exposureMetric {
	return exposureMetric{
		status: value.Status, quantity: value.Quantity, unit: value.Unit,
		coverage: value.CoverageRatio, inputReferences: value.InputReferences,
		limitations: value.Limitations,
	}
}

func validateExposure(name, unit string, value exposureMetric) error {
	if value.unit != unit {
		return invalid("%s暴露单位必须是 %s", name, unit)
	}
	if err := validateStringList(name+"暴露输入引用", value.inputReferences, false); err != nil {
		return err
	}
	if err := validateStringList(name+"暴露限制", value.limitations, false); err != nil {
		return err
	}
	switch value.status {
	case MetricAvailable:
		return validateAvailableExposure(name, value)
	case MetricPartial:
		return validatePartialExposure(name, value)
	case MetricUnavailable:
		return validateUnavailableExposure(name, value)
	default:
		return invalid("%s暴露状态无效", name)
	}
}

func validateAvailableExposure(name string, value exposureMetric) error {
	if err := validatePresentExposure(name, value); err != nil {
		return err
	}
	if math.Abs(*value.coverage-1) > 1e-9 {
		return invalid("%s完整暴露覆盖率必须为一", name)
	}
	return nil
}

func validatePartialExposure(name string, value exposureMetric) error {
	if err := validatePresentExposure(name, value); err != nil {
		return err
	}
	if *value.coverage <= 0 || *value.coverage >= 1 {
		return invalid("%s部分暴露覆盖率必须位于零到一之间", name)
	}
	if len(value.limitations) == 0 {
		return invalid("%s部分暴露必须说明限制", name)
	}
	return nil
}

func validatePresentExposure(name string, value exposureMetric) error {
	if value.quantity == nil || value.coverage == nil {
		return invalid("%s可计算暴露必须提供数量和覆盖率", name)
	}
	if !finite(*value.quantity) || *value.quantity < 0 {
		return invalid("%s暴露数量必须是有限非负数", name)
	}
	if !finite(*value.coverage) || *value.coverage < 0 || *value.coverage > 1 {
		return invalid("%s暴露覆盖率无效", name)
	}
	if len(value.inputReferences) == 0 {
		return invalid("%s可计算暴露必须包含输入引用", name)
	}
	return nil
}

func validateUnavailableExposure(name string, value exposureMetric) error {
	if value.quantity != nil || value.coverage != nil {
		return invalid("%s不可用暴露的数量和覆盖率必须为空", name)
	}
	if len(value.limitations) == 0 {
		return invalid("%s不可用暴露必须说明缺失原因", name)
	}
	return nil
}

func validateAdministration(value AdministrativeMatch) error {
	if err := validateStringList("行政代码", value.AdminCodes, false); err != nil {
		return err
	}
	if err := validateStringList("行政匹配输入引用", value.InputReferences, false); err != nil {
		return err
	}
	if err := validateStringList("行政匹配限制", value.Limitations, false); err != nil {
		return err
	}
	switch value.Status {
	case AdminMatchAvailable:
		return validateAvailableAdministration(value)
	case AdminMatchPartial:
		return validatePartialAdministration(value)
	case AdminMatchUnavailable:
		return validateUnavailableAdministration(value)
	default:
		return invalid("行政匹配状态无效")
	}
}

func validateAvailableAdministration(value AdministrativeMatch) error {
	if err := validateAdministrativeCoverage(value, true); err != nil {
		return err
	}
	if math.Abs(*value.CoverageRatio-1) > 1e-9 {
		return invalid("完整行政匹配覆盖率必须为一")
	}
	return nil
}

func validatePartialAdministration(value AdministrativeMatch) error {
	if err := validateAdministrativeCoverage(value, true); err != nil {
		return err
	}
	if *value.CoverageRatio <= 0 || *value.CoverageRatio >= 1 {
		return invalid("部分行政匹配覆盖率必须位于零到一之间")
	}
	if len(value.Limitations) == 0 {
		return invalid("部分行政匹配必须说明限制")
	}
	return nil
}

func validateAdministrativeCoverage(value AdministrativeMatch, requireReference bool) error {
	if value.CoverageRatio == nil || !finite(*value.CoverageRatio) {
		return invalid("行政匹配覆盖率无效")
	}
	if *value.CoverageRatio < 0 || *value.CoverageRatio > 1 {
		return invalid("行政匹配覆盖率超出零到一范围")
	}
	if requireReference && len(value.InputReferences) == 0 {
		return invalid("可用行政匹配必须包含输入引用")
	}
	return nil
}

func validateUnavailableAdministration(value AdministrativeMatch) error {
	if value.CoverageRatio != nil || len(value.AdminCodes) != 0 {
		return invalid("不可用行政匹配不能包含覆盖率或行政代码")
	}
	if len(value.Limitations) == 0 {
		return invalid("不可用行政匹配必须说明缺失原因")
	}
	return nil
}
