package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// LossAssessmentRepository 使用 PostgreSQL 保存可回放的损失评估和来源引用。
type LossAssessmentRepository struct{ pool *pgxpool.Pool }

var _ ports.LossAssessmentWriter = (*LossAssessmentRepository)(nil)
var _ ports.LossAssessmentReader = (*LossAssessmentRepository)(nil)

// NewLossAssessmentRepository 创建损失评估仓储适配器。
func NewLossAssessmentRepository(pool *pgxpool.Pool) *LossAssessmentRepository {
	return &LossAssessmentRepository{pool: pool}
}

// SaveAssessment 保存不可变的损失评估；同一标识再次写入必须内容一致。
func (r *LossAssessmentRepository) SaveAssessment(ctx context.Context, value loss.Assessment) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("%w: 损失评估数据库连接为空", domain.ErrInvalidInput)
	}
	if err := value.Validate(); err != nil {
		return err
	}
	assessment, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码损失评估 %s: %w", value.ID, err)
	}
	refs, err := json.Marshal(value.InputReferences)
	if err != nil {
		return fmt.Errorf("编码损失评估 %s 来源: %w", value.ID, err)
	}
	_, err = r.pool.Exec(ctx, insertLossAssessmentSQL, value.ID, value.SnapshotID, value.HazardType, value.RegionCode, value.FormulaVersion, value.Status, value.CalculatedAt, assessment, refs)
	if err != nil {
		return fmt.Errorf("保存损失评估 %s: %w", value.ID, err)
	}
	existing, err := r.GetAssessment(ctx, value.ID)
	if err != nil {
		return fmt.Errorf("核对已保存损失评估 %s: %w", value.ID, err)
	}
	left, _ := json.Marshal(existing)
	if !bytes.Equal(left, assessment) {
		return fmt.Errorf("%w: 损失评估标识 %s 已绑定其他内容", domain.ErrInvalidInput, value.ID)
	}
	return nil
}

// GetAssessment 按标识读取损失评估并重新校验其可回放结构。
func (r *LossAssessmentRepository) GetAssessment(ctx context.Context, id string) (loss.Assessment, error) {
	if r == nil || r.pool == nil || id == "" {
		return loss.Assessment{}, fmt.Errorf("%w: 损失评估查询参数无效", domain.ErrInvalidInput)
	}
	var payload []byte
	err := r.pool.QueryRow(ctx, selectLossAssessmentSQL, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return loss.Assessment{}, domain.ErrNotFound
	}
	if err != nil {
		return loss.Assessment{}, fmt.Errorf("读取损失评估 %s: %w", id, err)
	}
	var value loss.Assessment
	if err = json.Unmarshal(payload, &value); err != nil {
		return loss.Assessment{}, fmt.Errorf("解码损失评估 %s: %w", id, err)
	}
	if value.ID != id {
		return loss.Assessment{}, fmt.Errorf("%w: 损失评估内容标识不一致", domain.ErrInvalidInput)
	}
	if err = value.Validate(); err != nil {
		return loss.Assessment{}, fmt.Errorf("校验损失评估 %s: %w", id, err)
	}
	return value, nil
}

const insertLossAssessmentSQL = `INSERT INTO loss_assessments (
    id,snapshot_id,hazard_type,region_code,formula_version,status,calculated_at,assessment,source_references
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO NOTHING`

const selectLossAssessmentSQL = `SELECT assessment FROM loss_assessments WHERE id=$1`
