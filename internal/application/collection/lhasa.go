package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	lhasaFallbackQualityFlag        = "fallback_last_success"
	lhasaBoundaryFallbackFlag       = "fallback_boundary_identity_unverified"
	lhasaProviderName               = "NASA Earthdata GIS"
	lhasaDatasetName                = "LHASA Hazard Today"
	lhasaBoundaryFallbackLimitation = "本次未能刷新行政边界身份，沿用最后成功快照的已绑定边界"
)

// LHASACollector 获取、处理并原子保存 LHASA 风险分析。
type LHASACollector struct {
	discovery ports.ArtifactDiscovery
	fetcher   ports.ArtifactFetcher
	boundary  ports.HazardBoundaryProvider
	processor ports.RasterProcessor
	writer    ports.HazardAnalysisWriter
	reader    ports.HazardAnalysisReader
	locker    ports.HazardAnalysisRefreshLocker
	clock     ports.Clock
	maxAge    time.Duration
}

// NewLHASACollector 创建支持最后成功分析回退的 LHASA 采集用例。
func NewLHASACollector(discovery ports.ArtifactDiscovery, fetcher ports.ArtifactFetcher,
	boundary ports.HazardBoundaryProvider, processor ports.RasterProcessor, writer ports.HazardAnalysisWriter,
	reader ports.HazardAnalysisReader, locker ports.HazardAnalysisRefreshLocker,
	clock ports.Clock, maxAge time.Duration,
) (*LHASACollector, error) {
	if discovery == nil || fetcher == nil || boundary == nil || processor == nil || writer == nil ||
		reader == nil || locker == nil || clock == nil || maxAge <= 0 {
		return nil, fmt.Errorf("%w: LHASA 采集依赖或回退时效无效", domain.ErrInvalidInput)
	}
	return &LHASACollector{
		discovery: discovery, fetcher: fetcher, boundary: boundary, processor: processor,
		writer: writer, reader: reader, locker: locker, clock: clock, maxAge: maxAge,
	}, nil
}

// Collect 刷新 LHASA 分析；失败时只返回未超过时效的最后成功完整分析。
func (c *LHASACollector) Collect(ctx context.Context) (
	snapshot hazard.Snapshot, zones []hazard.RiskZone, err error,
) {
	lease, err := c.locker.LockAnalysisRefresh(ctx, c.analysisSelector())
	if err != nil {
		return hazard.Snapshot{}, nil, unavailableLHASA(fmt.Errorf("获取 LHASA 分析刷新锁: %w", err))
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			snapshot, zones = hazard.Snapshot{}, nil
			err = errors.Join(err, fmt.Errorf("释放 LHASA 分析刷新锁: %w", releaseErr))
		}
	}()
	return c.collectLocked(ctx)
}

func (c *LHASACollector) collectLocked(ctx context.Context) (hazard.Snapshot, []hazard.RiskZone, error) {
	previous := c.readLatest(ctx)
	boundary, err := c.readBoundary(ctx)
	if err != nil {
		return c.fallbackBoundary(previous, err)
	}
	coverageChanged := previous.err == nil && !sameCoverage(previous.snapshot.Coverage, boundary.Coverage)
	if err = c.reconcileCoverage(ctx, boundary); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	previous = c.readLatest(ctx)
	if previous.err == nil && !sameCoverage(previous.snapshot.Coverage, boundary.Coverage) {
		return hazard.Snapshot{}, nil, unavailableLHASA(
			fmt.Errorf("%w: 最新 LHASA 分析未切换到当前行政边界", domain.ErrInsufficientData))
	}
	artifact, err := c.discovery.DiscoverLatest(ctx)
	if err != nil {
		return c.fallbackAfterCoverageChange(previous, coverageChanged,
			fmt.Errorf("发现最新 LHASA 制品: %w", err))
	}
	artifact = reconcileArtifactRevision(artifact, previous)
	if err = c.validateArtifact(artifact); err != nil {
		return c.fallbackAfterCoverageChange(previous, coverageChanged, err)
	}
	if snapshot, zones, ok := c.reuseLatest(previous, artifact, boundary); ok {
		return snapshot, zones, nil
	}
	snapshot, zones, err := c.collectFresh(ctx, artifact, boundary)
	if err != nil {
		return c.fallbackAfterCoverageChange(previous, coverageChanged, err)
	}
	return snapshot, zones, nil
}

