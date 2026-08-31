package gdal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
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

// Process 完成外包范围预裁剪、行政边界精确裁剪、概率分级和分区统计。
func (p *Processor) Process(ctx context.Context, artifact provenance.Artifact,
	boundary hazard.ProcessingBoundary,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if err := validateArtifactFile(ctx, p.config, artifact); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	if err := boundary.Validate(); err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("校验 GDAL 风险处理边界: %w", err)
	}
	if err := validateBoundaryForBBox(boundary.Geometry, p.config.BBox); err != nil {
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
	if err = writeBoundaryGeoJSON(paths.boundary, boundary); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	stats, err := p.runPipeline(ctx, artifact.LocalPath, paths)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	features := []feature{}
	if stats.clippedZoneCount > 0 {
		features, err = readFeatureFiles(paths.statistics[:], statisticsLevels[:],
			p.config.MaxGeoJSON, p.config.MaxZoneCount)
		if err != nil {
			return hazard.Snapshot{}, nil, err
		}
		if len(features) != stats.clippedZoneCount {
			return hazard.Snapshot{}, nil, fmt.Errorf("%w: 统计风险区数量与裁剪结果不一致", domain.ErrInvalidInput)
		}
	}
	snapshot := p.snapshot(artifact, boundary, stats)
	return snapshot, zones(snapshot, artifact, boundary, features), nil
}

func newPipelinePaths(directory string) pipelinePaths {
	paths := pipelinePaths{
		clipped: filepath.Join(directory, "china-clipped.tif"), classified: filepath.Join(directory, "risk-class.tif"),
		boundary:       filepath.Join(directory, "china-adm0.geojson"),
		rawPolygons:    filepath.Join(directory, "risk-polygons-raw.geojson"),
		polygons:       filepath.Join(directory, "risk-polygons-china.geojson"),
		boundaryErrors: filepath.Join(directory, "boundary-errors.gpkg"),
		geometryErrors: filepath.Join(directory, "geometry-errors.gpkg"),
	}
	for index, level := range statisticsLevels {
		paths.levelPolygons[index] = filepath.Join(directory, fmt.Sprintf("risk-polygons-level-%d.geojson", level))
		paths.levelRaster[index] = filepath.Join(directory, fmt.Sprintf("risk-probability-level-%d.tif", level))
		paths.statistics[index] = filepath.Join(directory, fmt.Sprintf("risk-statistics-level-%d.geojson", level))
	}
	return paths
}

type pipelineStats struct {
	rawZoneCount     int
	clippedZoneCount int
}

func (p *Processor) runPipeline(ctx context.Context, input string,
	paths pipelinePaths,
) (pipelineStats, error) {
	if err := p.checkBoundaryGeometry(ctx, paths); err != nil {
		return pipelineStats{}, err
	}
	if err := p.prepareClassifiedRaster(ctx, input, paths); err != nil {
		return pipelineStats{}, err
	}
	rawCount, clippedCount, err := p.prepareClippedPolygons(ctx, paths)
	stats := pipelineStats{rawZoneCount: rawCount, clippedZoneCount: clippedCount}
	if err != nil || clippedCount == 0 {
		return stats, err
	}
	if err = p.checkGeometry(ctx, paths); err != nil {
		return stats, err
	}
	return stats, p.calculateStatistics(ctx, paths, clippedCount)
}

func (p *Processor) prepareClassifiedRaster(ctx context.Context, input string,
	paths pipelinePaths,
) error {
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
	return nil
}

func (p *Processor) prepareClippedPolygons(ctx context.Context,
	paths pipelinePaths,
) (int, int, error) {
	if _, err := p.run(ctx, paths.clipped, polygonizeArguments(paths.classified, paths.rawPolygons)); err != nil {
		return 0, 0, fmt.Errorf("执行 GDAL 矢量化: %w", err)
	}
	rawCount, err := countFeatures(paths.rawPolygons, p.config.MaxGeoJSON, p.config.MaxZoneCount)
	if err != nil || rawCount == 0 {
		return rawCount, 0, err
	}
	if _, err = p.run(ctx, paths.clipped,
		vectorClipArguments(paths.rawPolygons, paths.boundary, paths.polygons)); err != nil {
		return rawCount, 0, fmt.Errorf("按中国 ADM0 边界裁剪风险区: %w", err)
	}
	clippedCount, err := countFeatures(paths.polygons, p.config.MaxGeoJSON, p.config.MaxZoneCount)
	return rawCount, clippedCount, err
}

