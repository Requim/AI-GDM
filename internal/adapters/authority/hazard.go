package authority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	hazardCachePrefix       = "authority:hazard:"
	maxHazardAuthorityZones = 1_000
	maxHazardGeometryPoints = 200_000
	maxHazardGeometryBytes  = 8 << 20
	maxHazardCacheTTL       = 24 * time.Hour
)

type hazardAuthorityRecord struct {
	Analysis          report.HazardAuthorityAnalysis `json:"analysis"`
	AssessmentID      string                         `json:"assessmentId"`
	AssessmentAt      time.Time                      `json:"assessmentAt"`
	SpatialAnalysisID string                         `json:"spatialAnalysisId"`
	ValidTo           time.Time                      `json:"validTo"`
	Digest            string                         `json:"digest"`
}

func (r *Resolver) resolveHazard(ctx context.Context, id string) (report.Authority, error) {
	if r.cache == nil {
		return report.Authority{}, fmt.Errorf("%w: 风险权威缓存未配置", domain.ErrProviderUnavailable)
	}
	record, found, err := r.readHazardRecord(ctx, id)
	if err != nil {
		return report.Authority{}, err
	}
	if found {
		return r.authorityFromHazardRecord(id, record)
	}
	read, err := r.risks.ReadAuthority(ctx, id, defaultHazardLimits())
	if err != nil {
		return report.Authority{}, fmt.Errorf("读取风险权威快照 %s: %w", id, err)
	}
	analysis, err := r.spatial.LatestBySnapshot(ctx, id)
	if err != nil {
		return report.Authority{}, wrapSpatialReadError(id, err)
	}
	record, err = newHazardRecord(id, read, analysis)
	if err != nil {
		return report.Authority{}, err
	}
	if err = r.writeHazardRecord(ctx, id, record); err != nil {
		return report.Authority{}, err
	}
	return r.authorityFromHazardRecord(id, record)
}

func wrapSpatialReadError(id string, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return invalidBinding("风险快照缺少空间分析", err)
	}
	return fmt.Errorf("读取风险快照 %s 空间分析: %w", id, err)
}

func (r *Resolver) readHazardRecord(ctx context.Context,
	id string,
) (hazardAuthorityRecord, bool, error) {
	var payload json.RawMessage
	found, err := r.cache.Get(ctx, hazardCachePrefix+id, &payload)
	if err != nil {
		return hazardAuthorityRecord{}, false, fmt.Errorf("%w: 读取风险权威缓存: %w", domain.ErrProviderUnavailable, err)
	}
	if !found {
		return hazardAuthorityRecord{}, false, nil
	}
	var record hazardAuthorityRecord
	if err = decodeStrict(payload, &record); err != nil {
		return hazardAuthorityRecord{}, false, unsafeStored("风险权威缓存结构损坏", err)
	}
	if err = validateHazardRecord(id, record); err != nil {
		return hazardAuthorityRecord{}, false, err
	}
	now, err := r.resolvedAt()
	if err != nil {
		return hazardAuthorityRecord{}, false, err
	}
	if !now.Before(record.ValidTo) {
		return hazardAuthorityRecord{}, false, fmt.Errorf("%w: 风险权威引用 %s 已过期", domain.ErrNotFound, id)
	}
	return record, true, nil
}

func (r *Resolver) writeHazardRecord(ctx context.Context, id string,
	record hazardAuthorityRecord,
) error {
	now, err := r.resolvedAt()
	if err != nil {
		return err
	}
	ttl := record.ValidTo.Sub(now)
	if ttl <= 0 {
		return fmt.Errorf("%w: 风险权威引用 %s 已过期", domain.ErrNotFound, id)
	}
	if ttl > maxHazardCacheTTL {
		ttl = maxHazardCacheTTL
	}
	if err = r.cache.Set(ctx, hazardCachePrefix+id, record, ttl); err != nil {
		return fmt.Errorf("%w: 缓存风险权威记录: %w", domain.ErrProviderUnavailable, err)
	}
	return nil
}

func (r *Resolver) authorityFromHazardRecord(id string,
	record hazardAuthorityRecord,
) (report.Authority, error) {
	return r.newAuthority(report.AuthorityHazardSnapshot, id, risk.RuleVersion,
		report.AuthoritySchemaHazardV1, record.Analysis)
}

