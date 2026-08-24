package loss

import (
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// AssetType 表示损失评估中的资产类别。
type AssetType string

const (
	AssetBuilding AssetType = "building"
	AssetRoad     AssetType = "road"
	AssetFacility AssetType = "facility"
)

// BaselineStatus 标识基线是演示参数还是已审核数据。
type BaselineStatus string

const (
	BaselineDemoOnly BaselineStatus = "demo_only"
	BaselineApproved BaselineStatus = "approved"
)

// AssessmentStatus 表示损失评估是否具备完整输入。
type AssessmentStatus string

const (
	AssessmentAvailable        AssessmentStatus = "available"
	AssessmentInsufficientData AssessmentStatus = "insufficient_data"
)

// Exposure 描述风险区内暴露的资产或人口数量。
type Exposure struct {
	ZoneID        string                `json:"zoneId"`
	AssetType     string                `json:"assetType"`
	Quantity      float64               `json:"quantity"`
	Unit          string                `json:"unit"`
	DataYear      int                   `json:"dataYear"`
	CoverageRatio float64               `json:"coverageRatio"`
	Source        provenance.Provenance `json:"source"`
}

// CostBaseline 保存直接物理损害所需的单位重置成本情景带，金额单位为人民币分。
type CostBaseline struct {
	ID            string                `json:"id"`
	AssetType     AssetType             `json:"assetType"`
	RegionCode    string                `json:"regionCode"`
	Unit          string                `json:"unit"`
	LowCents      int64                 `json:"lowCents"`
	CentralCents  int64                 `json:"centralCents"`
	HighCents     int64                 `json:"highCents"`
	Currency      string                `json:"currency"`
	PriceBaseDate time.Time             `json:"priceBaseDate"`
	Status        BaselineStatus        `json:"status"`
	ApprovedBy    string                `json:"approvedBy,omitempty"`
	Source        provenance.Provenance `json:"source"`
}

// Vulnerability 保存有来源的影响比例和损伤率情景带。
type Vulnerability struct {
	ID                 string                `json:"id"`
	AssetType          AssetType             `json:"assetType"`
	HazardType         string                `json:"hazardType"`
	IntensityBand      string                `json:"intensityBand"`
	ImpactFractionLow  float64               `json:"impactFractionLow"`
	ImpactFractionMid  float64               `json:"impactFractionMid"`
	ImpactFractionHigh float64               `json:"impactFractionHigh"`
	DamageRatioLow     float64               `json:"damageRatioLow"`
	DamageRatioMid     float64               `json:"damageRatioMid"`
	DamageRatioHigh    float64               `json:"damageRatioHigh"`
	CalibrationRegion  string                `json:"calibrationRegion"`
	Status             BaselineStatus        `json:"status"`
	Source             provenance.Provenance `json:"source"`
}

// Assessment 保存低、中、高情景下的直接物理损害估算。
type Assessment struct {
	ID                   string           `json:"id"`
	SnapshotID           string           `json:"snapshotId"`
	FormulaVersion       string           `json:"formulaVersion"`
	ScenarioMethod       string           `json:"scenarioMethod"`
	ConditionalLowCents  int64            `json:"conditionalLowCents"`
	ConditionalMidCents  int64            `json:"conditionalCentralCents"`
	ConditionalHighCents int64            `json:"conditionalHighCents"`
	ExpectedLowCents     *int64           `json:"expectedLowCents,omitempty"`
	ExpectedMidCents     *int64           `json:"expectedCentralCents,omitempty"`
	ExpectedHighCents    *int64           `json:"expectedHighCents,omitempty"`
	ImpactAreaSquareM    float64          `json:"impactAreaSquareMeters"`
	AffectedPopulation   float64          `json:"affectedPopulation"`
	AffectedRoadMeters   float64          `json:"affectedRoadMeters"`
	AffectedFacilities   int              `json:"affectedFacilities"`
	InputReferences      []string         `json:"inputReferences"`
	IncludedAssets       []AssetType      `json:"includedAssets"`
	ExcludedLosses       []string         `json:"excludedLosses"`
	Status               AssessmentStatus `json:"status"`
	Limitations          []string         `json:"limitations"`
	CalculatedAt         time.Time        `json:"calculatedAt"`
}

// Validate 校验成本情景带、币种和审核要求。
func (b CostBaseline) Validate() error {
	if b.LowCents < 0 || b.LowCents > b.CentralCents || b.CentralCents > b.HighCents {
		return fmt.Errorf("%w: 单位成本情景带无序", domain.ErrInvalidInput)
	}
	if b.Currency != "CNY" || b.PriceBaseDate.IsZero() || b.Unit == "" {
		return fmt.Errorf("%w: 成本币种、单位或基准日无效", domain.ErrInvalidInput)
	}
	if b.Status == BaselineApproved && b.ApprovedBy == "" {
		return fmt.Errorf("%w: 已审核基线缺少审核人", domain.ErrInvalidInput)
	}
	return nil
}
