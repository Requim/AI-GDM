package loss

import (
	"fmt"
	"sort"
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
	for _, approvedBy := range []string{"   ", "reviewer\nadmin", "reviewer\u200badmin", strings.Repeat("a", 129)} {
		invalid = valid
		invalid.Status, invalid.ApprovedBy = BaselineApproved, approvedBy
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate() 未拒绝无效审核人 %q", approvedBy)
		}
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

func TestBindAssessmentIdentityBindsInputsAndExcludesCalculatedAt(t *testing.T) {
	first := validAssessmentFixture(t)
	secondInput := assessmentWithoutIdentity(first)
	secondInput.CalculatedAt = secondInput.CalculatedAt.Add(time.Hour)
	second, err := BindAssessmentIdentity(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.InputDigest != second.InputDigest {
		t.Fatalf("计算时间不应改变输入身份: first=%s second=%s", first.ID, second.ID)
	}
	changedInput := assessmentWithoutIdentity(first)
	changedInput.Evidence.Costs[0].LowCents++
	changed, err := BindAssessmentIdentity(changedInput)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID == first.ID || changed.InputDigest == first.InputDigest {
		t.Fatal("成本输入变化未改变评估身份")
	}
}

func TestAssessmentValidateRejectsEvidenceMutationAndUnitMismatch(t *testing.T) {
	value := validAssessmentFixture(t)
	value.Evidence.Exposures[0].Quantity++
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝摘要生成后的证据变化")
	}
	exposure := validAssessmentFixture(t).Evidence.Exposures[0]
	exposure.Unit = "meters"
	if err := exposure.Validate(); err == nil {
		t.Fatal("Exposure.Validate() 未拒绝设施单位错配")
	}
}

func TestAssessmentCoreBindsLastSuccessLimitationToVeryLowReference(t *testing.T) {
	base := validAssessmentFixture(t)
	base.Status = AssessmentReferenceOnly
	base.Confidence = MaxStaleReferenceConfidence
	base.ConfidenceBand = "very_low"
	base.Limitations = append(base.Limitations, LimitationReferenceOnly,
		LimitationReferenceRoadOnly, LimitationReferenceTransfer, LimitationLastSuccessStale)
	sort.Strings(base.Limitations)
	if err := validateAssessmentCore(base); err != nil {
		t.Fatalf("合法最后成功数据降级被拒绝: %v", err)
	}
	for name, mutate := range map[string]func(*Assessment){
		"状态冒充可用": func(value *Assessment) { value.Status = AssessmentAvailable },
		"置信度过高":  func(value *Assessment) { value.Confidence = MaxStaleReferenceConfidence + 0.01 },
		"等级过高":   func(value *Assessment) { value.ConfidenceBand = "low" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if err := validateAssessmentCore(changed); err == nil {
				t.Fatal("validateAssessmentCore() 未拒绝损坏的最后成功数据降级")
			}
		})
	}
}