func (c *LHASACollector) collectFresh(ctx context.Context,
	artifact provenance.Artifact, boundary hazard.ProcessingBoundary,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	stored, err := c.fetcher.Fetch(ctx, artifact)
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("下载 LHASA 制品: %w", err)
	}
	snapshot, zones, err := c.processor.Process(ctx, stored, boundary)
	if err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("处理 LHASA 栅格: %w", err)
	}
	if err = c.validateAnalysis(snapshot, zones); err != nil {
		return hazard.Snapshot{}, nil, err
	}
	if err = c.writer.SaveAnalysis(ctx, snapshot, zones); err != nil {
		return hazard.Snapshot{}, nil, fmt.Errorf("保存 LHASA 分析: %w", err)
	}
	return snapshot, zones, nil
}

func (c *LHASACollector) reuseLatest(previous lhasaAnalysis,
	artifact provenance.Artifact, boundary hazard.ProcessingBoundary,
) (hazard.Snapshot, []hazard.RiskZone, bool) {
	if previous.err != nil || !sameLHASAArtifact(previous.snapshot, artifact) ||
		!sameCoverage(previous.snapshot.Coverage, boundary.Coverage) {
		return hazard.Snapshot{}, nil, false
	}
	if c.validateAnalysis(previous.snapshot, previous.zones) != nil {
		return hazard.Snapshot{}, nil, false
	}
	return previous.snapshot, previous.zones, true
}

func (c *LHASACollector) reconcileCoverage(ctx context.Context,
	boundary hazard.ProcessingBoundary,
) error {
	err := c.writer.ReconcileAnalysisCoverage(ctx, c.analysisSelector(), boundary.Coverage, c.clock.Now().UTC())
	if err != nil {
		return unavailableLHASA(fmt.Errorf("协调 LHASA 行政边界范围: %w", err))
	}
	return nil
}

func (c *LHASACollector) readBoundary(ctx context.Context) (hazard.ProcessingBoundary, error) {
	boundary, err := c.boundary.RiskBoundary(ctx)
	if err != nil {
		return hazard.ProcessingBoundary{}, fmt.Errorf("读取中国风险处理边界: %w", err)
	}
	if err = boundary.Validate(); err != nil || boundary.Coverage.RegionCode != "CN" ||
		boundary.Coverage.BoundaryType != "ADM0" {
		return hazard.ProcessingBoundary{}, fmt.Errorf("%w: 中国风险处理边界无效", domain.ErrInvalidInput)
	}
	now := c.clock.Now()
	if now.IsZero() || boundary.Coverage.CollectedAt.After(now) {
		return hazard.ProcessingBoundary{}, fmt.Errorf("%w: 中国风险处理边界采集时间晚于当前时间",
			domain.ErrInsufficientData)
	}
	return boundary, nil
}

func (c *LHASACollector) fallbackBoundary(previous lhasaAnalysis, cause error,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	snapshot, zones, err := c.fallback(previous, cause)
	if err != nil {
		return hazard.Snapshot{}, nil, err
	}
	snapshot.Source.QualityFlags = appendUnique(snapshot.Source.QualityFlags, lhasaBoundaryFallbackFlag)
	snapshot.Limitations = appendUnique(snapshot.Limitations, lhasaBoundaryFallbackLimitation)
	return snapshot, zones, nil
}

func (c *LHASACollector) fallbackAfterCoverageChange(previous lhasaAnalysis,
	coverageChanged bool, cause error,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if coverageChanged && previous.err != nil {
		return hazard.Snapshot{}, nil, unavailableLHASA(cause,
			fmt.Errorf("%w: 行政边界版本已变化，禁止回退到旧范围", domain.ErrInsufficientData))
	}
	return c.fallback(previous, cause)
}

func (c *LHASACollector) fallback(previous lhasaAnalysis, cause error,
) (hazard.Snapshot, []hazard.RiskZone, error) {
	if previous.err != nil {
		return hazard.Snapshot{}, nil, unavailableLHASA(cause,
			fmt.Errorf("读取最后成功 LHASA 分析: %w", previous.err))
	}
	if err := c.validateAnalysis(previous.snapshot, previous.zones); err != nil {
		return hazard.Snapshot{}, nil, unavailableLHASA(cause, err)
	}
	snapshot, zones := markLHASAFallback(previous.snapshot, previous.zones)
	return snapshot, zones, nil
}

type lhasaAnalysis struct {
	snapshot hazard.Snapshot
	zones    []hazard.RiskZone
	err      error
}

func (c *LHASACollector) readLatest(ctx context.Context) lhasaAnalysis {
	snapshot, zones, err := c.reader.LatestAnalysis(ctx, c.analysisSelector())
	return lhasaAnalysis{snapshot: snapshot, zones: zones, err: err}
}

