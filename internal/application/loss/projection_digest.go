package loss

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

type riskProjectionIdentity struct {
	Version                string                  `json:"version"`
	Snapshot               riskProjectionSnapshot  `json:"snapshot"`
	Analysis               riskProjectionAnalysis  `json:"analysis"`
	Zones                  []riskProjectionZone    `json:"zones"`
	Features               []riskProjectionFeature `json:"features"`
	SourceReferenceDigests []string                `json:"sourceReferenceDigests"`
}

type riskProjectionSnapshot struct {
	ID                    string                    `json:"id"`
	HazardType            string                    `json:"hazardType"`
	ModelName             string                    `json:"modelName"`
	ModelVersion          string                    `json:"modelVersion"`
	RunAt                 time.Time                 `json:"runAt"`
	ValidFrom             time.Time                 `json:"validFrom"`
	ValidTo               time.Time                 `json:"validTo"`
	RasterReferenceDigest string                    `json:"rasterReferenceDigest"`
	ProbabilitySemantics  string                    `json:"probabilitySemantics"`
	Thresholds            []riskProjectionThreshold `json:"thresholds"`
	Status                string                    `json:"status"`
	Source                riskProjectionSource      `json:"source"`
	Limitations           []string                  `json:"limitations"`
}

type riskProjectionThreshold struct {
	Level       string  `json:"level"`
	Minimum     float64 `json:"minimum"`
	Maximum     float64 `json:"maximum"`
	Description string  `json:"description"`
}

type riskProjectionSource struct {
	Provider, Dataset, DatasetVersion, SourceRevision string
	SourceURIDigest, Citation, License                string
	DataKind                                          provenance.DataKind
	ObservedAt, PublishedAt, RevisionFirstSeenAt      time.Time
	FetchedAt, ValidFrom, ValidTo                     time.Time
	SpatialResolution, TemporalResolution, CRS        string
	BBox                                              [4]float64
	SHA256, TransformVersion, ProviderRequestIDDigest string
	Model                                             string
	Stale                                             bool
	QualityFlags, Limitations                         []string
	Parts                                             []riskProjectionSourcePart
}

type riskProjectionSourcePart struct {
	ReferenceDigest, Revision, SHA256 string
	SizeBytes                         int64
	BBox                              [4]float64
}

type riskProjectionAnalysis struct {
	ID, Version, Digest, SnapshotID, Status, RegionCode string
	AdminBoundaryID, AdminBoundaryDigest                string
	AdminBoundaryReferenceDigest                        string
	TotalAreaSquareMeters                               float64
	CalculatedAt, CollectedAt, ValidFrom, ValidTo       time.Time
	InputReferenceDigests, DatasetReferenceDigests      []string
	ProjectionLimitations                               []string
}

type riskProjectionZone struct {
	ID, SnapshotID, Level string
	AreaSquareM           float64
	AreaCalculated        bool
	AdminCodes            []string
}

type riskProjectionFeature struct {
	FeatureID, Kind, Unit, Status string
	ZoneIDs                       []string
	Quantity, CoverageRatio       float64
	Provided                      bool
	InputReferenceDigests         []string
}

// BindRiskProjectionIdentity 为内容寻址投影填充版本、脱敏来源摘要和稳定标识。
func BindRiskProjectionIdentity(value *LossInputProjection) error {
	if value == nil {
		return fmt.Errorf("%w: 损失输入投影为空", domain.ErrInvalidInput)
	}
	CanonicalizeRiskProjectionTimes(value)
	limitations, err := normalizeProjectionLimitations(value.Analysis.ProjectionLimitations)
	if err != nil {
		return err
	}
	value.Analysis.ProjectionLimitations = limitations
	if value.Analysis.ProjectionCollectedAt.IsZero() ||
		!utc(value.Analysis.ProjectionCollectedAt) || !validProjectionWindow(value.Analysis) {
		return fmt.Errorf("%w: 损失输入投影或有效时间为空", domain.ErrInvalidInput)
	}
	value.Analysis.SourceReferenceDigests = RiskProjectionSourceDigests(*value)
	digest, err := RiskProjectionDigest(*value)
	if err != nil {
		return err
	}
	value.Analysis.ProjectionID = "exposure-" + digest
	value.Analysis.ProjectionVersion = lossdomain.RiskProjectionVersion
	value.Analysis.ProjectionDigest = digest
	bindRiskProjectionStats(value)
	return nil
}