func TestAssessmentEvidenceBindsMaximumAndPerZoneIntensity(t *testing.T) {
	value := mixedAssessmentFixture(t)
	for _, summary := range []string{"low", "high"} {
		changed := value.Evidence
		changed.IntensityBand = summary
		if err := changed.Validate(); err == nil {
			t.Fatalf("顶层强度低报为 %q 未 fail-closed", summary)
		}
	}
	overstated := validAssessmentFixture(t).Evidence
	overstated.IntensityBand = "very_high"
	if err := overstated.Validate(); err == nil {
		t.Fatal("顶层强度高报未 fail-closed")
	}
	mismatch := mixedAssessmentFixture(t).Evidence
	mismatch.Vulnerabilities = append([]Vulnerability(nil), mismatch.Vulnerabilities...)
	mismatch.Vulnerabilities[0].IntensityBand = "moderate"
	if err := mismatch.Validate(); err == nil {
		t.Fatal("暴露与脆弱性的逐区强度错配未 fail-closed")
	}
	secondInput := assessmentWithoutIdentity(value)
	secondInput.CalculatedAt = secondInput.CalculatedAt.Add(time.Hour)
	second, err := BindAssessmentIdentity(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != value.ID || second.InputDigest != value.InputDigest {
		t.Fatal("混合等级权威输入的稳定身份被计算时刻改变")
	}
}

func TestAssessmentEvidenceSupportsSharedZonesAndRejectsDuplicateFeatures(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validEvidenceFixture(now)
	evidence.RiskZones = append(evidence.RiskZones, RiskZoneEvidence{ID: "zone-2", Level: "high",
		AreaSquareMeters: 100, AdminCodes: []string{"CN"}})
	evidence.SpatialAnalysis.TotalAreaSquareM = 150
	evidence.Population[0].ZoneIDs = []string{"zone-1", "zone-2"}
	for index := range evidence.Exposures {
		evidence.Exposures[index].ZoneIDs = []string{"zone-1", "zone-2"}
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("共享 feature 绑定多个风险区应有效: %v", err)
	}
	evidence.Exposures[0].FeatureID = evidence.Population[0].FeatureID
	if err := evidence.Validate(); err == nil {
		t.Fatal("全局重复 featureId 未 fail-closed")
	}
}

func TestAssessmentEvidenceRejectsUnionAreaBelowLargestZone(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validEvidenceFixture(now)
	evidence.RiskZones = append(evidence.RiskZones, RiskZoneEvidence{ID: "zone-2", Level: "high",
		AreaSquareMeters: 100, AdminCodes: []string{"CN"}})
	evidence.SpatialAnalysis.TotalAreaSquareM = 1
	if err := evidence.Validate(); err == nil {
		t.Fatal("并集面积低于最大单区面积未 fail-closed")
	}
}

func TestAssessmentIdentityEnforcesProjectionLimitationsAndCausality(t *testing.T) {
	limited := assessmentWithoutIdentity(validAssessmentFixture(t))
	limited.Evidence.SpatialAnalysis.ProjectionLimitations = []string{"跳过非闭合设施 way 42"}
	if _, err := BindAssessmentIdentity(limited); err == nil {
		t.Fatal("存在投影限制时仍允许 high 置信度")
	}
	limited.Confidence, limited.ConfidenceBand = 0.79, "moderate"
	bound, err := BindAssessmentIdentity(limited)
	if err != nil || !containsString(bound.Limitations, "跳过非闭合设施 way 42") {
		t.Fatalf("投影限制未进入评估或置信度门禁异常: value=%+v err=%v", bound, err)
	}

	causal := assessmentWithoutIdentity(validAssessmentFixture(t))
	causal.CalculatedAt = causal.Evidence.SpatialAnalysis.ProjectionCollectedAt.Add(-time.Microsecond)
	if _, err = BindAssessmentIdentity(causal); err == nil {
		t.Fatal("评估时间早于投影采集时间未 fail-closed")
	}
}

func TestAssessmentIdentityRejectsEveryLateProvenanceAuthorityField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*provenance.Provenance, time.Time)
	}{
		{"validFrom", func(value *provenance.Provenance, candidate time.Time) { value.ValidFrom = candidate }},
		{"observedAt", func(value *provenance.Provenance, candidate time.Time) { value.ObservedAt = candidate }},
		{"publishedAt", func(value *provenance.Provenance, candidate time.Time) { value.PublishedAt = candidate }},
		{"revisionFirstSeenAt", func(value *provenance.Provenance, candidate time.Time) { value.RevisionFirstSeenAt = candidate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := assessmentWithoutIdentity(validAssessmentFixture(t))
			test.mutate(&value.Evidence.Costs[0].Source, value.CalculatedAt.Add(time.Microsecond))
			if _, err := BindAssessmentIdentity(value); err == nil {
				t.Fatal("晚于评估的来源权威时间未 fail-closed")
			}
		})
	}
}

