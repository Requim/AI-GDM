package gdal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Processor 使用固定参数的 GDAL 3.13 CLI 处理 LHASA 栅格。
type Processor struct {
	runner CommandRunner
	config Config
	now    func() time.Time
	verify bool
}

var _ ports.RasterProcessor = (*Processor)(nil)

// ModelName 返回处理结果使用的模型名称。
func (p *Processor) ModelName() string { return ModelName }

// Version 返回当前 GDAL 处理算法和固定参数版本。
func (p *Processor) Version() string { return TransformVersion }

// New 创建 GDAL 栅格处理适配器。
func New(config Config) (*Processor, error) {
	config = applyDefaults(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	processor := newProcessor(config, execRunner{binary: config.Binary})
	processor.verify = true
	return processor, nil
}

func newProcessor(config Config, runner CommandRunner) *Processor {
	return &Processor{runner: runner, config: config, now: func() time.Time { return time.Now().UTC() }}
}

// Process 完成裁剪、数值校验、概率分级、矢量化和分区统计。
func (p *Processor) Process(ctx context.Context, artifact provenance.Artifact) (hazard.Snapshot, []hazard.RiskZone, error) {
	if err := validateArtifactFile(ctx, p.config, artifact); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	directory, err := os.MkdirTemp(p.config.TemporaryDir, "ai-gdm-raster-*")
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("创建 GDAL 临时目录: %w", err)
	}
	defer os.RemoveAll(directory)
	if err = p.ensureCapabilities(ctx, directory); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	paths := newPipelinePaths(directory)
	if err = p.runPipeline(ctx, artifact.LocalPath, paths); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	features, err := readFeatures(paths.statistics, p.config.MaxGeoJSON, p.config.MaxZoneCount)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	snapshot := p.snapshot(artifact)
	return snapshot, zones(snapshot, artifact, features), nil
}

func newPipelinePaths(directory string) pipelinePaths {
	return pipelinePaths{
		clipped: filepath.Join(directory, "china-clipped.tif"), classified: filepath.Join(directory, "risk-class.tif"),
		polygons: filepath.Join(directory, "risk-polygons.geojson"), statistics: filepath.Join(directory, "risk-statistics.geojson"),
		geometryErrors: filepath.Join(directory, "geometry-errors.gpkg"),
	}
}

func (p *Processor) runPipeline(ctx context.Context, input string, paths pipelinePaths) error {
	info, err := p.run(ctx, paths.clipped, infoArguments(input))
	if err != nil {
		return fmt.Errorf("检查原始 LHASA 栅格: %w", err)
	}
	if err = validateSourceRasterInfo(info, p.config.BBox); err != nil {
		return err
	}
	if _, err = p.run(ctx, paths.clipped, clipArguments(input, paths.clipped, p.config.BBox)); err != nil {
		return fmt.Errorf("裁剪中国外包范围栅格: %w", err)
	}
	info, err = p.run(ctx, paths.clipped, infoArguments(paths.clipped))
	if err != nil {
		return fmt.Errorf("检查裁剪后栅格: %w", err)
	}
	if err = validateSourceRasterInfo(info, p.config.BBox); err != nil {
		return err
	}
	if _, err = p.run(ctx, paths.clipped, classifyArguments(paths.clipped, paths.classified)); err != nil {
		return fmt.Errorf("执行 GDAL 概率分级: %w", err)
	}
	if _, err = p.run(ctx, paths.clipped, polygonizeArguments(paths.classified, paths.polygons)); err != nil {
		return fmt.Errorf("执行 GDAL 矢量化: %w", err)
	}
	count, err := countFeatures(paths.polygons, p.config.MaxGeoJSON, p.config.MaxZoneCount)
	if err != nil || count == 0 {
		return prepareEmptyStatistics(paths.statistics, count, err)
	}
	if _, err = p.run(ctx, paths.clipped, statisticsArguments(paths.clipped, paths.polygons, paths.statistics)); err != nil {
		return fmt.Errorf("执行 GDAL 分区统计: %w", err)
	}
	if _, err = p.run(ctx, paths.clipped, checkGeometryArguments(paths.statistics, paths.geometryErrors)); err != nil {
		return fmt.Errorf("执行 GDAL 几何检查: %w", err)
	}
	geometryFile, err := os.Stat(paths.geometryErrors)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("检查 GDAL 几何结果: %w", err)
	}
	if geometryFile.Size() == 0 {
		return nil
	}
	geometryInfo, err := p.run(ctx, paths.clipped, vectorInfoArguments(paths.geometryErrors))
	if err != nil {
		return fmt.Errorf("读取 GDAL 几何结果: %w", err)
	}
	return ensureNoGeometryErrors(geometryInfo)
}

