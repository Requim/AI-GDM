package loss

import (
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestCostBaselineValidate(t *testing.T) {
	valid := CostBaseline{
		ID: "cost-building-cn", RegionCode: "CN", AssetType: AssetBuilding,
		Unit: "平方米", LowCents: 100, CentralCents: 200, HighCents: 300,
		Currency: "CNY", PriceBaseDate: time.Now().UTC(), Status: BaselineDemoOnly,
		Source: testBaselineSource("v1"),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := valid
	invalid.LowCents = 400
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝无序情景带")
	}
}

func TestExposureBaselineValidate(t *testing.T) {
	value := ExposureBaseline{
		ID: "population-cn", RegionCode: "CN", Kind: ExposurePopulation,
		Quantity: 0, Unit: "people", DataYear: 2025, CoverageRatio: 1,
		Source: testBaselineSource("v1"),
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	value.CoverageRatio = 0
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝无效覆盖率")
	}
}

func TestVulnerabilityValidate(t *testing.T) {
	value := Vulnerability{
		ID: "landslide-building", AssetType: AssetBuilding, HazardType: "landslide",
		IntensityBand: "high", ImpactFractionLow: 0.2, ImpactFractionMid: 0.5, ImpactFractionHigh: 0.8,
		DamageRatioLow: 0.1, DamageRatioMid: 0.3, DamageRatioHigh: 0.7, CalibrationRegion: "CN",
		Status: BaselineApproved, ApprovedBy: "reviewer", Source: testBaselineSource("v1"),
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	value.DamageRatioLow = 0.9
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝无序损伤率")
	}
}

func TestBaselineSetValidateRequiresMatchingVersionsAndUniqueIDs(t *testing.T) {
	set := testBaselineSet("v1")
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	set.Roads[0].ID = set.Population[0].ID
	if err := set.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝重复基线标识")
	}
	set.Roads[0].ID = "road-cn"
	set.Roads[0].Source = testBaselineSource("v2")
	if err := set.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝版本不一致")
	}
}

func TestBaselineSourceRejectsEqualValidityBounds(t *testing.T) {
	source := testBaselineSource("v1")
	source.ValidTo = source.ValidFrom
	value := testBaselineSet("v1").Population[0]
	value.Source = source
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝相同的有效期起止时间")
	}
}

func testBaselineSet(version string) BaselineSet {
	source := testBaselineSource(version)
	return BaselineSet{
		Version: version,
		Population: []ExposureBaseline{{ID: "population-cn", RegionCode: "CN", Kind: ExposurePopulation,
			Quantity: 10, Unit: "people", DataYear: 2025, CoverageRatio: 1, Source: source}},
		Roads: []ExposureBaseline{{ID: "road-cn", RegionCode: "CN", Kind: ExposureRoad,
			Quantity: 1, Unit: "meters", DataYear: 2025, CoverageRatio: 1, Source: source}},
		Costs: []CostBaseline{{ID: "cost-cn", AssetType: AssetBuilding, RegionCode: "CN", Unit: "平方米",
			LowCents: 100, CentralCents: 200, HighCents: 300, Currency: "CNY",
			PriceBaseDate: source.ValidFrom, Status: BaselineDemoOnly, Source: source}},
		Vulnerabilities: []Vulnerability{{ID: "vulnerability-cn", AssetType: AssetBuilding, HazardType: "landslide",
			IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
			DamageRatioLow: 0.1, DamageRatioMid: 0.2, DamageRatioHigh: 0.3, CalibrationRegion: "CN",
			Status: BaselineDemoOnly, Source: source}},
	}
}

func testBaselineSource(version string) provenance.Provenance {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return provenance.Provenance{
		Provider: "test", Dataset: "loss-baseline", DatasetVersion: version,
		SourceRevision: "revision-1", SourceURI: "https://example.test/baseline",
		Citation: "测试基线引用", License: "CC-BY-4.0", DataKind: provenance.DataKindBaseline,
		FetchedAt: now, ValidFrom: now, ValidTo: now.Add(365 * 24 * time.Hour),
		SHA256: strings.Repeat("a", 64), TransformVersion: "baseline-import-v1",
		QualityFlags: []string{"test_fixture"},
	}
}