func bindRiskProjectionStats(value *LossInputProjection) {
	value.Stats.ProjectionID = value.Analysis.ProjectionID
	value.Stats.ProjectionVersion = value.Analysis.ProjectionVersion
	value.Stats.ProjectionDigest = value.Analysis.ProjectionDigest
	value.Stats.ProjectionCollectedAt = value.Analysis.ProjectionCollectedAt
	value.Stats.ProjectionValidFrom = value.Analysis.ProjectionValidFrom
	value.Stats.ProjectionValidTo = value.Analysis.ProjectionValidTo
	value.Stats.ProjectionLimitationCount, value.Stats.MaxProjectionLimitationBytes,
		value.Stats.ProjectionLimitationBytes = projectionLimitationStats(value.Analysis.ProjectionLimitations)
}

// ValidateRiskProjectionIdentity 独立重算投影摘要，拒绝同标识下的内容漂移。
func ValidateRiskProjectionIdentity(value LossInputProjection) error {
	if !validProjectionLimitations(value.Analysis.ProjectionLimitations) || !riskProjectionTimesCanonical(value) {
		return fmt.Errorf("%w: 损失输入投影限制或时间精度无效", domain.ErrInvalidInput)
	}
	wantSources := RiskProjectionSourceDigests(value)
	if !sameProjectionStrings(value.Analysis.SourceReferenceDigests, wantSources) {
		return fmt.Errorf("%w: 损失输入投影脱敏来源摘要不一致", domain.ErrInvalidInput)
	}
	digest, err := RiskProjectionDigest(value)
	if err != nil {
		return err
	}
	if value.Analysis.ProjectionVersion != lossdomain.RiskProjectionVersion ||
		value.Analysis.ProjectionDigest != digest || value.Analysis.ProjectionID != "exposure-"+digest ||
		value.Analysis.ProjectionCollectedAt.IsZero() || !utc(value.Analysis.ProjectionCollectedAt) ||
		!validProjectionWindow(value.Analysis) {
		return fmt.Errorf("%w: 损失输入投影内容寻址身份不一致", domain.ErrInvalidInput)
	}
	return nil
}

func validProjectionWindow(value LossSpatialProjection) bool {
	return !value.ProjectionValidFrom.IsZero() && !value.ProjectionValidTo.IsZero() &&
		utc(value.ProjectionValidFrom) && utc(value.ProjectionValidTo) &&
		value.ProjectionValidTo.After(value.ProjectionValidFrom) &&
		!value.ProjectionCollectedAt.Before(value.ProjectionValidFrom) &&
		value.ProjectionCollectedAt.Before(value.ProjectionValidTo)
}