func (c *LHASACollector) analysisSelector() hazard.AnalysisSelector {
	return hazard.AnalysisSelector{
		HazardType: hazard.TypeLandslide, ModelName: c.processor.ModelName(),
		TransformVersion: c.processor.Version(), Provider: lhasaProviderName, Dataset: lhasaDatasetName,
	}
}

func (c *LHASACollector) validateArtifact(artifact provenance.Artifact) error {
	if err := artifact.Validate(); err != nil {
		return fmt.Errorf("校验 LHASA 制品描述: %w", err)
	}
	if artifact.Provenance.Provider != lhasaProviderName ||
		artifact.Provenance.Dataset != lhasaDatasetName ||
		artifact.Provenance.DataKind != provenance.DataKindNowcast ||
		artifact.Provenance.SourceRevision == "" || artifact.Provenance.RevisionFirstSeenAt.IsZero() {
		return fmt.Errorf("%w: LHASA 制品供应商、数据集或数据分类无效", domain.ErrInvalidInput)
	}
	if provenanceExpired(artifact.Provenance, c.clock.Now().UTC(), c.maxAge) {
		return fmt.Errorf("%w: LHASA 最新制品已超过 %s", domain.ErrInsufficientData, c.maxAge)
	}
	return nil
}

func (c *LHASACollector) validateAnalysis(snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	if snapshot.ID == "" || snapshot.HazardType != hazard.TypeLandslide ||
		snapshot.ModelName != c.processor.ModelName() || snapshot.Source.TransformVersion != c.processor.Version() {
		return fmt.Errorf("%w: LHASA 分析身份或处理版本无效", domain.ErrInvalidInput)
	}
	if snapshot.Status != hazard.SnapshotAvailable && snapshot.Status != hazard.SnapshotStale {
		return fmt.Errorf("%w: LHASA 分析状态不可用", domain.ErrInsufficientData)
	}
	if err := snapshot.Source.Validate(); err != nil {
		return fmt.Errorf("校验 LHASA 分析来源: %w", err)
	}
	if snapshot.Coverage == nil || snapshot.Coverage.Validate() != nil ||
		snapshot.Coverage.RegionCode != "CN" || snapshot.Coverage.BoundaryType != "ADM0" {
		return fmt.Errorf("%w: LHASA 分析覆盖范围无效", domain.ErrInvalidInput)
	}
	if snapshot.Coverage.CollectedAt.After(snapshot.RunAt) {
		return fmt.Errorf("%w: LHASA 分析覆盖范围时间晚于处理时间", domain.ErrInvalidInput)
	}
	if snapshot.Source.Provider != lhasaProviderName || snapshot.Source.Dataset != lhasaDatasetName ||
		snapshot.Source.SourceRevision == "" || snapshot.Source.RevisionFirstSeenAt.IsZero() {
		return fmt.Errorf("%w: LHASA 分析供应商或数据集无效", domain.ErrInvalidInput)
	}
	if err := validateSnapshotShape(snapshot); err != nil {
		return err
	}
	if provenanceExpired(snapshot.Source, c.clock.Now().UTC(), c.maxAge) {
		return fmt.Errorf("%w: 最后成功 LHASA 分析已超过 %s", domain.ErrInsufficientData, c.maxAge)
	}
	return validateZoneOwnership(snapshot.ID, zones)
}

func provenanceExpired(source provenance.Provenance, now time.Time, maxAge time.Duration) bool {
	anchor := source.RevisionFirstSeenAt
	if anchor.IsZero() {
		anchor = source.ObservedAt
	}
	if anchor.IsZero() || anchor.After(now.Add(time.Hour)) {
		return true
	}
	return source.IsStale(now) || now.Sub(anchor) > maxAge
}

func validateZoneOwnership(snapshotID string, zones []hazard.RiskZone) error {
	seen := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		if zone.ID == "" || zone.SnapshotID != snapshotID {
			return fmt.Errorf("%w: LHASA 风险区不属于当前快照", domain.ErrInvalidInput)
		}
		if _, exists := seen[zone.ID]; exists {
			return fmt.Errorf("%w: LHASA 风险区标识重复", domain.ErrInvalidInput)
		}
		seen[zone.ID] = struct{}{}
	}
	return nil
}

