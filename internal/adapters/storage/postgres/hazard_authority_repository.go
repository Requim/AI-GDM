package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	fallbackQualityFlag                = "fallback_last_success"
	fallbackBoundaryQualityFlag        = "fallback_boundary_identity_unverified"
	fallbackSourceLimitation           = "实时采集失败，使用最后成功 LHASA 分析"
	fallbackSnapshotLimitation         = "本次刷新使用未超过时效的最后成功结果"
	fallbackBoundarySnapshotLimitation = "本次未能刷新行政边界身份，沿用最后成功快照的已绑定边界"
)

type fallbackOverlayVariant uint8

const (
	fallbackOverlayNone fallbackOverlayVariant = iota
	fallbackOverlayStandard
	fallbackOverlayBoundary
	fallbackOverlayInvalid
)

var _ ports.HazardAuthorityReader = (*HazardRepository)(nil)
var _ ports.RiskAuthorityReuser = (*HazardRepository)(nil)

// ReuseRiskAuthority 在重算空间分析前核对完整快照、风险区和已固化权威。
func (r *HazardRepository) ReuseRiskAuthority(ctx context.Context, snapshot hazard.Snapshot,
	zones []hazard.RiskZone,
) (bool, error) {
	if r == nil || r.pool == nil {
		return false, fmt.Errorf("%w: 风险权威数据库连接为空", domain.ErrInvalidInput)
	}
	if err := validateCompleteAnalysis(snapshot, zones); err != nil {
		return false, err
	}
	snapshot = normalizeSnapshotForStorage(snapshot)
	tx, err := r.pool.BeginTx(ctx, completeRiskReadOptions)
	if err != nil {
		return false, fmt.Errorf("开始复用风险权威事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	reusable, err := reuseStoredRiskAuthority(ctx, tx, snapshot, zones)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("提交复用风险权威事务: %w", err)
	}
	return reusable, nil
}

func reuseStoredRiskAuthority(ctx context.Context, tx pgx.Tx, snapshot hazard.Snapshot,
	zones []hazard.RiskZone,
) (bool, error) {
	stored, found, err := loadStoredRiskAuthority(ctx, tx, snapshot.ID)
	if err != nil || !found {
		return false, err
	}
	if err = sameStoredSnapshot(stored, snapshot); err != nil {
		return false, err
	}
	storedZones, err := zonesBySnapshot(ctx, tx, snapshot.ID)
	if err != nil {
		return false, err
	}
	equal, err := sameCanonicalZoneInputs(storedZones, zones)
	if err != nil {
		return false, fmt.Errorf("规范化复用风险区: %w", err)
	}
	if !equal {
		return false, fmt.Errorf("%w: 同一快照标识的风险区完整内容发生变化", domain.ErrInvalidInput)
	}
	if err = validateReusableSpatialAuthority(ctx, tx, snapshot.ID, stored, storedZones); err != nil {
		return false, err
	}
	return true, nil
}

func sameStoredSnapshot(stored storedRiskAuthority, snapshot hazard.Snapshot) error {
	storedJSON, storedDigest, _, _, err := canonicalStoredAuthority(stored)
	if err != nil {
		return fmt.Errorf("%w: 已固化风险权威损坏: %w", domain.ErrInsufficientData, err)
	}
	incomingJSON, incomingDigest, err := payloadDigest(snapshot)
	if err != nil {
		return fmt.Errorf("编码待复用风险快照: %w", err)
	}
	if storedDigest == incomingDigest && bytes.Equal(storedJSON, incomingJSON) {
		return nil
	}
	var storedSnapshot hazard.Snapshot
	if err = json.Unmarshal(storedJSON, &storedSnapshot); err != nil {
		return fmt.Errorf("%w: 已固化风险快照无法解码: %w", domain.ErrInsufficientData, err)
	}
	if validFallbackOverlay(storedSnapshot, snapshot) {
		return nil
	}
	return fmt.Errorf("%w: 同一快照标识的完整内容发生变化", domain.ErrInvalidInput)
}

func validFallbackOverlay(stored, incoming hazard.Snapshot) bool {
	boundaryFlag, validFlags := fallbackListVariant(stored.Source.QualityFlags,
		incoming.Source.QualityFlags, fallbackQualityFlag, fallbackBoundaryQualityFlag)
	boundaryLimitation, validLimitations := fallbackListVariant(stored.Limitations,
		incoming.Limitations, fallbackSnapshotLimitation, fallbackBoundarySnapshotLimitation)
	if incoming.Status != hazard.SnapshotStale || !incoming.Source.Stale ||
		!validFlags || !validLimitations || boundaryFlag != boundaryLimitation ||
		!fallbackList(stored.Source.Limitations, incoming.Source.Limitations, fallbackSourceLimitation) {
		return false
	}
	candidate := incoming
	candidate.Status, candidate.Source.Stale = stored.Status, stored.Source.Stale
	candidate.Source.QualityFlags = append([]string(nil), stored.Source.QualityFlags...)
	candidate.Source.Limitations = append([]string(nil), stored.Source.Limitations...)
	candidate.Limitations = append([]string(nil), stored.Limitations...)
	left, leftDigest, err := payloadDigest(stored)
	if err != nil {
		return false
	}
	right, rightDigest, err := payloadDigest(candidate)
	return err == nil && leftDigest == rightDigest && bytes.Equal(left, right)
}

func fallbackListVariant(stored, incoming []string, required, optional string) (bool, bool) {
	storedBase, storedVariant := splitFallbackSuffix(stored, required, optional)
	incomingBase, incomingVariant := splitFallbackSuffix(incoming, required, optional)
	if storedVariant == fallbackOverlayInvalid || incomingVariant == fallbackOverlayInvalid ||
		incomingVariant == fallbackOverlayNone || storedVariant > incomingVariant ||
		!sameOrderedStrings(storedBase, incomingBase) {
		return false, false
	}
	return incomingVariant == fallbackOverlayBoundary, true
}

func splitFallbackSuffix(values []string, required, optional string) ([]string, fallbackOverlayVariant) {
	base, variant := values, fallbackOverlayNone
	switch {
	case len(values) >= 2 && values[len(values)-2] == required && values[len(values)-1] == optional:
		base, variant = values[:len(values)-2], fallbackOverlayBoundary
	case len(values) >= 1 && values[len(values)-1] == required:
		base, variant = values[:len(values)-1], fallbackOverlayStandard
	}
	if containsOrderedString(base, required) || containsOrderedString(base, optional) {
		return nil, fallbackOverlayInvalid
	}
	return base, variant
}

func fallbackList(stored, incoming []string, marker string) bool {
	if sameOrderedStrings(stored, incoming) {
		return containsOrderedString(incoming, marker)
	}
	if containsOrderedString(stored, marker) {
		return false
	}
	if len(incoming) != len(stored)+1 || incoming[len(incoming)-1] != marker {
		return false
	}
	return sameOrderedStrings(stored, incoming[:len(stored)])
}

func containsOrderedString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validateReusableSpatialAuthority(ctx context.Context, queryer rowQueryer, snapshotID string,
	stored storedRiskAuthority, zones []hazard.RiskZone,
) error {
	var assessment risk.Assessment
	if err := json.Unmarshal(stored.assessmentJSON, &assessment); err != nil {
		return fmt.Errorf("%w: 已固化风险评估无法解码: %w", domain.ErrInsufficientData, err)
	}
	if assessment.Decision == nil || assessment.Decision.ZoneCount != len(zones) {
		return fmt.Errorf("%w: 已固化风险评估与风险区数量不一致", domain.ErrInsufficientData)
	}
	summaries := make([]ports.HazardZoneSummary, len(zones))
	for _, zone := range zones {
		if !zone.AreaCalculated {
			return fmt.Errorf("%w: 已固化风险区缺少空间面积", domain.ErrInsufficientData)
		}
	}
	for index, zone := range zones {
		summaries[index] = ports.HazardZoneSummary{ID: zone.ID, SnapshotID: zone.SnapshotID, Level: zone.Level}
	}
	if err := validateStoredRiskDecision(summaries, assessment.Decision); err != nil {
		return fmt.Errorf("%w: 已固化风险结论损坏: %w", domain.ErrInsufficientData, err)
	}
	var complete bool
	if err := queryer.QueryRow(ctx, selectReusableSpatialAuthoritySQL,
		snapshotID, len(zones)).Scan(&complete); err != nil {
		return fmt.Errorf("核对可复用空间分析: %w", err)
	}
	if !complete {
		return fmt.Errorf("%w: 快照缺少完整可复用空间分析", domain.ErrInsufficientData)
	}
	return nil
}

// ReadAuthority 在单个只读可重复读事务中返回无几何的权威风险投影。
func (r *HazardRepository) ReadAuthority(ctx context.Context, snapshotID string,
	limits ports.HazardAuthorityLimits,
) (ports.HazardAuthorityRead, error) {
	if err := validateAuthorityReadInput(r, snapshotID, limits); err != nil {
		return ports.HazardAuthorityRead{}, err
	}
	tx, err := r.pool.BeginTx(ctx, completeRiskReadOptions)
	if err != nil {
		return ports.HazardAuthorityRead{}, fmt.Errorf("开始读取风险权威事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := readHazardAuthority(ctx, tx, snapshotID, limits)
	if err != nil {
		return ports.HazardAuthorityRead{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.HazardAuthorityRead{}, fmt.Errorf("提交风险权威读取事务: %w", err)
	}
	return result, nil
}

func readHazardAuthority(ctx context.Context, tx pgx.Tx, snapshotID string,
	limits ports.HazardAuthorityLimits,
) (ports.HazardAuthorityRead, error) {
	snapshot, err := scanSnapshot(tx.QueryRow(ctx, selectSnapshotSQL+riskDetailWhere, snapshotID))
	if err != nil {
		return ports.HazardAuthorityRead{}, fmt.Errorf("读取风险权威快照: %w", err)
	}
	counts, err := readAuthorityCounts(ctx, tx, snapshotID, limits)
	if err != nil {
		return ports.HazardAuthorityRead{}, err
	}
	storedSnapshot, assessment, err := readStoredRiskAuthority(ctx, tx, snapshotID, snapshot)
	if err != nil {
		return ports.HazardAuthorityRead{}, err
	}
	zones, err := readAuthorityZoneSummaries(ctx, tx, snapshotID, limits.MaxZones)
	if err != nil {
		return ports.HazardAuthorityRead{}, err
	}
	if len(zones) != counts.zones {
		return ports.HazardAuthorityRead{}, fmt.Errorf("%w: 风险权威区计数与摘要不一致", domain.ErrInsufficientData)
	}
	if err = validateStoredRiskDecision(zones, assessment.Decision); err != nil {
		return ports.HazardAuthorityRead{}, fmt.Errorf(
			"%w: 风险权威结论与风险区不一致: %w", domain.ErrInsufficientData, err)
	}
	return ports.HazardAuthorityRead{
		Snapshot: storedSnapshot, Assessment: assessment, Zones: zones,
		TotalZoneCount: counts.zones, TotalGeometryPoints: counts.points,
		TotalGeometryBytes: counts.bytes,
	}, nil
}

type authorityCounts struct {
	zones  int
	points int
	bytes  int
}

func readAuthorityCounts(ctx context.Context, queryer riskMapQueryer, snapshotID string,
	limits ports.HazardAuthorityLimits,
) (authorityCounts, error) {
	var zones, points, geometryBytes int64
	err := queryer.QueryRow(ctx, selectAuthorityCountsSQL, snapshotID).
		Scan(&zones, &points, &geometryBytes)
	if err != nil {
		return authorityCounts{}, fmt.Errorf("统计风险权威负载: %w", err)
	}
	if zones < 0 || points < 0 || geometryBytes < 0 || zones > int64(limits.MaxZones) ||
		points > int64(limits.MaxGeometryPoints) || geometryBytes > int64(limits.MaxGeometryBytes) {
		return authorityCounts{}, fmt.Errorf("%w: 风险权威负载超过前置上限", domain.ErrInsufficientData)
	}
	return authorityCounts{zones: int(zones), points: int(points), bytes: int(geometryBytes)}, nil
}

func readStoredRiskAuthority(ctx context.Context, rowSource rowQueryer, snapshotID string,
	liveSnapshot hazard.Snapshot,
) (hazard.Snapshot, risk.Assessment, error) {
	stored, found, err := loadStoredRiskAuthority(ctx, rowSource, snapshotID)
	if err != nil {
		return hazard.Snapshot{}, risk.Assessment{}, err
	}
	if !found {
		return hazard.Snapshot{}, risk.Assessment{}, fmt.Errorf(
			"%w: 风险快照缺少持久评估", domain.ErrInsufficientData)
	}
	snapshotJSON, _, assessmentJSON, _, err := canonicalStoredAuthority(stored)
	if err != nil {
		return hazard.Snapshot{}, risk.Assessment{}, fmt.Errorf(
			"%w: 持久风险权威内容损坏: %w", domain.ErrInsufficientData, err)
	}
	liveJSON, liveDigest, err := payloadDigest(liveSnapshot)
	if err != nil || liveDigest != stored.snapshotDigest || !bytes.Equal(liveJSON, snapshotJSON) {
		return hazard.Snapshot{}, risk.Assessment{}, fmt.Errorf(
			"%w: 当前快照与持久权威快照不一致", domain.ErrInsufficientData)
	}
	var storedSnapshot hazard.Snapshot
	var assessment risk.Assessment
	if err = json.Unmarshal(snapshotJSON, &storedSnapshot); err != nil {
		return hazard.Snapshot{}, risk.Assessment{}, err
	}
	if err = json.Unmarshal(assessmentJSON, &assessment); err != nil {
		return hazard.Snapshot{}, risk.Assessment{}, err
	}
	return storedSnapshot, assessment, nil
}

func loadStoredRiskAuthority(ctx context.Context, rowSource rowQueryer,
	snapshotID string,
) (storedRiskAuthority, bool, error) {
	var stored storedRiskAuthority
	err := rowSource.QueryRow(ctx, selectRiskAssessmentSQL, snapshotID).
		Scan(&stored.snapshotJSON, &stored.snapshotDigest,
			&stored.assessmentJSON, &stored.assessmentDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedRiskAuthority{}, false, nil
	}
	if err != nil {
		return storedRiskAuthority{}, false, fmt.Errorf("读取持久风险评估: %w", err)
	}
	return stored, true, nil
}

func sameCanonicalZoneInputs(left, right []hazard.RiskZone) (bool, error) {
	leftJSON, err := canonicalZoneInputSet(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := canonicalZoneInputSet(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func validateStoredRiskDecision(zones []ports.HazardZoneSummary, decision *risk.Decision) error {
	if decision == nil || decision.ZoneCount != len(zones) {
		return fmt.Errorf("%w: 风险结论数量无效", domain.ErrInvalidInput)
	}
	level, highestIDs := hazard.RiskLow, make([]string, 0)
	for _, zone := range zones {
		rank := storedRiskLevelRank(zone.Level)
		if zone.ID == "" || rank == 0 {
			return fmt.Errorf("%w: 风险区摘要无效", domain.ErrInvalidInput)
		}
		current := storedRiskLevelRank(level)
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
	if decision.Level != level || decision.Basis != basis || !sameOrderedStrings(decision.HighestZoneIDs, highestIDs) {
		return fmt.Errorf("%w: 风险结论最高等级或依据不一致", domain.ErrInvalidInput)
	}
	return nil
}

func storedRiskLevelRank(value hazard.RiskLevel) int {
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

func sameOrderedStrings(left, right []string) bool {
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

func canonicalZoneInputSet(values []hazard.RiskZone) ([]byte, error) {
	copyValues := append([]hazard.RiskZone(nil), values...)
	sort.Slice(copyValues, func(left, right int) bool { return copyValues[left].ID < copyValues[right].ID })
	for index := range copyValues {
		copyValues[index].AreaSquareM = 0
		copyValues[index].AreaCalculated = false
		coordinates, err := canonicalJSON(copyValues[index].Geometry.Coordinates)
		if err != nil {
			return nil, fmt.Errorf("规范化风险区 %s 几何: %w", copyValues[index].ID, err)
		}
		copyValues[index].Geometry.Coordinates = coordinates
	}
	return json.Marshal(copyValues)
}

func canonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(decoded)
	return json.RawMessage(payload), err
}

func readAuthorityZoneSummaries(ctx context.Context, queryer sqlQueryer, snapshotID string,
	maxZones int,
) ([]ports.HazardZoneSummary, error) {
	rows, err := queryer.Query(ctx, selectAuthorityZonesSQL, snapshotID, maxZones)
	if err != nil {
		return nil, fmt.Errorf("读取风险权威区摘要: %w", err)
	}
	defer rows.Close()
	values := make([]ports.HazardZoneSummary, 0)
	for rows.Next() {
		var value ports.HazardZoneSummary
		if err = rows.Scan(&value.ID, &value.SnapshotID, &value.Level); err != nil {
			return nil, fmt.Errorf("扫描风险权威区摘要: %w", err)
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历风险权威区摘要: %w", err)
	}
	return values, nil
}

func validateAuthorityReadInput(repository *HazardRepository, snapshotID string,
	limits ports.HazardAuthorityLimits,
) error {
	if repository == nil || repository.pool == nil {
		return fmt.Errorf("%w: 风险权威数据库连接为空", domain.ErrInvalidInput)
	}
	if err := validateRiskSnapshotID(snapshotID); err != nil {
		return err
	}
	if limits.MaxZones <= 0 || limits.MaxGeometryPoints <= 0 || limits.MaxGeometryBytes <= 0 {
		return fmt.Errorf("%w: 风险权威读取上限无效", domain.ErrInvalidInput)
	}
	return nil
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const selectAuthorityCountsSQL = `SELECT COUNT(*),
    COALESCE(SUM(ST_NPoints(geometry)),0),
    COALESCE(SUM(OCTET_LENGTH(ST_AsEWKB(geometry))),0)
    FROM risk_zones WHERE snapshot_id=$1`

const selectAuthorityZonesSQL = `SELECT id,snapshot_id,risk_level
    FROM risk_zones WHERE snapshot_id=$1 ORDER BY id LIMIT $2`

const selectReusableSpatialAuthoritySQL = `SELECT EXISTS(
    SELECT 1 FROM (
        SELECT id,zone_count FROM spatial_analyses WHERE snapshot_id=$1
        ORDER BY calculated_at DESC,id DESC LIMIT 1
    ) AS analysis
    WHERE analysis.zone_count=$2
        AND (SELECT COUNT(*) FROM spatial_zone_results AS result
            WHERE result.analysis_id=analysis.id)=$2
        AND NOT EXISTS (
            SELECT 1 FROM risk_zones AS zone
            LEFT JOIN spatial_zone_results AS result
                ON result.analysis_id=analysis.id
                AND result.snapshot_id=zone.snapshot_id AND result.zone_id=zone.id
            WHERE zone.snapshot_id=$1 AND (
                NOT zone.area_calculated OR result.zone_id IS NULL
                OR zone.area_square_meters IS DISTINCT FROM result.area_square_meters
            )
        )
)`