// RiskProjectionDigest 只序列化脱敏来源摘要，不把原始来源地址写入摘要载荷。
func RiskProjectionDigest(value LossInputProjection) (string, error) {
	if !validProjectionLimitations(value.Analysis.ProjectionLimitations) || !riskProjectionTimesCanonical(value) {
		return "", fmt.Errorf("%w: 损失输入投影限制或时间精度无效", domain.ErrInvalidInput)
	}
	payload, err := json.Marshal(newRiskProjectionIdentity(value))
	if err != nil {
		return "", fmt.Errorf("序列化损失输入投影摘要: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// RiskProjectionSourceDigests 返回投影引用的排序去重 SHA-256，不暴露原始地址。
func RiskProjectionSourceDigests(value LossInputProjection) []string {
	references := []string{value.Snapshot.RasterReference, value.Snapshot.Source.SourceURI,
		value.Analysis.AdminBoundaryReference}
	for _, part := range value.Snapshot.Source.SourceParts {
		references = append(references, part.Reference)
	}
	references = append(references, value.Analysis.InputReferences...)
	references = append(references, value.Analysis.DatasetReferences...)
	for _, feature := range value.Analysis.Features {
		references = append(references, feature.InputReferences...)
	}
	return hashedProjectionReferences(references)
}

func newRiskProjectionIdentity(value LossInputProjection) riskProjectionIdentity {
	return riskProjectionIdentity{Version: lossdomain.RiskProjectionVersion,
		Snapshot: riskProjectionSnapshotValue(value), Analysis: riskProjectionAnalysisValue(value.Analysis),
		Zones: riskProjectionZones(value.Zones), Features: riskProjectionFeatures(value.Analysis.Features),
		SourceReferenceDigests: RiskProjectionSourceDigests(value)}
}

func riskProjectionSnapshotValue(value LossInputProjection) riskProjectionSnapshot {
	snapshot := value.Snapshot
	return riskProjectionSnapshot{ID: snapshot.ID, HazardType: string(snapshot.HazardType),
		ModelName: snapshot.ModelName, ModelVersion: snapshot.ModelVersion, RunAt: snapshot.RunAt,
		ValidFrom: snapshot.ValidFrom, ValidTo: snapshot.ValidTo,
		RasterReferenceDigest: hashProjectionReference(snapshot.RasterReference),
		ProbabilitySemantics:  snapshot.ProbabilitySemantics, Thresholds: riskProjectionThresholds(value),
		Status: string(snapshot.Status), Source: riskProjectionSourceValue(snapshot.Source),
		Limitations: sortedProjectionStrings(snapshot.Limitations)}
}

func riskProjectionThresholds(value LossInputProjection) []riskProjectionThreshold {
	result := make([]riskProjectionThreshold, len(value.Snapshot.Thresholds))
	for index, threshold := range value.Snapshot.Thresholds {
		result[index] = riskProjectionThreshold{Level: string(threshold.Level), Minimum: threshold.Minimum,
			Maximum: threshold.Maximum, Description: threshold.Description}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Level < result[right].Level })
	return result
}

func riskProjectionSourceValue(value provenance.Provenance) riskProjectionSource {
	return riskProjectionSource{Provider: value.Provider, Dataset: value.Dataset,
		DatasetVersion: value.DatasetVersion, SourceRevision: value.SourceRevision,
		SourceURIDigest: hashProjectionReference(value.SourceURI), Citation: value.Citation, License: value.License,
		DataKind: value.DataKind, ObservedAt: value.ObservedAt, PublishedAt: value.PublishedAt,
		RevisionFirstSeenAt: value.RevisionFirstSeenAt, FetchedAt: value.FetchedAt,
		ValidFrom: value.ValidFrom, ValidTo: value.ValidTo, SpatialResolution: value.SpatialResolution,
		TemporalResolution: value.TemporalResolution, CRS: value.CRS, BBox: value.BBox,
		SHA256: value.SHA256, TransformVersion: value.TransformVersion,
		ProviderRequestIDDigest: hashOptionalProjectionReference(value.ProviderRequestID), Model: value.Model,
		Stale: value.Stale, QualityFlags: sortedProjectionStrings(value.QualityFlags),
		Limitations: sortedProjectionStrings(value.Limitations), Parts: riskProjectionSourceParts(value.SourceParts)}
}

func riskProjectionSourceParts(values []provenance.SourcePart) []riskProjectionSourcePart {
	result := make([]riskProjectionSourcePart, len(values))
	for index, part := range values {
		result[index] = riskProjectionSourcePart{ReferenceDigest: hashProjectionReference(part.Reference),
			Revision: part.Revision, SHA256: part.SHA256, SizeBytes: part.SizeBytes, BBox: part.BBox}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ReferenceDigest < result[right].ReferenceDigest
	})
	return result
}

func riskProjectionAnalysisValue(value LossSpatialProjection) riskProjectionAnalysis {
	return riskProjectionAnalysis{ID: value.ID, Version: value.Version, Digest: value.Digest,
		SnapshotID: value.SnapshotID, Status: string(value.Status), RegionCode: value.RegionCode,
		AdminBoundaryID: value.AdminBoundaryID, AdminBoundaryDigest: value.AdminBoundaryDigest,
		AdminBoundaryReferenceDigest: hashProjectionReference(value.AdminBoundaryReference),
		TotalAreaSquareMeters:        value.TotalAreaSquareMeters, CalculatedAt: value.CalculatedAt,
		CollectedAt: value.ProjectionCollectedAt,
		ValidFrom:   value.ProjectionValidFrom, ValidTo: value.ProjectionValidTo,
		InputReferenceDigests:   hashedProjectionReferences(value.InputReferences),
		DatasetReferenceDigests: hashedProjectionReferences(value.DatasetReferences),
		ProjectionLimitations:   sortedProjectionStrings(value.ProjectionLimitations)}
}

func riskProjectionZones(values []LossRiskZone) []riskProjectionZone {
	result := make([]riskProjectionZone, len(values))
	for index, value := range values {
		result[index] = riskProjectionZone{ID: value.ID, SnapshotID: value.SnapshotID,
			Level: string(value.Level), AreaSquareM: value.AreaSquareM, AreaCalculated: value.AreaCalculated,
			AdminCodes: sortedProjectionStrings(value.AdminCodes)}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func riskProjectionFeatures(values []LossExposureFeature) []riskProjectionFeature {
	result := make([]riskProjectionFeature, len(values))
	for index, value := range values {
		result[index] = riskProjectionFeature{FeatureID: value.FeatureID, Kind: string(value.Kind),
			ZoneIDs: sortedProjectionStrings(value.ZoneIDs), Quantity: value.Quantity, Unit: value.Unit,
			CoverageRatio: value.CoverageRatio, Status: string(value.Status), Provided: value.Provided,
			InputReferenceDigests: hashedProjectionReferences(value.InputReferences)}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].FeatureID < result[right].Kind+"\x00"+result[right].FeatureID
	})
	return result
}

func hashedProjectionReferences(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[hashProjectionReference(value)] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedProjectionStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func normalizeProjectionLimitations(values []string) ([]string, error) {
	if len(values) > maxLossProjectionLimitations {
		return nil, fmt.Errorf("%w: 空间投影限制数量超限", domain.ErrInvalidInput)
	}
	seen, total := make(map[string]struct{}, len(values)), 0
	for _, value := range values {
		if value == "" || len(value) > maxLossProjectionLimitationBytes ||
			len(value)+total > maxLossProjectionLimitationTotalBytes || unsafeText(value) {
			return nil, fmt.Errorf("%w: 空间投影限制文本超限或无效", domain.ErrInvalidInput)
		}
		if value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%w: 空间投影限制文本未规范化", domain.ErrInvalidInput)
		}
		total += len(value)
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validProjectionLimitations(values []string) bool {
	if values == nil {
		return false
	}
	normalized, err := normalizeProjectionLimitations(values)
	return err == nil && sameProjectionStrings(values, normalized)
}

// CanonicalizeRiskProjectionTimes 统一投影摘要涉及的时间为 PostgreSQL 可往返的 UTC 微秒精度。
func CanonicalizeRiskProjectionTimes(value *LossInputProjection) {
	if value == nil {
		return
	}
	value.Snapshot.RunAt = canonicalProjectionTime(value.Snapshot.RunAt)
	value.Snapshot.ValidFrom = canonicalProjectionTime(value.Snapshot.ValidFrom)
	value.Snapshot.ValidTo = canonicalProjectionTime(value.Snapshot.ValidTo)
	canonicalizeProjectionProvenance(&value.Snapshot.Source)
	value.Analysis.CalculatedAt = canonicalProjectionTime(value.Analysis.CalculatedAt)
	value.Analysis.ProjectionCollectedAt = canonicalProjectionTime(value.Analysis.ProjectionCollectedAt)
	value.Analysis.ProjectionValidFrom = canonicalProjectionTime(value.Analysis.ProjectionValidFrom)
	value.Analysis.ProjectionValidTo = canonicalProjectionTime(value.Analysis.ProjectionValidTo)
}

func canonicalizeProjectionProvenance(value *provenance.Provenance) {
	value.ObservedAt = canonicalProjectionTime(value.ObservedAt)
	value.PublishedAt = canonicalProjectionTime(value.PublishedAt)
	value.RevisionFirstSeenAt = canonicalProjectionTime(value.RevisionFirstSeenAt)
	value.FetchedAt = canonicalProjectionTime(value.FetchedAt)
	value.ValidFrom = canonicalProjectionTime(value.ValidFrom)
	value.ValidTo = canonicalProjectionTime(value.ValidTo)
}

func canonicalProjectionTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func riskProjectionTimesCanonical(value LossInputProjection) bool {
	times := []time.Time{value.Snapshot.RunAt, value.Snapshot.ValidFrom, value.Snapshot.ValidTo,
		value.Snapshot.Source.ObservedAt, value.Snapshot.Source.PublishedAt,
		value.Snapshot.Source.RevisionFirstSeenAt, value.Snapshot.Source.FetchedAt,
		value.Snapshot.Source.ValidFrom, value.Snapshot.Source.ValidTo, value.Analysis.CalculatedAt,
		value.Analysis.ProjectionCollectedAt, value.Analysis.ProjectionValidFrom,
		value.Analysis.ProjectionValidTo}
	for _, value := range times {
		if !value.IsZero() && (!utc(value) || value.Nanosecond()%1_000 != 0) {
			return false
		}
	}
	return true
}

func hashOptionalProjectionReference(value string) string {
	if value == "" {
		return ""
	}
	return hashProjectionReference(value)
}

func hashProjectionReference(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func sameProjectionStrings(left, right []string) bool {
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