func validateSnapshotShape(snapshot hazard.Snapshot) error {
	if snapshot.ModelVersion == "" || snapshot.RasterReference == "" || snapshot.RunAt.IsZero() ||
		snapshot.ValidFrom.IsZero() || snapshot.ValidTo.IsZero() || snapshot.ValidTo.Before(snapshot.ValidFrom) {
		return fmt.Errorf("%w: LHASA 快照字段或有效期无效", domain.ErrInvalidInput)
	}
	for _, value := range []time.Time{snapshot.RunAt, snapshot.ValidFrom, snapshot.ValidTo} {
		_, offset := value.Zone()
		if offset != 0 {
			return fmt.Errorf("%w: LHASA 快照时间必须使用 UTC", domain.ErrInvalidInput)
		}
	}
	return hazard.ValidateThresholds(snapshot.Thresholds)
}

func sameLHASAArtifact(snapshot hazard.Snapshot, artifact provenance.Artifact) bool {
	return snapshot.Source.SourceURI == artifact.Reference &&
		snapshot.Source.DatasetVersion == artifact.Provenance.DatasetVersion &&
		snapshot.Source.SourceRevision == artifact.Provenance.SourceRevision &&
		snapshot.Source.RevisionFirstSeenAt.Equal(artifact.Provenance.RevisionFirstSeenAt)
}

func sameCoverage(previous *hazard.Coverage, current hazard.Coverage) bool {
	return previous != nil && previous.Identity() == current.Identity()
}

func reconcileArtifactRevision(artifact provenance.Artifact, previous lhasaAnalysis) provenance.Artifact {
	if previous.err != nil || !sameSourceRevision(previous.snapshot.Source, artifact.Provenance) {
		return artifact
	}
	prior, current := previous.snapshot.Source, artifact.Provenance
	current.ObservedAt, current.PublishedAt = prior.ObservedAt, prior.PublishedAt
	current.RevisionFirstSeenAt = prior.RevisionFirstSeenAt
	current.ValidFrom, current.ValidTo = prior.ValidFrom, prior.ValidTo
	current.QualityFlags = mergeUniqueValues(prior.QualityFlags, current.QualityFlags)
	current.Limitations = mergeUniqueValues(prior.Limitations, current.Limitations)
	artifact.Provenance = current
	return artifact
}

func sameSourceRevision(previous, current provenance.Provenance) bool {
	return current.SourceRevision != "" && previous.Provider == current.Provider &&
		previous.Dataset == current.Dataset && previous.DatasetVersion == current.DatasetVersion &&
		previous.SourceURI == current.SourceURI && previous.SourceRevision == current.SourceRevision
}

func mergeUniqueValues(first, second []string) []string {
	result := append([]string(nil), first...)
	for _, value := range second {
		result = appendUnique(result, value)
	}
	return result
}

func markLHASAFallback(snapshot hazard.Snapshot,
	zones []hazard.RiskZone,
) (hazard.Snapshot, []hazard.RiskZone) {
	result := cloneSnapshot(snapshot)
	result.Status = hazard.SnapshotStale
	result.Source.Stale = true
	result.Source.QualityFlags = appendUnique(result.Source.QualityFlags, lhasaFallbackQualityFlag)
	result.Source.Limitations = appendUnique(result.Source.Limitations, "实时采集失败，使用最后成功 LHASA 分析")
	result.Limitations = appendUnique(result.Limitations, "本次刷新使用未超过时效的最后成功结果")
	return result, cloneZones(zones)
}

func cloneSnapshot(value hazard.Snapshot) hazard.Snapshot {
	value.Thresholds = append([]hazard.RiskThreshold(nil), value.Thresholds...)
	value.Limitations = append([]string(nil), value.Limitations...)
	value.Source.QualityFlags = append([]string(nil), value.Source.QualityFlags...)
	value.Source.Limitations = append([]string(nil), value.Source.Limitations...)
	value.Source.SourceParts = append([]provenance.SourcePart(nil), value.Source.SourceParts...)
	if value.Coverage != nil {
		coverage := *value.Coverage
		value.Coverage = &coverage
	}
	return value
}

func cloneZones(values []hazard.RiskZone) []hazard.RiskZone {
	result := make([]hazard.RiskZone, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Geometry.Coordinates = append(json.RawMessage(nil), value.Geometry.Coordinates...)
		result[index].AdminCodes = append([]string(nil), value.AdminCodes...)
		result[index].InputReferences = append([]string(nil), value.InputReferences...)
		result[index].Limitations = append([]string(nil), value.Limitations...)
	}
	return result
}

func unavailableLHASA(causes ...error) error {
	values := append([]error{domain.ErrInsufficientData}, causes...)
	return fmt.Errorf("LHASA 实时风险不可用: %w", errors.Join(values...))
}