func (p *Processor) calculateStatistics(ctx context.Context, paths pipelinePaths, expected int) error {
	total := 0
	for index, level := range statisticsLevels {
		count, err := p.calculateLevelStatistics(ctx, paths, index, level)
		if err != nil {
			return err
		}
		total += count
	}
	if total != expected {
		return fmt.Errorf("%w: 分级风险区数量 %d 与裁剪后数量 %d 不一致", domain.ErrInvalidInput, total, expected)
	}
	return nil
}

func (p *Processor) calculateLevelStatistics(ctx context.Context, paths pipelinePaths,
	index, level int,
) (int, error) {
	if _, err := p.run(ctx, paths.clipped,
		levelFilterArguments(paths.polygons, paths.levelPolygons[index], level)); err != nil {
		return 0, fmt.Errorf("筛选第 %d 级风险区: %w", level, err)
	}
	count, err := countFeatures(paths.levelPolygons[index], p.config.MaxGeoJSON, p.config.MaxZoneCount)
	if err != nil || count == 0 {
		return count, prepareEmptyStatistics(paths.statistics[index], count, err)
	}
	if _, err = p.run(ctx, paths.clipped, levelProbabilityArguments(paths.clipped,
		paths.classified, paths.levelRaster[index], level)); err != nil {
		return 0, fmt.Errorf("生成第 %d 级概率栅格: %w", level, err)
	}
	if _, err = p.run(ctx, paths.clipped, statisticsArguments(paths.levelRaster[index],
		paths.levelPolygons[index], paths.statistics[index])); err != nil {
		return 0, fmt.Errorf("执行第 %d 级 GDAL 分区统计: %w", level, err)
	}
	statisticsCount, err := countFeatures(paths.statistics[index], p.config.MaxGeoJSON, p.config.MaxZoneCount)
	if err != nil {
		return 0, err
	}
	if statisticsCount != count {
		return 0, fmt.Errorf("%w: 第 %d 级统计输出 %d 与输入 %d 不一致",
			domain.ErrInvalidInput, level, statisticsCount, count)
	}
	return count, nil
}

func (p *Processor) checkGeometry(ctx context.Context, paths pipelinePaths) error {
	return p.checkGeometryFile(ctx, paths.clipped, paths.polygons,
		paths.geometryErrors, "风险区")
}

func (p *Processor) checkBoundaryGeometry(ctx context.Context, paths pipelinePaths) error {
	return p.checkGeometryFile(ctx, paths.clipped, paths.boundary,
		paths.boundaryErrors, "中国 ADM0 边界")
}

func (p *Processor) checkGeometryFile(ctx context.Context, workingPath, input,
	output, label string,
) error {
	if _, err := p.run(ctx, workingPath, checkGeometryArguments(input, output)); err != nil {
		return fmt.Errorf("执行 GDAL %s几何检查: %w", label, err)
	}
	geometryFile, err := os.Stat(output)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查 GDAL %s几何结果: %w", label, err)
	}
	if geometryFile.Size() == 0 {
		return nil
	}
	geometryInfo, err := p.run(ctx, workingPath, vectorInfoArguments(output))
	if err != nil {
		return fmt.Errorf("读取 GDAL %s几何结果: %w", label, err)
	}
	return ensureNoGeometryErrors(geometryInfo)
}

type boundaryFeatureCollection struct {
	Type     string            `json:"type"`
	Features []boundaryFeature `json:"features"`
}

type boundaryFeature struct {
	Type       string             `json:"type"`
	Properties boundaryProperties `json:"properties"`
	Geometry   spatial.Geometry   `json:"geometry"`
}

type boundaryProperties struct {
	BoundaryID string `json:"boundaryId"`
	RegionCode string `json:"regionCode"`
}

func writeBoundaryGeoJSON(path string, boundary hazard.ProcessingBoundary) error {
	payload, err := json.Marshal(boundaryFeatureCollection{Type: "FeatureCollection",
		Features: []boundaryFeature{{Type: "Feature",
			Properties: boundaryProperties{BoundaryID: boundary.Coverage.BoundaryID,
				RegionCode: boundary.Coverage.RegionCode}, Geometry: boundary.Geometry}}})
	if err != nil {
		return fmt.Errorf("编码中国 ADM0 处理边界: %w", err)
	}
	if err = os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("写入中国 ADM0 处理边界: %w", err)
	}
	return nil
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
	commands := [][]string{{"raster", "info"}, {"raster", "clip"}, {"raster", "calc"},
		{"raster", "polygonize"}, {"raster", "zonal-stats"}, {"vector", "clip"},
		{"vector", "filter"}, {"vector", "check-geometry"}, {"vector", "info"}}
	for _, command := range commands {
		arguments := append(append([]string(nil), command...), "--json-usage")
		if _, err = p.runner.Run(ctx, workingDir, arguments...); err != nil {
			return fmt.Errorf("%w: GDAL 子命令不可用: %s", domain.ErrProviderUnavailable, strings.Join(command, " "))
		}
	}
	return nil
}

