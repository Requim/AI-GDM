package gdal

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
)

const lhasaResolution = 1.0 / 120.0

type authorityID struct {
	Authority string `json:"authority"`
	Code      int    `json:"code"`
}

type rasterBand struct {
	Type            string   `json:"type"`
	NoDataValue     *float64 `json:"noDataValue"`
	Minimum         *float64 `json:"minimum"`
	Maximum         *float64 `json:"maximum"`
	ComputedMinimum *float64 `json:"computedMin"`
	ComputedMaximum *float64 `json:"computedMax"`
}

type rasterInfo struct {
	Driver           string    `json:"driver"`
	DriverShortName  string    `json:"driverShortName"`
	Size             []int     `json:"size"`
	GeoTransform     []float64 `json:"geoTransform"`
	CoordinateSystem struct {
		ProjJSON struct {
			ID authorityID `json:"id"`
		} `json:"projjson"`
	} `json:"coordinateSystem"`
	STAC struct {
		ProjJSON struct {
			ID authorityID `json:"id"`
		} `json:"proj:projjson"`
		Shape []int `json:"proj:shape"`
	} `json:"stac"`
	Bands []rasterBand `json:"bands"`
}

func validateSourceRasterInfo(payload []byte, bbox [4]float64) error {
	info, err := decodeRasterInfo(payload)
	if err != nil {
		return err
	}
	if !isGTiff(info) || len(info.Bands) != 1 || !validBandType(info.Bands[0].Type) {
		return fmt.Errorf("%w: LHASA 栅格驱动、波段数或数据类型无效", domain.ErrInvalidInput)
	}
	if !isEPSG4326(info) || !validNoData(info.Bands[0].NoDataValue) {
		return fmt.Errorf("%w: LHASA 栅格 CRS 或 NoData 无效", domain.ErrInvalidInput)
	}
	transform, size := transformAndSize(info)
	if !validGrid(transform, size) || !coversBBox(transform, size, bbox) {
		return fmt.Errorf("%w: LHASA 栅格分辨率、网格或覆盖范围无效", domain.ErrInvalidInput)
	}
	minimum, maximum := probabilityRange(info.Bands[0])
	return validateProbabilityRange(minimum, maximum)
}

func decodeRasterInfo(payload []byte) (rasterInfo, error) {
	var info rasterInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return rasterInfo{}, fmt.Errorf("%w: 解析 GDAL 栅格信息: %v", domain.ErrInvalidInput, err)
	}
	return info, nil
}

func isGTiff(info rasterInfo) bool {
	driver := info.Driver
	if driver == "" {
		driver = info.DriverShortName
	}
	return strings.EqualFold(driver, "GTiff")
}

func validBandType(value string) bool {
	return value == "Float32" || value == "Float64"
}

func isEPSG4326(info rasterInfo) bool {
	id := info.STAC.ProjJSON.ID
	if id.Code == 0 {
		id = info.CoordinateSystem.ProjJSON.ID
	}
	return strings.EqualFold(id.Authority, "EPSG") && id.Code == 4326
}

func validNoData(value *float64) bool {
	return value != nil && math.Abs(*value-(-9999)) < 1e-9
}

func transformAndSize(info rasterInfo) ([]float64, []int) {
	transform := info.GeoTransform
	size := info.Size
	if len(size) == 0 {
		size = info.STAC.Shape
	}
	return transform, size
}

func probabilityRange(band rasterBand) (*float64, *float64) {
	minimum, maximum := band.Minimum, band.Maximum
	if minimum == nil {
		minimum = band.ComputedMinimum
	}
	if maximum == nil {
		maximum = band.ComputedMaximum
	}
	return minimum, maximum
}

func validGrid(transform []float64, size []int) bool {
	if len(transform) != 6 || len(size) != 2 || size[0] < 1 || size[1] < 1 || int64(size[0])*int64(size[1]) > 1_000_000_000 {
		return false
	}
	if math.Abs(transform[1]-lhasaResolution) > 1e-8 || math.Abs(transform[5]+lhasaResolution) > 1e-8 {
		return false
	}
	return math.Abs(transform[2]) < 1e-12 && math.Abs(transform[4]) < 1e-12 && alignedOrigin(transform)
}

func alignedOrigin(transform []float64) bool {
	x := (transform[0] + 180) / lhasaResolution
	y := (90 - transform[3]) / lhasaResolution
	return math.Abs(x-math.Round(x)) < 1e-5 && math.Abs(y-math.Round(y)) < 1e-5
}

func coversBBox(transform []float64, size []int, bbox [4]float64) bool {
	minimumX := transform[0]
	maximumY := transform[3]
	maximumX := minimumX + float64(size[0])*transform[1]
	minimumY := maximumY + float64(size[1])*transform[5]
	return minimumX <= bbox[0]+1e-8 && minimumY <= bbox[1]+1e-8 &&
		maximumX >= bbox[2]-1e-8 && maximumY >= bbox[3]-1e-8
}

func validateProbabilityRange(minimum, maximum *float64) error {
	if minimum == nil || maximum == nil || math.IsNaN(*minimum) || math.IsNaN(*maximum) || math.IsInf(*minimum, 0) || math.IsInf(*maximum, 0) {
		return fmt.Errorf("%w: LHASA 栅格没有可用概率值", domain.ErrInsufficientData)
	}
	if *minimum < 0 || *maximum > 1 || *minimum > *maximum {
		return fmt.Errorf("%w: LHASA 概率值超出零到一范围", domain.ErrInvalidInput)
	}
	return nil
}
