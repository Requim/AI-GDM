package main

import losssdomain "github.com/Requim/AI-GDM/internal/domain/loss"

func bindFixtureBaselineRegion(value *losssdomain.BaselineSet, region string) {
	for index := range value.Population {
		value.Population[index].RegionCode = region
	}
	for index := range value.Roads {
		value.Roads[index].RegionCode = region
	}
	for index := range value.Costs {
		value.Costs[index].RegionCode = region
	}
	for index := range value.Vulnerabilities {
		value.Vulnerabilities[index].CalibrationRegion = region
	}
}
