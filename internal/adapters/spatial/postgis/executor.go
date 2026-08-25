package postgis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

// Executor 使用 PostGIS 在一致事务中计算并保存空间分析。
type Executor struct {
	pool *pgxpool.Pool
}

// New 创建 PostGIS 空间分析执行器。
func New(pool *pgxpool.Pool) *Executor {
	return &Executor{pool: pool}
}

// Execute 锁定完整灾害快照，计算真实面积并原子保存结果。
func (e *Executor) Execute(ctx context.Context, snapshotID string,
	calculatedAt time.Time,
) (spatialanalysis.Analysis, error) {
	if err := validateRequest(e.pool, snapshotID, calculatedAt); err != nil {
		return spatialanalysis.Analysis{}, err
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("开始空间分析事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockCompleteSnapshot(ctx, tx, snapshotID); err != nil {
		return spatialanalysis.Analysis{}, err
	}
	input, err := calculateInput(ctx, tx, snapshotID, calculatedAt)
	if err != nil {
		return spatialanalysis.Analysis{}, err
	}
	value, err := spatialanalysis.NewAnalysis(input)
	if err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("构造空间分析结果: %w", err)
	}
	stored, err := persistAnalysis(ctx, tx, value)
	if err != nil {
		return spatialanalysis.Analysis{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return spatialanalysis.Analysis{}, fmt.Errorf("提交空间分析事务: %w", err)
	}
	return stored, nil
}

func validateRequest(pool *pgxpool.Pool, snapshotID string, calculatedAt time.Time) error {
	if pool == nil {
		return fmt.Errorf("%w: PostGIS 连接池为空", domain.ErrInvalidInput)
	}
	if snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) {
		return fmt.Errorf("%w: 灾害快照标识无效", domain.ErrInvalidInput)
	}
	if calculatedAt.IsZero() {
		return fmt.Errorf("%w: 空间分析时间为空", domain.ErrInvalidInput)
	}
	_, offset := calculatedAt.Zone()
	if offset != 0 {
		return fmt.Errorf("%w: 空间分析时间必须是 UTC", domain.ErrInvalidInput)
	}
	return nil
}

func lockCompleteSnapshot(ctx context.Context, tx pgx.Tx, snapshotID string) error {
	var complete bool
	err := tx.QueryRow(ctx, `SELECT analysis_complete FROM hazard_snapshots
        WHERE id=$1 FOR SHARE`, snapshotID).Scan(&complete)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("锁定灾害快照 %s: %w", snapshotID, err)
	}
	if !complete {
		return fmt.Errorf("%w: 灾害快照尚未形成完整风险区集合", domain.ErrInsufficientData)
	}
	return nil
}
