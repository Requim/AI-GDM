package spatialanalysis

import (
	"math"
	"sort"
	"strings"
)

func normalizeAnalysis(value Analysis) Analysis {
	value.SnapshotID = strings.TrimSpace(value.SnapshotID)
	value.Version = strings.TrimSpace(value.Version)
	value.Area = normalizeArea(value.Area)
	value.Zones = normalizeZones(value.Zones)
	value.InputReferences = collectReferences(value)
	value.DatasetReferences = normalizeStrings(value.DatasetReferences)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func normalizeArea(value AreaCalculation) AreaCalculation {
	value.Method = strings.TrimSpace(value.Method)
	if value.TotalSquareMeters == 0 {
		value.TotalSquareMeters = 0
	}
	value.InputReferences = normalizeStrings(value.InputReferences)
	return value
}

func normalizeZones(values []ZoneResult) []ZoneResult {
	result := make([]ZoneResult, len(values))
	for index, value := range values {
		result[index] = normalizeZone(value)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ZoneID < result[right].ZoneID
	})
	return result
}

func normalizeZone(value ZoneResult) ZoneResult {
	value.ZoneID = strings.TrimSpace(value.ZoneID)
	value.Area.InputReferences = normalizeStrings(value.Area.InputReferences)
	value.Population = normalizePopulation(value.Population)
	value.Roads = normalizeRoads(value.Roads)
	value.POIs = normalizePOIs(value.POIs)
	value.Administration = normalizeAdministration(value.Administration)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func normalizePopulation(value PopulationExposureMetric) PopulationExposureMetric {
	value.Quantity, value.CoverageRatio = cloneFloat(value.Quantity), cloneCoverage(value.CoverageRatio)
	value.Unit = strings.TrimSpace(value.Unit)
	value.InputReferences = normalizeStrings(value.InputReferences)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func normalizeRoads(value RoadExposureMetric) RoadExposureMetric {
	value.Quantity, value.CoverageRatio = cloneFloat(value.Quantity), cloneCoverage(value.CoverageRatio)
	value.Unit = strings.TrimSpace(value.Unit)
	value.InputReferences = normalizeStrings(value.InputReferences)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func normalizePOIs(value POIExposureMetric) POIExposureMetric {
	value.Quantity, value.CoverageRatio = cloneFloat(value.Quantity), cloneCoverage(value.CoverageRatio)
	value.Unit = strings.TrimSpace(value.Unit)
	value.InputReferences = normalizeStrings(value.InputReferences)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func normalizeAdministration(value AdministrativeMatch) AdministrativeMatch {
	value.CoverageRatio = cloneCoverage(value.CoverageRatio)
	value.AdminCodes = normalizeStrings(value.AdminCodes)
	value.InputReferences = normalizeStrings(value.InputReferences)
	value.Limitations = normalizeStrings(value.Limitations)
	return value
}

func collectReferences(value Analysis) []string {
	result := append([]string(nil), value.InputReferences...)
	result = append(result, value.Area.InputReferences...)
	for _, zone := range value.Zones {
		result = append(result, zone.Area.InputReferences...)
		result = append(result, zone.Population.InputReferences...)
		result = append(result, zone.Roads.InputReferences...)
		result = append(result, zone.POIs.InputReferences...)
		result = append(result, zone.Administration.InputReferences...)
	}
	return normalizeStrings(result)
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	sort.Strings(result)
	return compactSorted(result)
}

func compactSorted(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value
	if result == 0 {
		result = 0
	}
	return &result
}

func cloneCoverage(value *float64) *float64 {
	result := cloneFloat(value)
	if result == nil || !finite(*result) {
		return result
	}
	if math.Abs(*result-1) <= 1e-9 {
		*result = 1
	}
	return result
}