func newHazardRecord(id string, read ports.HazardAuthorityRead,
	analysis spatialanalysis.Analysis,
) (hazardAuthorityRecord, error) {
	if err := validateHazardRead(id, read); err != nil {
		return hazardAuthorityRecord{}, err
	}
	if err := analysis.Validate(); err != nil {
		return hazardAuthorityRecord{}, unsafeStored("空间分析记录损坏", err)
	}
	if analysis.SnapshotID != id || !sameZoneIDs(id, read.Zones, analysis.Zones) {
		return hazardAuthorityRecord{}, invalidBinding("空间分析与风险快照绑定不一致", domain.ErrInvalidInput)
	}
	confidence, _ := confidenceLevel(read.Assessment.Confidence.Level)
	dto := report.HazardAuthorityAnalysis{
		AffectedAreaSquareMeters: analysis.Area.TotalSquareMeters,
		ConfidenceLevel:          confidence, DataStatus: string(read.Assessment.DataStatus),
		HazardType: string(read.Snapshot.HazardType), RiskLevel: string(read.Assessment.Decision.Level),
		RiskZoneCount: read.TotalZoneCount, RuleVersion: read.Assessment.RuleVersion,
		SnapshotID: read.Snapshot.ID,
	}
	record := hazardAuthorityRecord{
		Analysis: dto, AssessmentID: read.Assessment.ID, AssessmentAt: read.Assessment.EvaluatedAt,
		SpatialAnalysisID: analysis.ID, ValidTo: read.Snapshot.ValidTo,
	}
	digest, err := hazardRecordDigest(record)
	if err != nil {
		return hazardAuthorityRecord{}, err
	}
	record.Digest = digest
	return record, validateHazardRecord(id, record)
}

func validateHazardRead(id string, read ports.HazardAuthorityRead) error {
	if read.Snapshot.ID != id || !supportedHazardType(read.Snapshot.HazardType) ||
		!validSnapshotStatus(read.Snapshot.Status) ||
		read.Assessment.ID == "" || read.Assessment.SnapshotID != id ||
		read.Assessment.HazardType != read.Snapshot.HazardType {
		return invalidBinding("风险快照与持久评估身份不一致", domain.ErrInvalidInput)
	}
	if read.Assessment.RuleVersion != risk.RuleVersion || read.Assessment.Decision == nil ||
		!validRiskAssessmentStatus(read.Assessment.Status) || !validUTCTime(read.Assessment.EvaluatedAt) {
		return invalidBinding("风险规则版本、评估时间或结论不可用", domain.ErrInvalidInput)
	}
	if !validHazardWindow(read.Snapshot) {
		return invalidBinding("风险快照有效期绑定无效", domain.ErrInvalidInput)
	}
	if err := validateHazardCounts(read); err != nil {
		return err
	}
	if read.Assessment.Decision.ZoneCount != read.TotalZoneCount ||
		!validRiskLevel(read.Assessment.Decision.Level) ||
		!validHazardQuality(read.Assessment.DataStatus, read.Assessment.Confidence.Level) ||
		!validAuthorityRiskDecision(read.Zones, read.Assessment.Decision) {
		return invalidBinding("风险等级、状态或风险区数量绑定不一致", domain.ErrInvalidInput)
	}
	_, err := confidenceLevel(read.Assessment.Confidence.Level)
	return err
}

func validateHazardCounts(read ports.HazardAuthorityRead) error {
	limits := defaultHazardLimits()
	if read.TotalZoneCount < 0 || read.TotalZoneCount != len(read.Zones) ||
		read.TotalZoneCount > limits.MaxZones || read.TotalGeometryPoints < 0 ||
		read.TotalGeometryPoints > limits.MaxGeometryPoints || read.TotalGeometryBytes < 0 ||
		read.TotalGeometryBytes > limits.MaxGeometryBytes {
		return invalidBinding("风险权威读取未满足仓储前置负载上限", domain.ErrInsufficientData)
	}
	seen := make(map[string]struct{}, len(read.Zones))
	for _, zone := range read.Zones {
		if zone.ID == "" || zone.SnapshotID != read.Snapshot.ID || !validRiskLevel(zone.Level) {
			return invalidBinding("风险区摘要身份或等级无效", domain.ErrInvalidInput)
		}
		if _, exists := seen[zone.ID]; exists {
			return invalidBinding("风险区摘要包含重复标识", domain.ErrInvalidInput)
		}
		seen[zone.ID] = struct{}{}
	}
	return nil
}

func validateHazardRecord(id string, record hazardAuthorityRecord) error {
	if record.Analysis.SnapshotID != id || record.Analysis.RuleVersion != risk.RuleVersion ||
		record.AssessmentID == "" || record.SpatialAnalysisID == "" ||
		!validUTCTime(record.AssessmentAt) || !validUTCTime(record.ValidTo) ||
		!record.AssessmentAt.Before(record.ValidTo) {
		return unsafeStored("风险权威缓存绑定损坏", domain.ErrInvalidInput)
	}
	if err := validateHazardAnalysis(record.Analysis); err != nil {
		return unsafeStored("风险权威缓存枚举或数值损坏", err)
	}
	digest, err := hazardRecordDigest(record)
	if err != nil || digest != record.Digest {
		return unsafeStored("风险权威缓存摘要不一致", errors.Join(domain.ErrInvalidInput, err))
	}
	return nil
}

