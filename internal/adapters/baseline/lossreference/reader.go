package lossreference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

const (
	datasetVersion = "gyirong-road-reference-2024-fx-20240301-v1"
	articleURL     = "https://doi.org/10.1007/s13753-024-00545-x"
	ecbRateURL     = "https://data-api.ecb.europa.eu/service/data/EXR/D.CNY.EUR.SP00.A?startPeriod=2024-03-01&endPeriod=2024-03-01"
	articleClaim   = "third_class_eur_per_km=83460;third_class_vulnerability=0.65;fourth_class_eur_per_km=46320;fourth_class_vulnerability=0.85"
	rateClaim      = "date=2024-03-01;currency_pair=EUR/CNY;cny_per_eur=7.7837"
)

var _ applicationloss.BaselineSetReader = (*Reader)(nil)

// Reader 返回内置、版本化且明确标记为研究参考的道路损失基线。
type Reader struct{}

// New 创建道路研究参考基线读取器。
func New() *Reader {
	return &Reader{}
}

// BaselineSet 返回请求中道路语义键对应的研究参考记录，不为其他资产伪造基线。
func (r *Reader) BaselineSet(ctx context.Context,
	query applicationloss.BaselineQuery,
) (lossdomain.BaselineSet, error) {
	if err := ctx.Err(); err != nil {
		return lossdomain.BaselineSet{}, err
	}
	intensities, err := validateQuery(query)
	if err != nil {
		return lossdomain.BaselineSet{}, err
	}
	set := buildSet(query.HazardType, intensities)
	if err = set.Validate(); err != nil {
		return lossdomain.BaselineSet{}, fmt.Errorf("校验道路研究参考基线: %w", err)
	}
	return set, nil
}

func validateQuery(query applicationloss.BaselineQuery) ([]string, error) {
	if invalidLookup(query.RegionCode) || invalidLookup(query.HazardType) || query.At.IsZero() {
		return nil, fmt.Errorf("%w: 道路研究参考基线查询无效", domain.ErrInvalidInput)
	}
	if err := query.Requirements.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(query.RegionCode, "CN") || !supportedHazard(query.HazardType) {
		return nil, referenceNotFound()
	}
	intensities := roadIntensities(query.Requirements.Vulnerabilities)
	if !hasRoadCost(query.Requirements.Costs) || len(intensities) == 0 {
		return nil, referenceNotFound()
	}
	return intensities, nil
}

func invalidLookup(value string) bool {
	return value == "" || strings.TrimSpace(value) != value || len(value) > 128
}

func supportedHazard(value string) bool {
	return value == "landslide" || value == "debris_flow"
}

func hasRoadCost(values []applicationloss.CostBaselineRequirement) bool {
	for _, value := range values {
		if value.AssetType == lossdomain.AssetRoad && value.Unit == "meters" {
			return true
		}
	}
	return false
}

func roadIntensities(values []applicationloss.VulnerabilityBaselineRequirement) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value.AssetType == lossdomain.AssetRoad {
			result = append(result, value.IntensityBand)
		}
	}
	sort.Strings(result)
	return result
}

func referenceNotFound() error {
	return fmt.Errorf("%w: 请求未包含可用道路研究参考语义键", domain.ErrNotFound)
}

func buildSet(hazardType string, intensities []string) lossdomain.BaselineSet {
	source := referenceSource()
	return lossdomain.BaselineSet{
		Version:         datasetVersion,
		Population:      []lossdomain.ExposureBaseline{identityExposure(lossdomain.ExposurePopulation, source)},
		Roads:           []lossdomain.ExposureBaseline{identityExposure(lossdomain.ExposureRoad, source)},
		Costs:           []lossdomain.CostBaseline{roadCost(source)},
		Vulnerabilities: roadVulnerabilities(hazardType, intensities, source),
	}
}

func identityExposure(kind lossdomain.ExposureKind, source provenance.Provenance) lossdomain.ExposureBaseline {
	unit, id := "people", "reference-population-identity-v1"
	if kind == lossdomain.ExposureRoad {
		unit, id = "meters", "reference-road-identity-v1"
	}
	return lossdomain.ExposureBaseline{ID: id, RegionCode: "CN-54", Kind: kind, Quantity: 0,
		Unit: unit, DataYear: 2024, CoverageRatio: 1, Source: cloneSource(source)}
}

