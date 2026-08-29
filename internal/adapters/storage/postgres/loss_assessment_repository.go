package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	maxStoredLossAssessmentBytes = 1 << 20
	maxStoredLossReferencesBytes = 1 << 20
	maxStoredLossIdentityBytes   = 128
	maxStoredLossStatusBytes     = 64
)

type lossAssessmentTransaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type lossAssessmentBegin func(context.Context, pgx.TxOptions) (lossAssessmentTransaction, error)

// LossAssessmentRepository 使用 PostgreSQL 保存可回放的损失评估和来源引用。
type LossAssessmentRepository struct {
	pool  *pgxpool.Pool
	begin lossAssessmentBegin
}

type storedLossAssessment struct {
	metadataWithinBounds bool
	snapshotID           string
	hazardType           string
	regionCode           string
	formulaVersion       string
	status               string
	calculatedAt         time.Time
	assessmentBytes      int64
	referenceBytes       int64
	assessment           []byte
	sourceReferences     []byte
}

var _ ports.LossAssessmentWriter = (*LossAssessmentRepository)(nil)
var _ ports.LossAssessmentReader = (*LossAssessmentRepository)(nil)

// NewLossAssessmentRepository 创建损失评估仓储适配器。
func NewLossAssessmentRepository(pool *pgxpool.Pool) *LossAssessmentRepository {
	repository := &LossAssessmentRepository{pool: pool}
	if pool != nil {
		repository.begin = func(ctx context.Context, options pgx.TxOptions) (lossAssessmentTransaction, error) {
			return pool.BeginTx(ctx, options)
		}
	}
	return repository
}

// SaveAssessment 保存不可变的损失评估；同一标识再次写入必须内容一致。
func (r *LossAssessmentRepository) SaveAssessment(ctx context.Context, value loss.Assessment) error {
	if r == nil || r.pool == nil || r.begin == nil {
		return fmt.Errorf("%w: 损失评估数据库连接为空", domain.ErrInvalidInput)
	}
	if err := value.Validate(); err != nil {
		return err
	}
	assessment, refs, err := encodeStoredLossAssessment(value)
	if err != nil {
		return err
	}
	tx, err := r.begin(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始损失评估事务 %s: %w", value.ID, err)
	}
	defer rollbackLossAssessment(tx)
	_, err = tx.Exec(ctx, insertLossAssessmentSQL, value.ID, value.SnapshotID, value.HazardType,
		value.RegionCode, value.FormulaVersion, value.Status, value.CalculatedAt, assessment, refs)
	if err != nil {
		return fmt.Errorf("保存损失评估 %s: %w", value.ID, err)
	}
	existing, err := readStoredLossAssessment(ctx, tx, value.ID)
	if err != nil {
		return fmt.Errorf("核对已保存损失评估 %s: %w", value.ID, err)
	}
	left, _ := json.Marshal(existing)
	if !bytes.Equal(left, assessment) {
		return fmt.Errorf("%w: 损失评估标识 %s 已绑定其他内容: %w",
			ports.ErrStoredAssessmentIntegrity, value.ID, domain.ErrInvalidInput)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交损失评估 %s: %w", value.ID, err)
	}
	return nil
}

func rollbackLossAssessment(tx lossAssessmentTransaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func encodeStoredLossAssessment(value loss.Assessment) ([]byte, []byte, error) {
	assessment, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("编码损失评估 %s: %w", value.ID, err)
	}
	refs, err := json.Marshal(value.InputReferences)
	if err != nil {
		return nil, nil, fmt.Errorf("编码损失评估 %s 来源: %w", value.ID, err)
	}
	if len(assessment) > maxStoredLossAssessmentBytes || len(refs) > maxStoredLossReferencesBytes {
		return nil, nil, fmt.Errorf("%w: 损失评估存储内容超过字节预算", domain.ErrInvalidInput)
	}
	return assessment, refs, nil
}

// GetAssessment 按标识读取损失评估并重新校验其可回放结构。
func (r *LossAssessmentRepository) GetAssessment(ctx context.Context, id string) (loss.Assessment, error) {
	if r == nil || r.pool == nil || strings.TrimSpace(id) == "" || len(id) > maxStoredLossIdentityBytes {
		return loss.Assessment{}, fmt.Errorf("%w: 损失评估查询参数无效", domain.ErrInvalidInput)
	}
	return readStoredLossAssessment(ctx, r.pool, id)
}

type lossAssessmentQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readStoredLossAssessment(ctx context.Context, queryer lossAssessmentQueryer, id string) (loss.Assessment, error) {
	stored, err := queryStoredLossAssessment(ctx, queryer, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return loss.Assessment{}, domain.ErrNotFound
	}
	if err != nil {
		return loss.Assessment{}, fmt.Errorf("读取损失评估 %s: %w", id, err)
	}
	if err = validateStoredLossPayloadBounds(stored); err != nil {
		return loss.Assessment{}, storedLossDecodeError(id, "字节预算", err)
	}
	return decodeStoredLossAssessment(id, stored)
}

func queryStoredLossAssessment(ctx context.Context, queryer lossAssessmentQueryer,
	id string,
) (storedLossAssessment, error) {
	var stored storedLossAssessment
	err := queryer.QueryRow(ctx, selectLossAssessmentSQL, id, maxStoredLossAssessmentBytes,
		maxStoredLossReferencesBytes, maxStoredLossIdentityBytes, maxStoredLossStatusBytes).Scan(
		&stored.metadataWithinBounds, &stored.snapshotID, &stored.hazardType, &stored.regionCode, &stored.formulaVersion,
		&stored.status, &stored.calculatedAt, &stored.assessmentBytes, &stored.referenceBytes,
		&stored.assessment, &stored.sourceReferences,
	)
	return stored, err
}