func (p *Processor) run(ctx context.Context, workingDir string, arguments []string) ([]byte, error) {
	return p.runner.Run(ctx, filepath.Dir(workingDir), arguments...)
}

func (p *Processor) ensureCapabilities(ctx context.Context, workingDir string) error {
	if !p.verify {
		return nil
	}
	version, err := p.runner.Run(ctx, workingDir, "--version")
	if err != nil || !strings.Contains(string(version), "GDAL "+p.config.RequiredVersion) {
		return fmt.Errorf("%w: 需要 GDAL %s", domain.ErrProviderUnavailable, p.config.RequiredVersion)
	}
	commands := [][]string{{"raster", "info"}, {"raster", "clip"}, {"raster", "calc"}, {"raster", "polygonize"}, {"raster", "zonal-stats"}, {"vector", "check-geometry"}, {"vector", "info"}}
	for _, command := range commands {
		arguments := append(append([]string(nil), command...), "--json-usage")
		if _, err = p.runner.Run(ctx, workingDir, arguments...); err != nil {
			return fmt.Errorf("%w: GDAL 子命令不可用: %s", domain.ErrProviderUnavailable, strings.Join(command, " "))
		}
	}
	return nil
}

func (p *Processor) snapshot(artifact provenance.Artifact) hazard.Snapshot {
	checkedAt := p.now()
	source := artifact.Provenance
	source.TransformVersion = TransformVersion
	source.BBox = p.config.BBox
	status := hazard.SnapshotAvailable
	if source.IsStale(checkedAt) {
		status = hazard.SnapshotStale
	}
	return hazard.Snapshot{
		ID: p.snapshotID(artifact), HazardType: hazard.TypeLandslide, ModelName: p.ModelName(),
		ModelVersion: source.DatasetVersion, RunAt: source.ObservedAt, ValidFrom: source.ValidFrom, ValidTo: source.ValidTo,
		RasterReference:      artifact.Reference + "#sha256=" + source.SHA256,
		ProbabilitySemantics: "约 30 弧秒网格的日尺度滑坡发生概率模型估计；等级为 AI-GDM 派生且采用严格大于阈值",
		Thresholds:           defaultThresholds(), Status: status, Source: source,
		Limitations: []string{"辅助研判结果，不是中国官方预警", "当前仅为 WGS84 外包矩形预筛选，不代表中国国界或行政区"},
	}
}

func zones(snapshot hazard.Snapshot, artifact provenance.Artifact, features []feature) []hazard.RiskZone {
	values := make([]hazard.RiskZone, 0, len(features))
	for _, value := range features {
		values = append(values, hazard.RiskZone{
			ID: zoneID(snapshot.ID, value), SnapshotID: snapshot.ID,
			Geometry: value.Geometry, Minimum: value.Property.Min, Mean: value.Property.Mean,
			Maximum: value.Property.Max, Level: riskLevel(value.Property.Level), AreaCalculated: false,
			InputReferences: []string{artifact.Reference, artifact.Provenance.SHA256},
			Limitations:     []string{"面积、人口、道路和行政区暴露信息将在空间分析阶段计算"},
		})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values
}

func (p *Processor) snapshotID(artifact provenance.Artifact) string {
	payload := artifact.Provenance.SHA256 + "|" + artifact.Provenance.ObservedAt.UTC().Format(time.RFC3339Nano) + "|" +
		TransformVersion + "|" + bboxValue(p.config.BBox)
	digest := sha256.Sum256([]byte(payload))
	return "lhasa-" + hex.EncodeToString(digest[:8])
}