func roadCost(source provenance.Provenance) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{
		ID: "reference-road-conditional-cost-v1", AssetType: lossdomain.AssetRoad, RegionCode: "CN-54",
		Unit: "meters", LowCents: 30_646, CentralCents: 36_436, HighCents: 42_226,
		Currency: "CNY", PriceBaseDate: referencePriceDate(), Status: lossdomain.BaselineDemoOnly,
		Provided: true, BaselineLevel: lossdomain.BaselineRegional, Source: cloneSource(source),
	}
}

func roadVulnerabilities(hazardType string, intensities []string,
	source provenance.Provenance,
) []lossdomain.Vulnerability {
	result := make([]lossdomain.Vulnerability, 0, len(intensities))
	for _, intensity := range intensities {
		result = append(result, lossdomain.Vulnerability{
			ID: "reference-road-conditional-" + intensity + "-v1", AssetType: lossdomain.AssetRoad,
			HazardType: hazardType, IntensityBand: intensity,
			ImpactFractionLow: 1, ImpactFractionMid: 1, ImpactFractionHigh: 1,
			DamageRatioLow: 1, DamageRatioMid: 1, DamageRatioHigh: 1,
			CalibrationRegion: "CN-54", Status: lossdomain.BaselineDemoOnly,
			Provided: true, BaselineLevel: lossdomain.BaselineRegional, Source: cloneSource(source),
		})
	}
	return result
}

func referenceSource() provenance.Provenance {
	parts := referenceParts()
	return provenance.Provenance{
		Provider: "International Journal of Disaster Risk Science and European Central Bank",
		Dataset:  "Gyirong road conditional loss reference", DatasetVersion: datasetVersion,
		SourceRevision: provenance.CompositeSourceRevision(parts), SourceURI: articleURL,
		Citation: "2024 年西藏吉隆藏布流域泥石流道路损失研究，DOI 10.1007/s13753-024-00545-x；欧洲中央银行 2024-03-01 参考汇率 1 EUR=7.7837 CNY",
		License:  "研究文章 CC BY 4.0；ECB 数据遵循 ECB 使用条款", DataKind: provenance.DataKindBaseline,
		RevisionFirstSeenAt: referenceFetchedAt(), FetchedAt: referenceFetchedAt(),
		ValidFrom: referencePriceDate(), ValidTo: time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC),
		TemporalResolution: "2024 research reference", SHA256: referenceDigest(),
		TransformVersion: "class-envelope-midpoint-v1", Model: "conditional-road-direct-loss",
		QualityFlags: []string{"demo_only", "research_reference", "road_only", "historical_fx_conversion"},
		Limitations:  referenceLimitations(), SourceParts: parts,
	}
}

func referenceParts() []provenance.SourcePart {
	return []provenance.SourcePart{
		referencePart(articleURL, articleClaim),
		referencePart(ecbRateURL, rateClaim),
	}
}

func referencePart(reference, claim string) provenance.SourcePart {
	digest := digestText(claim)
	return provenance.SourcePart{Reference: reference, Revision: "sha256:" + digest,
		SizeBytes: int64(len([]byte(claim))), SHA256: digest}
}

func referenceLimitations() []string {
	return []string{
		"仅覆盖道路直接物理损失，不覆盖设施、人口、建筑物或间接损失",
		"基线来自西藏吉隆藏布流域案例，跨区域外推仅作研究参考",
		"金额使用 2024-03-01 历史欧元兑人民币参考汇率换算，未做现价调整",
		"当前暴露投影无法可靠识别道路等级，采用三级与四级道路条件损失包络及算术中点",
	}
}

func referenceDigest() string {
	return digestText("ai-gdm-loss-reference-v1\n" + datasetVersion + "\n" + articleClaim + "\n" + rateClaim)
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func referencePriceDate() time.Time {
	return time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
}

func referenceFetchedAt() time.Time {
	return time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
}

func cloneSource(value provenance.Provenance) provenance.Provenance {
	value.QualityFlags = append([]string(nil), value.QualityFlags...)
	value.Limitations = append([]string(nil), value.Limitations...)
	value.SourceParts = append([]provenance.SourcePart(nil), value.SourceParts...)
	return value
}