func validateStoredLossPayloadBounds(stored storedLossAssessment) error {
	if !stored.metadataWithinBounds || stored.assessmentBytes <= 0 || stored.assessmentBytes > maxStoredLossAssessmentBytes ||
		stored.referenceBytes <= 0 || stored.referenceBytes > maxStoredLossReferencesBytes {
		return fmt.Errorf("%w: PostgreSQL 存储元数据或 JSON 超过边界", domain.ErrInvalidInput)
	}
	if len(stored.assessment) == 0 || len(stored.sourceReferences) == 0 {
		return fmt.Errorf("%w: PostgreSQL 未返回有界存储 JSON", domain.ErrInvalidInput)
	}
	return nil
}

func decodeStoredLossAssessment(id string, stored storedLossAssessment) (loss.Assessment, error) {
	var value loss.Assessment
	if err := decodeStrictStoredJSON(stored.assessment, maxStoredLossAssessmentBytes, &value); err != nil {
		return loss.Assessment{}, storedLossDecodeError(id, "内容", err)
	}
	var references []string
	if err := decodeStrictStoredJSON(stored.sourceReferences, maxStoredLossReferencesBytes, &references); err != nil {
		return loss.Assessment{}, storedLossDecodeError(id, "来源", err)
	}
	if !storedLossAssessmentMatches(id, value, references, stored) {
		return loss.Assessment{}, fmt.Errorf("%w: 损失评估审计列与内容不一致: %w",
			ports.ErrStoredAssessmentIntegrity, domain.ErrInvalidInput)
	}
	if err := value.Validate(); err != nil {
		return loss.Assessment{}, fmt.Errorf("%w: 校验损失评估 %s: %w",
			ports.ErrStoredAssessmentIntegrity, id, err)
	}
	return value, nil
}

func decodeStrictStoredJSON(payload []byte, maximum int, destination any) error {
	if len(payload) == 0 || len(payload) > maximum {
		return fmt.Errorf("%w: 存储 JSON 字节预算无效", domain.ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.Join(domain.ErrInvalidInput, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.Join(domain.ErrInvalidInput, fmt.Errorf("存储 JSON 包含尾随内容"))
	}
	storedCanonical, err := canonicalStoredJSON(payload)
	if err != nil {
		return errors.Join(domain.ErrInvalidInput, err)
	}
	typed, err := json.Marshal(destination)
	if err != nil {
		return errors.Join(domain.ErrInvalidInput, err)
	}
	typedCanonical, err := canonicalStoredJSON(typed)
	if err != nil || !bytes.Equal(storedCanonical, typedCanonical) {
		return errors.Join(domain.ErrInvalidInput, fmt.Errorf("存储 JSON 字段大小写或结构不符合固定 schema"))
	}
	return nil
}

func canonicalStoredJSON(payload []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("存储 JSON 包含尾随内容")
	}
	return json.Marshal(value)
}

func storedLossDecodeError(id, section string, err error) error {
	return fmt.Errorf("%w: 解码损失评估 %s %s: %w", ports.ErrStoredAssessmentIntegrity, id, section, err)
}

func storedLossAssessmentMatches(id string, value loss.Assessment, references []string,
	stored storedLossAssessment,
) bool {
	return value.ID == id && value.SnapshotID == stored.snapshotID && value.HazardType == stored.hazardType &&
		value.RegionCode == stored.regionCode && value.FormulaVersion == stored.formulaVersion &&
		string(value.Status) == stored.status && value.CalculatedAt.Equal(stored.calculatedAt) &&
		slices.Equal(value.InputReferences, references)
}

const insertLossAssessmentSQL = `INSERT INTO loss_assessments (
    id,snapshot_id,hazard_type,region_code,formula_version,status,calculated_at,assessment,source_references
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO NOTHING`

const selectLossAssessmentSQL = `WITH candidate AS (
    SELECT snapshot_id,hazard_type,region_code,formula_version,status,calculated_at,assessment,source_references,
      assessment_bytes,source_references_bytes AS reference_bytes,
      (OCTET_LENGTH(snapshot_id) BETWEEN 1 AND $4 AND
       OCTET_LENGTH(hazard_type) BETWEEN 1 AND $4 AND
       OCTET_LENGTH(region_code) BETWEEN 1 AND $4 AND
       OCTET_LENGTH(formula_version) BETWEEN 1 AND $4 AND
       OCTET_LENGTH(status) BETWEEN 1 AND $5) AS metadata_within_bounds
    FROM loss_assessments WHERE id=$1
) SELECT metadata_within_bounds,
    CASE WHEN metadata_within_bounds THEN snapshot_id ELSE '' END,
    CASE WHEN metadata_within_bounds THEN hazard_type ELSE '' END,
    CASE WHEN metadata_within_bounds THEN region_code ELSE '' END,
    CASE WHEN metadata_within_bounds THEN formula_version ELSE '' END,
    CASE WHEN metadata_within_bounds THEN status ELSE '' END,calculated_at,
    assessment_bytes,reference_bytes,
    CASE WHEN metadata_within_bounds AND assessment_bytes BETWEEN 1 AND $2 AND
      reference_bytes BETWEEN 1 AND $3 THEN assessment END,
    CASE WHEN metadata_within_bounds AND assessment_bytes BETWEEN 1 AND $2 AND
      reference_bytes BETWEEN 1 AND $3 THEN source_references END
FROM candidate`
