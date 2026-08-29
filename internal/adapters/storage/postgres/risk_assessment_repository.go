package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/ports"
)

var _ ports.RiskAssessmentWriter = (*HazardRepository)(nil)

// SaveRiskAssessment 固化完整快照和确定性评估；相同内容重复写入是幂等操作。
func (r *HazardRepository) SaveRiskAssessment(ctx context.Context, snapshot hazard.Snapshot,
	assessment risk.Assessment,
) error {
	if err := validateRiskAssessmentWrite(r, snapshot, assessment); err != nil {
		return err
	}
	snapshot = normalizeSnapshotForStorage(snapshot)
	if err := validateRiskAssessmentBinding(snapshot, assessment); err != nil {
		return err
	}
	snapshotJSON, snapshotDigest, err := payloadDigest(snapshot)
	if err != nil {
		return fmt.Errorf("编码风险快照 %s: %w", snapshot.ID, err)
	}
	assessmentJSON, assessmentDigest, err := payloadDigest(assessment)
	if err != nil {
		return fmt.Errorf("编码风险评估 %s: %w", assessment.ID, err)
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始固化风险评估事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = verifyLiveRiskSnapshot(ctx, tx, snapshot, snapshotJSON,
		snapshotDigest, assessment.Decision); err != nil {
		return err
	}
	stored, err := writeRiskAssessment(ctx, tx, snapshot, assessment,
		snapshotJSON, snapshotDigest, assessmentJSON, assessmentDigest)
	if err != nil {
		return err
	}
	if err = verifyStoredRiskAuthority(stored, snapshotJSON, snapshotDigest,
		assessmentJSON, assessmentDigest); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交固化风险评估事务: %w", err)
	}
	return nil
}

type storedRiskAuthority struct {
	snapshotJSON     []byte
	snapshotDigest   string
	assessmentJSON   []byte
	assessmentDigest string
}

func writeRiskAssessment(ctx context.Context, queryer rowQueryer,
	snapshot hazard.Snapshot, assessment risk.Assessment,
	snapshotJSON []byte, snapshotDigest string, assessmentJSON []byte, assessmentDigest string,
) (storedRiskAuthority, error) {
	var stored storedRiskAuthority
	err := queryer.QueryRow(ctx, insertRiskAssessmentSQL,
		snapshot.ID, assessment.ID, snapshot.HazardType, assessment.RuleVersion,
		assessment.Status, assessment.DataStatus, assessment.EvaluatedAt,
		snapshotJSON, snapshotDigest, assessmentJSON, assessmentDigest,
	).Scan(&stored.snapshotJSON, &stored.snapshotDigest,
		&stored.assessmentJSON, &stored.assessmentDigest)
	if errors.Is(err, pgx.ErrNoRows) || riskAssessmentUniqueViolation(err) {
		return storedRiskAuthority{}, fmt.Errorf(
			"%w: 快照 %s 已绑定其他风险评估", domain.ErrInvalidInput, snapshot.ID)
	}
	if err != nil {
		return storedRiskAuthority{}, fmt.Errorf("保存风险评估 %s: %w", assessment.ID, err)
	}
	return stored, nil
}

func verifyLiveRiskSnapshot(ctx context.Context, queryer riskMapQueryer, snapshot hazard.Snapshot,
	expectedJSON []byte, expectedDigest string, decision *risk.Decision,
) error {
	live, err := scanSnapshot(queryer.QueryRow(ctx,
		selectSnapshotSQL+riskAssessmentSnapshotWhere, snapshot.ID))
	if err != nil {
		return fmt.Errorf("读取待固化风险快照 %s: %w", snapshot.ID, err)
	}
	liveJSON, liveDigest, err := payloadDigest(live)
	if err != nil {
		return fmt.Errorf("编码待固化风险快照 %s: %w", snapshot.ID, err)
	}
	if liveDigest != expectedDigest || !bytes.Equal(liveJSON, expectedJSON) {
		return fmt.Errorf("%w: 待固化风险快照与数据库完整内容不一致", domain.ErrInvalidInput)
	}
	var zoneCount int
	if err = queryer.QueryRow(ctx, countZonesSQL, snapshot.ID).Scan(&zoneCount); err != nil {
		return fmt.Errorf("核对待固化风险区数量: %w", err)
	}
	if decision == nil || zoneCount != decision.ZoneCount {
		return fmt.Errorf("%w: 待固化评估风险区数量与数据库不一致", domain.ErrInvalidInput)
	}
	zones, err := readAuthorityZoneSummaries(ctx, queryer, snapshot.ID, zoneCount)
	if err != nil {
		return err
	}
	return validateStoredRiskDecision(zones, decision)
}

func validateRiskAssessmentWrite(repository *HazardRepository, snapshot hazard.Snapshot,
	assessment risk.Assessment,
) error {
	if repository == nil || repository.pool == nil {
		return fmt.Errorf("%w: 风险评估数据库连接为空", domain.ErrInvalidInput)
	}
	if err := validateCompleteAnalysis(snapshot, nil); err != nil {
		return fmt.Errorf("校验风险评估快照: %w", err)
	}
	if err := validateRiskAssessmentBinding(snapshot, assessment); err != nil {
		return err
	}
	return nil
}