func (p *Processor) snapshot(artifact provenance.Artifact,
	boundary hazard.ProcessingBoundary, stats pipelineStats,
) hazard.Snapshot {
	checkedAt := p.now()
	source := artifact.Provenance
	source.TransformVersion = TransformVersion
	source.BBox = p.config.BBox
	status := hazard.SnapshotAvailable
	if source.IsStale(checkedAt) {
		status = hazard.SnapshotStale
	}
	return hazard.Snapshot{
		ID: p.snapshotID(artifact, boundary), HazardType: hazard.TypeLandslide, ModelName: p.ModelName(),
		ModelVersion: source.DatasetVersion, RunAt: checkedAt, ValidFrom: source.ValidFrom, ValidTo: source.ValidTo,
		RasterReference:      artifact.Reference + "#sha256=" + source.SHA256,
		ProbabilitySemantics: "固定 30 弧秒目标网格最近邻导出的日尺度滑坡发生概率模型估计；等级按严格大于阈值派生，边界均值按像元相交比例加权，最小和最大值取相交的同等级像元",
		Thresholds:           defaultThresholds(), Status: status, Source: source,
		Coverage: &boundary.Coverage,
		Limitations: []string{
			"辅助研判结果，不是中国官方预警",
			"RunAt 表示 AI-GDM 本地确定性处理时刻，NASA 精确模型运行时刻未知",
			"数据先按 WGS84 中国外接矩形下载，再按版本化 CHN ADM0 行政边界精确裁剪",
			"国界处亚像元风险区的均值按几何相交比例加权，最小和最大值取所有相交的同等级像元",
			"geoBoundaries 公开数据仅用于风险计算范围约束，不作为中国法定国界或官方地图依据",
			boundaryCountLimitation(stats),
		},
	}
}

func boundaryCountLimitation(stats pipelineStats) string {
	if stats.rawZoneCount == 0 {
		return "中国外接矩形内未生成达到阈值的风险区"
	}
	if stats.clippedZoneCount == 0 {
		return fmt.Sprintf("外接矩形内 %d 个风险区均位于 CHN ADM0 边界外，该边界范围内保留 0 个",
			stats.rawZoneCount)
	}
	return fmt.Sprintf("外接矩形分级生成 %d 个风险区，CHN ADM0 裁剪后该边界范围内保留 %d 个",
		stats.rawZoneCount, stats.clippedZoneCount)
}

func zones(snapshot hazard.Snapshot, artifact provenance.Artifact,
	boundary hazard.ProcessingBoundary, features []feature,
) []hazard.RiskZone {
	inputs := zoneInputReferences(artifact, boundary)
	values := make([]hazard.RiskZone, 0, len(features))
	for _, value := range features {
		minimum, mean, maximum := featureStatistics(value)
		values = append(values, hazard.RiskZone{
			ID: zoneID(snapshot.ID, value), SnapshotID: snapshot.ID,
			Geometry: value.Geometry, Minimum: minimum, Mean: mean,
			Maximum: maximum, Level: riskLevel(value.Property.Level), AreaCalculated: false,
			AdminCodes: []string{boundary.Coverage.RegionCode}, InputReferences: append([]string(nil), inputs...),
			Limitations: []string{"面积、人口、道路和行政区暴露信息将在空间分析阶段计算"},
		})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values
}

func zoneInputReferences(artifact provenance.Artifact,
	boundary hazard.ProcessingBoundary,
) []string {
	values := []string{artifact.Reference, artifact.Provenance.SHA256,
		boundary.Coverage.Reference, boundary.Coverage.SHA256, boundary.Coverage.GeometrySHA256}
	return values
}

func (p *Processor) snapshotID(artifact provenance.Artifact,
	boundary hazard.ProcessingBoundary,
) string {
	payload := artifact.Provenance.SHA256 + "|" + artifact.Provenance.SourceRevision + "|" +
		TransformVersion + "|" + bboxValue(p.config.BBox) + "|" + boundary.Coverage.Identity()
	digest := sha256.Sum256([]byte(payload))
	return "lhasa-" + hex.EncodeToString(digest[:8])
}
