package lossreference

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestReaderReturnsRoadReferenceForMixedRequirements(t *testing.T) {
	set, err := New().BaselineSet(context.Background(), mixedQuery())
	if err != nil {
		t.Fatalf("BaselineSet() error = %v", err)
	}
	if err = set.Validate(); err != nil {
		t.Fatalf("BaselineSet().Validate() error = %v", err)
	}
	assertReferenceCost(t, set)
	assertReferenceVulnerabilities(t, set)
	assertReferenceSource(t, set.Costs[0].Source)
	if len(set.Population) != 1 || len(set.Roads) != 1 || set.Population[0].Quantity != 0 || set.Roads[0].Quantity != 0 {
		t.Fatalf("身份暴露记录不完整: population=%+v roads=%+v", set.Population, set.Roads)
	}
}

func TestReaderRejectsQueriesWithoutCompleteRoadSemantics(t *testing.T) {
	tests := []struct {
		name  string
		query applicationloss.BaselineQuery
	}{
		{name: "only facility", query: queryWith(
			[]applicationloss.CostBaselineRequirement{{AssetType: lossdomain.AssetFacility, Unit: "count"}},
			[]applicationloss.VulnerabilityBaselineRequirement{{AssetType: lossdomain.AssetFacility, IntensityBand: "high"}})},
		{name: "road cost without vulnerability", query: queryWith(
			[]applicationloss.CostBaselineRequirement{{AssetType: lossdomain.AssetRoad, Unit: "meters"}},
			[]applicationloss.VulnerabilityBaselineRequirement{{AssetType: lossdomain.AssetFacility, IntensityBand: "high"}})},
		{name: "road vulnerability without cost", query: queryWith(
			[]applicationloss.CostBaselineRequirement{{AssetType: lossdomain.AssetFacility, Unit: "count"}},
			[]applicationloss.VulnerabilityBaselineRequirement{{AssetType: lossdomain.AssetRoad, IntensityBand: "high"}})},
		{name: "wrong road unit", query: queryWith(
			[]applicationloss.CostBaselineRequirement{{AssetType: lossdomain.AssetRoad, Unit: "kilometers"}},
			[]applicationloss.VulnerabilityBaselineRequirement{{AssetType: lossdomain.AssetRoad, IntensityBand: "high"}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().BaselineSet(context.Background(), test.query)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("BaselineSet() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestReaderRejectsUnsupportedScopeAndInvalidQuery(t *testing.T) {
	tests := []struct {
		name string
		edit func(*applicationloss.BaselineQuery)
		want error
	}{
		{name: "outside china", edit: func(q *applicationloss.BaselineQuery) { q.RegionCode = "US" }, want: domain.ErrNotFound},
		{name: "unsupported hazard", edit: func(q *applicationloss.BaselineQuery) { q.HazardType = "flood" }, want: domain.ErrNotFound},
		{name: "empty region", edit: func(q *applicationloss.BaselineQuery) { q.RegionCode = "" }, want: domain.ErrInvalidInput},
		{name: "empty time", edit: func(q *applicationloss.BaselineQuery) { q.At = time.Time{} }, want: domain.ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := mixedQuery()
			test.edit(&query)
			_, err := New().BaselineSet(context.Background(), query)
			if !errors.Is(err, test.want) {
				t.Fatalf("BaselineSet() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestReaderHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().BaselineSet(ctx, mixedQuery())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BaselineSet() error = %v, want context.Canceled", err)
	}
}

func TestReaderBindsRequestedDebrisFlowHazard(t *testing.T) {
	query := mixedQuery()
	query.HazardType = "debris_flow"
	set, err := New().BaselineSet(context.Background(), query)
	if err != nil {
		t.Fatalf("BaselineSet() error = %v", err)
	}
	for _, value := range set.Vulnerabilities {
		if value.HazardType != "debris_flow" {
			t.Fatalf("Vulnerability.HazardType = %q, want debris_flow", value.HazardType)
		}
	}
}

func TestReaderReturnsIndependentReferenceValues(t *testing.T) {
	first, err := New().BaselineSet(context.Background(), mixedQuery())
	if err != nil {
		t.Fatalf("first BaselineSet() error = %v", err)
	}
	first.Costs[0].Source.Limitations[0] = "tampered"
	first.Vulnerabilities[0].Source.QualityFlags[0] = "tampered"
	first.Roads[0].Source.SourceParts[0].Reference = "https://example.invalid"
	second, err := New().BaselineSet(context.Background(), mixedQuery())
	if err != nil {
		t.Fatalf("second BaselineSet() error = %v", err)
	}
	if second.Costs[0].Source.Limitations[0] == "tampered" ||
		second.Vulnerabilities[0].Source.QualityFlags[0] == "tampered" ||
		second.Roads[0].Source.SourceParts[0].Reference == "https://example.invalid" {
		t.Fatal("BaselineSet() 在调用之间共享可变来源切片")
	}
}

func assertReferenceCost(t *testing.T, set lossdomain.BaselineSet) {
	t.Helper()
	if set.Version != datasetVersion || len(set.Costs) != 1 {
		t.Fatalf("道路成本基线数量或版本无效: %+v", set)
	}
	cost := set.Costs[0]
	if cost.AssetType != lossdomain.AssetRoad || cost.Unit != "meters" ||
		cost.LowCents != 30_646 || cost.CentralCents != 36_436 || cost.HighCents != 42_226 {
		t.Fatalf("道路条件损失包络无效: %+v", cost)
	}
	if cost.Status != lossdomain.BaselineDemoOnly || !cost.Provided ||
		cost.BaselineLevel != lossdomain.BaselineRegional || cost.RegionCode != "CN-54" {
		t.Fatalf("道路参考基线选择元数据无效: %+v", cost)
	}
}

func assertReferenceVulnerabilities(t *testing.T, set lossdomain.BaselineSet) {
	t.Helper()
	if len(set.Vulnerabilities) != 2 {
		t.Fatalf("道路脆弱性数量 = %d, want 2", len(set.Vulnerabilities))
	}
	for _, value := range set.Vulnerabilities {
		if value.AssetType != lossdomain.AssetRoad || value.CalibrationRegion != "CN-54" ||
			value.Status != lossdomain.BaselineDemoOnly || !value.Provided ||
			value.ImpactFractionLow != 1 || value.ImpactFractionMid != 1 || value.ImpactFractionHigh != 1 ||
			value.DamageRatioLow != 1 || value.DamageRatioMid != 1 || value.DamageRatioHigh != 1 {
			t.Fatalf("道路参考脆弱性无效: %+v", value)
		}
	}
}

func assertReferenceSource(t *testing.T, source provenance.Provenance) {
	t.Helper()
	if source.DataKind != provenance.DataKindBaseline || source.DatasetVersion != datasetVersion ||
		source.TransformVersion != "class-envelope-midpoint-v1" || len(source.SourceParts) != 2 {
		t.Fatalf("来源身份无效: %+v", source)
	}
	if source.SourceRevision != provenance.CompositeSourceRevision(source.SourceParts) ||
		!strings.Contains(source.License, "CC BY 4.0") || !strings.Contains(source.Citation, "7.7837") {
		t.Fatalf("来源审计绑定无效: %+v", source)
	}
	if source.SourceParts[0].Reference != articleURL || source.SourceParts[1].Reference != ecbRateURL ||
		source.SourceParts[0].SHA256 != digestText(articleClaim) || source.SourceParts[1].SHA256 != digestText(rateClaim) {
		t.Fatalf("来源分片绑定无效: %+v", source.SourceParts)
	}
	joined := strings.Join(source.Limitations, "\n")
	for _, text := range []string{"跨区域外推", "历史欧元兑人民币", "不覆盖设施"} {
		if !strings.Contains(joined, text) {
			t.Fatalf("来源限制缺少 %q: %s", text, joined)
		}
	}
}

func mixedQuery() applicationloss.BaselineQuery {
	return queryWith(
		[]applicationloss.CostBaselineRequirement{
			{AssetType: lossdomain.AssetRoad, Unit: "meters"},
			{AssetType: lossdomain.AssetFacility, Unit: "count"},
		},
		[]applicationloss.VulnerabilityBaselineRequirement{
			{AssetType: lossdomain.AssetRoad, IntensityBand: "very_high"},
			{AssetType: lossdomain.AssetFacility, IntensityBand: "high"},
			{AssetType: lossdomain.AssetRoad, IntensityBand: "high"},
		},
	)
}

func queryWith(costs []applicationloss.CostBaselineRequirement,
	vulnerabilities []applicationloss.VulnerabilityBaselineRequirement,
) applicationloss.BaselineQuery {
	return applicationloss.BaselineQuery{RegionCode: "CN", HazardType: "landslide",
		Requirements: applicationloss.BaselineRequirements{Costs: costs, Vulnerabilities: vulnerabilities},
		At:           time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}
}