func validateRiskAssessmentBinding(snapshot hazard.Snapshot, assessment risk.Assessment) error {
	if strings.TrimSpace(assessment.ID) == "" || assessment.SnapshotID != snapshot.ID ||
		assessment.HazardType != snapshot.HazardType || assessment.RuleVersion != risk.RuleVersion ||
		assessment.Decision == nil || assessment.Decision.ZoneCount < 0 {
		return fmt.Errorf("%w: 风险评估与快照身份或结论不一致", domain.ErrInvalidInput)
	}
	if assessment.EvaluatedAt.IsZero() || !isUTCTimestamp(assessment.EvaluatedAt) ||
		assessment.EvaluatedAt.Before(snapshot.ValidFrom) || assessment.EvaluatedAt.After(snapshot.ValidTo) {
		return fmt.Errorf("%w: 风险评估时间不在快照 UTC 有效期内", domain.ErrInvalidInput)
	}
	if !snapshot.ValidTo.Equal(snapshot.Source.ValidTo) || !validStoredRiskQuality(assessment) ||
		!validStoredRiskLevel(assessment.Decision.Level) {
		return fmt.Errorf("%w: 风险评估状态、等级或来源有效期无效", domain.ErrInvalidInput)
	}
	return nil
}

func validStoredRiskQuality(value risk.Assessment) bool {
	switch value.Status {
	case risk.AssessmentAvailable:
		return value.DataStatus == risk.DataCurrent &&
			(value.Confidence.Level == risk.ConfidenceHigh || value.Confidence.Level == risk.ConfidenceMedium)
	case risk.AssessmentDegraded:
		return value.DataStatus == risk.DataFallback && value.Confidence.Level == risk.ConfidenceLow
	default:
		return false
	}
}

func validStoredRiskLevel(value hazard.RiskLevel) bool {
	switch value {
	case hazard.RiskLow, hazard.RiskModerate, hazard.RiskHigh, hazard.RiskVeryHigh:
		return true
	default:
		return false
	}
}

func verifyStoredRiskAuthority(stored storedRiskAuthority, expectedSnapshot []byte,
	expectedSnapshotDigest string, expectedAssessment []byte, expectedAssessmentDigest string,
) error {
	if stored.snapshotDigest != expectedSnapshotDigest || stored.assessmentDigest != expectedAssessmentDigest {
		return fmt.Errorf("%w: 已保存风险权威摘要与输入不一致", domain.ErrInvalidInput)
	}
	snapshotJSON, snapshotDigest, assessmentJSON, assessmentDigest, err := canonicalStoredAuthority(stored)
	if err != nil {
		return err
	}
	if snapshotDigest != expectedSnapshotDigest || assessmentDigest != expectedAssessmentDigest ||
		!bytes.Equal(snapshotJSON, expectedSnapshot) || !bytes.Equal(assessmentJSON, expectedAssessment) {
		return fmt.Errorf("%w: 已保存风险权威完整内容与输入不一致", domain.ErrInvalidInput)
	}
	return nil
}

func canonicalStoredAuthority(stored storedRiskAuthority) ([]byte, string, []byte, string, error) {
	var snapshot hazard.Snapshot
	if err := json.Unmarshal(stored.snapshotJSON, &snapshot); err != nil {
		return nil, "", nil, "", fmt.Errorf("解码已保存风险快照: %w", err)
	}
	var assessment risk.Assessment
	if err := json.Unmarshal(stored.assessmentJSON, &assessment); err != nil {
		return nil, "", nil, "", fmt.Errorf("解码已保存风险评估: %w", err)
	}
	if err := validateCompleteAnalysis(snapshot, nil); err != nil {
		return nil, "", nil, "", fmt.Errorf("校验已保存风险快照: %w", err)
	}
	if err := validateRiskAssessmentBinding(snapshot, assessment); err != nil {
		return nil, "", nil, "", fmt.Errorf("校验已保存风险权威绑定: %w", err)
	}
	snapshotJSON, snapshotDigest, err := payloadDigest(snapshot)
	if err != nil {
		return nil, "", nil, "", err
	}
	assessmentJSON, assessmentDigest, err := payloadDigest(assessment)
	if err != nil {
		return nil, "", nil, "", err
	}
	if snapshotDigest != stored.snapshotDigest || assessmentDigest != stored.assessmentDigest {
		return nil, "", nil, "", fmt.Errorf("%w: 已保存风险权威摘要自校验失败", domain.ErrInvalidInput)
	}
	return snapshotJSON, snapshotDigest, assessmentJSON, assessmentDigest, nil
}

func payloadDigest(value any) ([]byte, string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

func isUTCTimestamp(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

func riskAssessmentUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

const insertRiskAssessmentSQL = `INSERT INTO risk_assessments (
    snapshot_id,assessment_id,hazard_type,rule_version,status,data_status,evaluated_at,
    snapshot,snapshot_digest,assessment,assessment_digest
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (snapshot_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id
WHERE risk_assessments.snapshot=EXCLUDED.snapshot
    AND risk_assessments.snapshot_digest=EXCLUDED.snapshot_digest
    AND risk_assessments.assessment=EXCLUDED.assessment
    AND risk_assessments.assessment_digest=EXCLUDED.assessment_digest
RETURNING snapshot,snapshot_digest,assessment,assessment_digest`

const selectRiskAssessmentSQL = `SELECT snapshot,snapshot_digest,assessment,assessment_digest
    FROM risk_assessments WHERE snapshot_id=$1`

const riskAssessmentSnapshotWhere = ` WHERE id=$1 AND analysis_complete=TRUE
    AND status IN ('available','stale') FOR SHARE`
