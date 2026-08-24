package gdal

import (
	"fmt"
	"math"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const (
	DefaultBinary       = "gdal"
	DefaultVersion      = "3.13.3"
	TransformVersion    = "lhasa-gdal-1-gdal-3.13.3"
	defaultMaxGeoJSON   = 128 << 20
	defaultMaxZoneCount = 100000
	defaultMaxInput     = 512 << 20
)

var chinaBBox = [4]float64{73.5, 18.0, 135.1, 53.6}

// Config 配置 GDAL 程序、临时目录和结果资源上限。
type Config struct {
	Binary          string
	RequiredVersion string
	ArtifactRoot    string
	TemporaryDir    string
	BBox            [4]float64
	MaxInputBytes   int64
	MaxGeoJSON      int64
	MaxZoneCount    int
}

func applyDefaults(config Config) Config {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = DefaultBinary
	}
	if strings.TrimSpace(config.RequiredVersion) == "" {
		config.RequiredVersion = DefaultVersion
	}
	if config.BBox == [4]float64{} {
		config.BBox = chinaBBox
	}
	if config.MaxInputBytes <= 0 {
		config.MaxInputBytes = defaultMaxInput
	}
	if config.MaxGeoJSON <= 0 {
		config.MaxGeoJSON = defaultMaxGeoJSON
	}
	if config.MaxZoneCount <= 0 {
		config.MaxZoneCount = defaultMaxZoneCount
	}
	return config
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.ArtifactRoot) == "" {
		return fmt.Errorf("%w: 原始制品根目录为空", domain.ErrInvalidInput)
	}
	if config.MaxInputBytes < 1024 || config.MaxGeoJSON < 1024 || config.MaxZoneCount < 1 {
		return fmt.Errorf("%w: GDAL 结果资源上限无效", domain.ErrInvalidInput)
	}
	if !validBBox(config.BBox) {
		return fmt.Errorf("%w: GDAL 裁剪范围无效", domain.ErrInvalidInput)
	}
	return hazard.ValidateThresholds(defaultThresholds())
}

func validBBox(value [4]float64) bool {
	for _, coordinate := range value {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return false
		}
	}
	return value[0] >= -180 && value[2] <= 180 && value[1] >= -90 && value[3] <= 90 &&
		value[0] < value[2] && value[1] < value[3]
}

func defaultThresholds() []hazard.RiskThreshold {
	return []hazard.RiskThreshold{
		{Level: hazard.RiskLow, Minimum: 0, Maximum: 0.1, Description: "AI-GDM 派生低风险：[0,0.1]"},
		{Level: hazard.RiskModerate, Minimum: 0.1, Maximum: 0.5, Description: "AI-GDM 派生中风险：(0.1,0.5]"},
		{Level: hazard.RiskHigh, Minimum: 0.5, Maximum: 0.9, Description: "AI-GDM 派生高风险：(0.5,0.9]"},
		{Level: hazard.RiskVeryHigh, Minimum: 0.9, Maximum: 1, Description: "AI-GDM 派生极高风险：(0.9,1]"},
	}
}