func validateHazardAnalysis(value report.HazardAuthorityAnalysis) error {
	if !supportedHazardType(hazard.Type(value.HazardType)) ||
		!validRiskLevel(hazard.RiskLevel(value.RiskLevel)) ||
		!validHazardQuality(risk.DataStatus(value.DataStatus), risk.ConfidenceLevel(value.ConfidenceLevel)) {
		return fmt.Errorf("%w: 风险类型、等级或状态枚举无效", domain.ErrInvalidInput)
	}
	if _, err := confidenceLevel(risk.ConfidenceLevel(value.ConfidenceLevel)); err != nil {
		return err
	}
	if math.IsNaN(value.AffectedAreaSquareMeters) || math.IsInf(value.AffectedAreaSquareMeters, 0) ||
		value.AffectedAreaSquareMeters < 0 || value.RiskZoneCount < 0 {
		return fmt.Errorf("%w: 风险权威面积或数量无效", domain.ErrInvalidInput)
	}
	return nil
}

func hazardRecordDigest(record hazardAuthorityRecord) (string, error) {
	record.Digest = ""
	payload, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("编码风险权威缓存摘要: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("缓存只能包含一个 JSON 对象")
		}
		return err
	}
	return nil
}

func defaultHazardLimits() ports.HazardAuthorityLimits {
	return ports.HazardAuthorityLimits{
		MaxZones: maxHazardAuthorityZones, MaxGeometryPoints: maxHazardGeometryPoints,
		MaxGeometryBytes: maxHazardGeometryBytes,
	}
}

func supportedHazardType(value hazard.Type) bool {
	return value == hazard.TypeLandslide || value == hazard.TypeDebrisFlow
}

func validSnapshotStatus(value hazard.SnapshotStatus) bool {
	return value == hazard.SnapshotAvailable || value == hazard.SnapshotStale
}

func validRiskAssessmentStatus(value risk.AssessmentStatus) bool {
	return value == risk.AssessmentAvailable || value == risk.AssessmentDegraded
}

func validHazardWindow(snapshot hazard.Snapshot) bool {
	return validUTCTime(snapshot.ValidTo) && validUTCTime(snapshot.Source.ValidTo) &&
		snapshot.ValidTo.Equal(snapshot.Source.ValidTo)
}

func validUTCTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func confidenceLevel(value risk.ConfidenceLevel) (string, error) {
	switch value {
	case risk.ConfidenceHigh, risk.ConfidenceMedium, risk.ConfidenceLow, risk.ConfidenceUnavailable:
		return string(value), nil
	default:
		return "", invalidBinding("风险置信度枚举无效", domain.ErrInvalidInput)
	}
}

func validRiskLevel(value hazard.RiskLevel) bool {
	switch value {
	case hazard.RiskLow, hazard.RiskModerate, hazard.RiskHigh, hazard.RiskVeryHigh:
		return true
	default:
		return false
	}
}

func validAuthorityRiskDecision(zones []ports.HazardZoneSummary, decision *risk.Decision) bool {
	if decision == nil || decision.ZoneCount != len(zones) {
		return false
	}
	level, highestIDs := hazard.RiskLow, make([]string, 0)
	for _, zone := range zones {
		rank, current := authorityRiskLevelRank(zone.Level), authorityRiskLevelRank(level)
		if zone.ID == "" || rank == 0 {
			return false
		}
		if rank > current {
			level, highestIDs = zone.Level, []string{zone.ID}
		} else if rank == current {
			highestIDs = append(highestIDs, zone.ID)
		}
	}
	basis := "highest_zone_level"
	if len(zones) == 0 {
		basis = "no_elevated_zone"
	}
	sort.Strings(highestIDs)
	return decision.Level == level && decision.Basis == basis &&
		sameOrderedAuthorityStrings(decision.HighestZoneIDs, highestIDs)
}

func authorityRiskLevelRank(value hazard.RiskLevel) int {
	switch value {
	case hazard.RiskLow:
		return 1
	case hazard.RiskModerate:
		return 2
	case hazard.RiskHigh:
		return 3
	case hazard.RiskVeryHigh:
		return 4
	default:
		return 0
	}
}

func sameOrderedAuthorityStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validHazardQuality(status risk.DataStatus, confidence risk.ConfidenceLevel) bool {
	switch status {
	case risk.DataCurrent:
		return confidence == risk.ConfidenceHigh || confidence == risk.ConfidenceMedium
	case risk.DataFallback:
		return confidence == risk.ConfidenceLow
	default:
		return false
	}
}

func sameZoneIDs(snapshotID string, riskZones []ports.HazardZoneSummary,
	spatialZones []spatialanalysis.ZoneResult,
) bool {
	if len(riskZones) != len(spatialZones) {
		return false
	}
	riskSet := make(map[string]struct{}, len(riskZones))
	for _, zone := range riskZones {
		if zone.ID == "" || zone.SnapshotID != snapshotID {
			return false
		}
		if _, exists := riskSet[zone.ID]; exists {
			return false
		}
		riskSet[zone.ID] = struct{}{}
	}
	spatialSet := make(map[string]struct{}, len(spatialZones))
	for _, zone := range spatialZones {
		if zone.ZoneID == "" {
			return false
		}
		if _, exists := spatialSet[zone.ZoneID]; exists {
			return false
		}
		spatialSet[zone.ZoneID] = struct{}{}
	}
	return sameStringSet(riskSet, spatialSet)
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			return false
		}
	}
	return true
}
