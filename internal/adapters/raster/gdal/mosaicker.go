package gdal

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Requim/AI-GDM/internal/domain"
)

// MosaicConfig 配置 Earthdata 分片拼接使用的 GDAL 版本和目标范围。
type MosaicConfig struct {
	Binary          string
	RequiredVersion string
	BBox            [4]float64
}

// Mosaicker 使用固定 GDAL 参数把同网格 TIFF 分片物化为完整栅格。
type Mosaicker struct {
	runner CommandRunner
	config MosaicConfig
	verify bool
}

// NewMosaicker 创建并校验 Earthdata 固定目标网格拼接器配置。
func NewMosaicker(config MosaicConfig) (*Mosaicker, error) {
	config = applyMosaicDefaults(config)
	if !validBBox(config.BBox) {
		return nil, fmt.Errorf("%w: GDAL 拼接目标范围无效", domain.ErrInvalidInput)
	}
	return &Mosaicker{runner: execRunner{binary: config.Binary}, config: config, verify: true}, nil
}

func applyMosaicDefaults(config MosaicConfig) MosaicConfig {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = DefaultBinary
	}
	if strings.TrimSpace(config.RequiredVersion) == "" {
		config.RequiredVersion = DefaultVersion
	}
	if config.BBox == [4]float64{} {
		config.BBox = chinaBBox
	}
	return config
}

// Mosaic 拼接全部分片，并在返回前验证完整栅格的网格、范围和概率值。
func (m *Mosaicker) Mosaic(ctx context.Context, inputs []string, output string) error {
	if len(inputs) == 0 || strings.TrimSpace(output) == "" {
		return fmt.Errorf("%w: GDAL 拼接输入或输出为空", domain.ErrInvalidInput)
	}
	workingDir := filepath.Dir(output)
	if err := m.ensureCapabilities(ctx, workingDir); err != nil {
		return err
	}
	if _, err := m.runner.Run(ctx, workingDir, mosaicArguments(inputs, output, m.config.BBox)...); err != nil {
		return fmt.Errorf("执行 GDAL 栅格拼接: %w", err)
	}
	info, err := m.runner.Run(ctx, workingDir, infoArguments(output)...)
	if err != nil {
		return fmt.Errorf("检查 GDAL 拼接栅格: %w", err)
	}
	if err = validateMosaicInfo(info, m.config.BBox); err != nil {
		return err
	}
	file, err := os.Stat(output)
	if err != nil || file.Size() == 0 {
		return fmt.Errorf("%w: GDAL 拼接结果不存在或为空", domain.ErrProviderUnavailable)
	}
	return nil
}

func (m *Mosaicker) ensureCapabilities(ctx context.Context, workingDir string) error {
	if !m.verify {
		return nil
	}
	version, err := m.runner.Run(ctx, workingDir, "--version")
	if err != nil || !strings.Contains(string(version), "GDAL "+m.config.RequiredVersion) {
		return fmt.Errorf("%w: 需要 GDAL %s", domain.ErrProviderUnavailable, m.config.RequiredVersion)
	}
	for _, command := range [][]string{{"raster", "mosaic"}, {"raster", "info"}} {
		arguments := append(append([]string(nil), command...), "--json-usage")
		if _, err = m.runner.Run(ctx, workingDir, arguments...); err != nil {
			return fmt.Errorf("%w: GDAL 子命令不可用: %s", domain.ErrProviderUnavailable, strings.Join(command, " "))
		}
	}
	return nil
}

func mosaicArguments(inputs []string, output string, bbox [4]float64) []string {
	resolution := strconv.FormatFloat(lhasaResolution, 'f', -1, 64)
	arguments := []string{
		"raster", "mosaic", "--output-format", "GTiff", "--bbox", bboxValue(bbox),
		"--resolution", resolution + "," + resolution, "--target-aligned-pixels",
		"--input-nodata", "-9999", "--output-nodata", "-9999",
		"--creation-option", "TILED=YES", "--creation-option", "COMPRESS=ZSTD",
		"--creation-option", "PREDICTOR=3", "--creation-option", "BIGTIFF=IF_SAFER", "--overwrite",
	}
	arguments = append(arguments, inputs...)
	return append(arguments, output)
}

func validateMosaicInfo(payload []byte, bbox [4]float64) error {
	if err := validateSourceRasterInfo(payload, bbox); err != nil {
		return fmt.Errorf("校验 Earthdata 拼接栅格: %w", err)
	}
	info, err := decodeRasterInfo(payload)
	if err != nil {
		return err
	}
	transform, size := transformAndSize(info)
	wantWidth := int(math.Round((bbox[2] - bbox[0]) / lhasaResolution))
	wantHeight := int(math.Round((bbox[3] - bbox[1]) / lhasaResolution))
	if len(size) != 2 || size[0] != wantWidth || size[1] != wantHeight ||
		math.Abs(transform[0]-bbox[0]) > 1e-8 || math.Abs(transform[3]-bbox[3]) > 1e-8 {
		return fmt.Errorf("%w: Earthdata 拼接栅格尺寸或原点无效", domain.ErrInvalidInput)
	}
	return nil
}