func TestAssessmentIdentityCanonicalizesSubMicrosecondTimes(t *testing.T) {
	value := assessmentWithoutIdentity(validAssessmentFixture(t))
	value.CalculatedAt = value.CalculatedAt.Add(999 * time.Nanosecond)
	value.Evidence.SpatialAnalysis.ProjectionCollectedAt =
		value.Evidence.SpatialAnalysis.ProjectionCollectedAt.Add(777 * time.Nanosecond)
	bound, err := BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	if bound.CalculatedAt.Nanosecond()%1_000 != 0 ||
		bound.Evidence.SpatialAnalysis.ProjectionCollectedAt.Nanosecond()%1_000 != 0 {
		t.Fatalf("身份时间未规范为 UTC 微秒: assessment=%s projection=%s",
			bound.CalculatedAt, bound.Evidence.SpatialAnalysis.ProjectionCollectedAt)
	}
}

func TestAssessmentEvidenceRejectsProjectionWindowOutsideSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*AssessmentEvidence){
		"validFrom提前": func(value *AssessmentEvidence) {
			value.SpatialAnalysis.ProjectionValidFrom = value.Snapshot.ValidFrom.Add(-time.Microsecond)
		},
		"validTo延后": func(value *AssessmentEvidence) {
			value.SpatialAnalysis.ProjectionValidTo = value.Snapshot.ValidTo.Add(time.Microsecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			evidence := validEvidenceFixture(now)
			mutate(&evidence)
			if err := evidence.Validate(); err == nil {
				t.Fatal("空间投影有效期越界未 fail-closed")
			}
		})
	}
}

func TestProjectionEvidenceLimitationBounds(t *testing.T) {
	items := make([]string, maxProjectionLimitationItems)
	for index := range items {
		items[index] = fmt.Sprintf("%03d", index)
	}
	if err := validateProjectionEvidenceLimitations(items); err != nil {
		t.Fatalf("精确限制数量上界被拒绝: %v", err)
	}
	if err := validateProjectionEvidenceLimitations(append(items, "overflow")); err == nil {
		t.Fatal("限制数量越界未被拒绝")
	}
	if err := validateProjectionEvidenceLimitations([]string{strings.Repeat("x", maxProjectionLimitationBytes)}); err != nil {
		t.Fatalf("精确单项字符上界被拒绝: %v", err)
	}
	if err := validateProjectionEvidenceLimitations([]string{strings.Repeat("x", maxProjectionLimitationBytes+1)}); err == nil {
		t.Fatal("限制单项字符越界未被拒绝")
	}
	total := make([]string, 16)
	for index := range total {
		total[index] = fmt.Sprintf("%02d", index) + strings.Repeat("x", maxProjectionLimitationBytes-2)
	}
	if err := validateProjectionEvidenceLimitations(total); err != nil {
		t.Fatalf("精确限制总字符上界被拒绝: %v", err)
	}
	if err := validateProjectionEvidenceLimitations(append(total, "z")); err == nil {
		t.Fatal("限制总字符越界未被拒绝")
	}
}

func TestEvidenceReferencesIncludesProvenanceSourceParts(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validEvidenceFixture(now)
	parts := []provenance.SourcePart{{Reference: "https://example.test/lhasa-part", Revision: "part-1", SizeBytes: 128}}
	evidence.Snapshot.Source.SourceParts = parts
	evidence.Snapshot.Source.SourceRevision = provenance.CompositeSourceRevision(parts)
	references := EvidenceReferences(evidence)
	if !containsString(references, parts[0].Reference) {
		t.Fatalf("快照来源分片未进入规范引用集: %v", references)
	}
	value := validAssessmentFixture(t)
	value = assessmentWithoutIdentity(value)
	value.Evidence, value.InputReferences = evidence, references
	if _, err := BindAssessmentIdentity(value); err != nil {
		t.Fatalf("含 SourceParts 的完整证据无法绑定: %v", err)
	}
}

