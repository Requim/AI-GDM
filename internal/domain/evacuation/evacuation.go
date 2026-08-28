package evacuation

import (
	"fmt"
	"math"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// TravelMode 表示疏散交通方式。
type TravelMode string

const (
	TravelDriving TravelMode = "driving"
	TravelWalking TravelMode = "walking"
	TravelTransit TravelMode = "transit"
)

// FacilityType 表示避险相关设施分类。
type FacilityType string

const (
	FacilityShelter   FacilityType = "shelter"
	FacilityHospital  FacilityType = "hospital"
	FacilityTransport FacilityType = "transport"
)

// Facility 表示地图供应商返回的候选设施。
type Facility struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Type           FacilityType          `json:"type"`
	Location       spatial.Point         `json:"location"`
	Address        string                `json:"address"`
	DistanceMeters float64               `json:"distanceMeters,omitempty"`
	Source         provenance.Provenance `json:"source"`
}

// RouteStep 表示一段可展示的导航说明。
type RouteStep struct {
	Instruction string  `json:"instruction"`
	RoadName    string  `json:"roadName,omitempty"`
	DistanceM   float64 `json:"distanceMeters"`
}

// Route 表示尚未获得交管部门确认的候选疏散路线。
type Route struct {
	ID                 string                `json:"id"`
	Origin             spatial.Point         `json:"origin"`
	Destination        spatial.Point         `json:"destination"`
	Mode               TravelMode            `json:"mode"`
	DistanceMeters     float64               `json:"distanceMeters"`
	DurationSeconds    int64                 `json:"durationSeconds"`
	Geometry           spatial.Geometry      `json:"geometry"`
	Steps              []RouteStep           `json:"steps"`
	IntersectsRiskZone bool                  `json:"intersectsRiskZone"`
	RiskScore          float64               `json:"riskScore"`
	RiskScoreProvided  bool                  `json:"riskScoreProvided"`
	Rank               int                   `json:"rank,omitempty"`
	Source             provenance.Provenance `json:"source"`
	Limitations        []string              `json:"limitations"`
}

// Validate 校验候选路线的基本数值约束。
func (r Route) Validate() error {
	if err := r.Origin.Validate(); err != nil {
		return err
	}
	if err := r.Destination.Validate(); err != nil {
		return err
	}
	if r.DistanceMeters <= 0 || r.DurationSeconds <= 0 ||
		math.IsNaN(r.RiskScore) || math.IsInf(r.RiskScore, 0) || r.RiskScore < 0 || r.RiskScore > 100 {
		return fmt.Errorf("%w: 路线距离、时长或风险分数无效", domain.ErrInvalidInput)
	}
	if !r.RiskScoreProvided && r.RiskScore != 0 {
		return fmt.Errorf("%w: 未声明风险分数来源却返回了数值", domain.ErrInvalidInput)
	}
	if r.Mode != TravelDriving && r.Mode != TravelWalking && r.Mode != TravelTransit {
		return fmt.Errorf("%w: 不支持的交通方式 %q", domain.ErrInvalidInput, r.Mode)
	}
	return nil
}