func validAssessmentFixture(t *testing.T) Assessment {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validEvidenceFixture(now)
	value := Assessment{SnapshotID: evidence.Snapshot.ID, FormulaVersion: FormulaVersion,
		ScenarioMethod: "确定性公式", HazardType: evidence.Snapshot.HazardType,
		RegionCode: evidence.SpatialAnalysis.RegionCode, ConditionalLowCents: 10,
		ConditionalMidCents: 20, ConditionalHighCents: 30,
		ImpactAreaSquareM: 100, AffectedPopulation: 10, AffectedRoadMeters: 5, AffectedFacilities: 1,
		InputReferences: EvidenceReferences(evidence), IncludedAssets: []AssetType{AssetFacility, AssetRoad},
		ExcludedLosses: []string{"建筑物损失未纳入"}, Status: AssessmentAvailable,
		Confidence: 1, ConfidenceBand: "high", Limitations: []string{"辅助研判"},
		CalculatedAt: now, Evidence: evidence}
	bound, err := BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func mixedAssessmentFixture(t *testing.T) Assessment {
	t.Helper()
	value := assessmentWithoutIdentity(validAssessmentFixture(t))
	evidence := value.Evidence
	evidence.RiskZones[0].Level = "low"
	evidence.RiskZones = append(evidence.RiskZones, RiskZoneEvidence{ID: "zone-2", Level: "very_high",
		AreaSquareMeters: 100, AdminCodes: []string{"CN"}})
	evidence.Population = append(evidence.Population, PopulationEvidence{FeatureID: "population-2", ZoneID: "zone-2",
		ZoneIDs: []string{"zone-2"}, Quantity: 2, Unit: "people", CoverageRatio: 1, Provided: true,
		MetricStatus: "available", InputReferences: []string{"population://zone-2"}})
	facilityLow, roadLow := evidence.Exposures[0], evidence.Exposures[1]
	facilityLow.IntensityBand, roadLow.IntensityBand = "low", "low"
	facilityHigh := Exposure{FeatureID: "facility-2", ZoneID: "zone-2", ZoneIDs: []string{"zone-2"},
		AssetType: AssetFacility, Quantity: 1, Unit: "count", CoverageRatio: 1, Provided: true,
		MetricStatus: "available", IntensityBand: "very_high", AnalysisID: "analysis-1",
		AnalysisVersion: "analysis-v1", InputReferences: []string{"poi://zone-2"}}
	roadHigh := Exposure{FeatureID: "road-2", ZoneID: "zone-2", ZoneIDs: []string{"zone-2"}, AssetType: AssetRoad,
		Quantity: 0, Unit: "meters", CoverageRatio: 1, Provided: true, MetricStatus: "available",
		IntensityBand: "very_high", AnalysisID: "analysis-1", AnalysisVersion: "analysis-v1",
		InputReferences: []string{"road://zone-2"}}
	evidence.Exposures = []Exposure{facilityLow, facilityHigh, roadLow, roadHigh}
	facilityLowVulnerability, roadLowVulnerability := evidence.Vulnerabilities[0], evidence.Vulnerabilities[1]
	facilityLowVulnerability.IntensityBand, roadLowVulnerability.IntensityBand = "low", "low"
	facilityHighVulnerability := facilityLowVulnerability
	facilityHighVulnerability.ID, facilityHighVulnerability.IntensityBand = "vulnerability-facility-very-high", "very_high"
	roadHighVulnerability := roadLowVulnerability
	roadHighVulnerability.ID, roadHighVulnerability.IntensityBand = "vulnerability-road-very-high", "very_high"
	evidence.Vulnerabilities = []Vulnerability{facilityLowVulnerability, facilityHighVulnerability,
		roadLowVulnerability, roadHighVulnerability}
	evidence.IntensityBand, evidence.SpatialAnalysis.TotalAreaSquareM = "very_high", 150
	value.Evidence, value.ImpactAreaSquareM = evidence, 150
	value.AffectedPopulation, value.AffectedFacilities = 12, 2
	value.InputReferences = EvidenceReferences(evidence)
	bound, err := BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func validEvidenceFixture(now time.Time) AssessmentEvidence {
	baseline := testBaselineSource("v1")
	dynamic := baseline
	dynamic.Provider, dynamic.Dataset, dynamic.DataKind = "NASA", "LHASA", provenance.DataKindNowcast
	dynamic.SourceURI, dynamic.FetchedAt = "https://example.test/lhasa", now.Add(-time.Hour)
	dynamic.ValidFrom, dynamic.ValidTo = now.Add(-2*time.Hour), now.Add(12*time.Hour)
	return AssessmentEvidence{Version: EvidenceVersion,
		Snapshot: SnapshotEvidence{ID: "snapshot-1", HazardType: "landslide", ModelName: "LHASA", ModelVersion: "2.1.1",
			Status: "available", RunAt: now.Add(-time.Hour), ValidFrom: now.Add(-2 * time.Hour), ValidTo: now.Add(12 * time.Hour), Source: dynamic},
		SpatialAnalysis: SpatialAnalysisEvidence{ID: "analysis-1", Version: "analysis-v1", Digest: strings.Repeat("b", 64),
			ProjectionID: "exposure-" + strings.Repeat("c", 64), ProjectionVersion: RiskProjectionVersion,
			ProjectionDigest: strings.Repeat("c", 64), ProjectionCollectedAt: now.Add(-20 * time.Minute),
			ProjectionValidFrom: now.Add(-time.Hour), ProjectionValidTo: now.Add(time.Hour),
			SourceReferenceDigests: []string{strings.Repeat("d", 64)},
			ProjectionLimitations:  []string{}, AdminBoundaryID: "CHN-ADM0-geoboundaries-v6",
			AdminBoundaryDigest: strings.Repeat("e", 64), Status: "available",
			RegionCode: "CN", TotalAreaSquareM: 100, CalculatedAt: now.Add(-30 * time.Minute),
			InputReferences: []string{"analysis://input"}, DatasetReferences: []string{"analysis://dataset"}},
		BaselineSet:   BaselineSetEvidence{Provider: baseline.Provider, Dataset: baseline.Dataset, Version: baseline.DatasetVersion},
		IntensityBand: "high", RiskZones: []RiskZoneEvidence{{ID: "zone-1", Level: "high", AreaSquareMeters: 100, AdminCodes: []string{"CN"}}},
		Population: []PopulationEvidence{{FeatureID: "population-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"},
			Quantity: 10, Unit: "people", CoverageRatio: 1, Provided: true,
			MetricStatus: "available", InputReferences: []string{"population://zone-1"}}},
		Exposures: []Exposure{
			{FeatureID: "facility-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"}, AssetType: AssetFacility,
				Quantity: 1, Unit: "count", CoverageRatio: 1, Provided: true, MetricStatus: "available",
				IntensityBand: "high", AnalysisID: "analysis-1", AnalysisVersion: "analysis-v1", InputReferences: []string{"poi://zone-1"}},
			{FeatureID: "road-1", ZoneID: "zone-1", ZoneIDs: []string{"zone-1"}, AssetType: AssetRoad,
				Quantity: 5, Unit: "meters", CoverageRatio: 1, Provided: true, MetricStatus: "available",
				IntensityBand: "high", AnalysisID: "analysis-1", AnalysisVersion: "analysis-v1", InputReferences: []string{"road://zone-1"}},
		}, Costs: []CostBaseline{approvedDomainCost(AssetFacility, "count", baseline), approvedDomainCost(AssetRoad, "meters", baseline)},
		Vulnerabilities: []Vulnerability{approvedDomainVulnerability(AssetFacility, baseline), approvedDomainVulnerability(AssetRoad, baseline)}}
}

func approvedDomainCost(asset AssetType, unit string, source provenance.Provenance) CostBaseline {
	return CostBaseline{ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: unit,
		LowCents: 10, CentralCents: 20, HighCents: 30, Currency: "CNY", PriceBaseDate: source.ValidFrom,
		Status: BaselineApproved, Provided: true, BaselineLevel: BaselineNational,
		ApprovedBy: "reviewer", Source: source}
}

func approvedDomainVulnerability(asset AssetType, source provenance.Provenance) Vulnerability {
	return Vulnerability{ID: "vulnerability-" + string(asset), AssetType: asset, HazardType: "landslide",
		IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
		DamageRatioLow: 0.1, DamageRatioMid: 0.2, DamageRatioHigh: 0.3, CalibrationRegion: "CN",
		Status: BaselineApproved, Provided: true, BaselineLevel: BaselineNational,
		ApprovedBy: "reviewer", Source: source}
}

func assessmentWithoutIdentity(value Assessment) Assessment {
	value.ID, value.InputDigest = "", ""
	return value
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
