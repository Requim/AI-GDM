package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/ports"
)

// LatestHazardWriter 仅供单快照采集流程使用，不改变历史业务评估仓储的写入规则。
type LatestHazardWriter struct {
	*HazardRepository
}

// NewLatestHazardWriter 创建同灾种模型只保留最近一次完整风险快照的写入器。
func NewLatestHazardWriter(repository *HazardRepository) *LatestHazardWriter {
	return &LatestHazardWriter{HazardRepository: repository}
}

// LockAnalysisRefresh 跨处理版本串行化单快照刷新，防止原始文件清理与其他刷新交错。
func (r *LatestHazardWriter) LockAnalysisRefresh(ctx context.Context, selector hazard.AnalysisSelector) (ports.HazardAnalysisRefreshLease, error) {
	selector.TransformVersion, selector.Provider, selector.Dataset = "single-latest", "single-latest", "single-latest"
	return r.HazardRepository.LockAnalysisRefresh(ctx, selector)
}

// SaveAnalysis 在新快照和全部风险区保存成功后，同事务删除该模型的旧快照。
func (r *LatestHazardWriter) SaveAnalysis(ctx context.Context, snapshot hazard.Snapshot, zones []hazard.RiskZone) error {
	return r.saveAnalysis(ctx, snapshot, zones, true)
}

func lockRetention(ctx context.Context, tx pgx.Tx, snapshot hazard.Snapshot) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"hazard-retention|"+string(snapshot.HazardType)+"|"+snapshot.ModelName)
	if err != nil {
		return fmt.Errorf("锁定单快照替换事务: %w", err)
	}
	return nil
}

func retirePrevious(ctx context.Context, tx pgx.Tx, snapshot hazard.Snapshot) error {
	var newer bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM hazard_snapshots WHERE hazard_type=$1 AND model_name=$2 AND id<>$3
		AND source->>'provider'=$4 AND source->>'dataset'=$5
		AND COALESCE((source->>'publishedAt')::timestamptz,valid_from)>$6
		AND analysis_complete=TRUE)`, snapshot.HazardType, snapshot.ModelName, snapshot.ID,
		snapshot.Source.Provider, snapshot.Source.Dataset, snapshot.Source.ValidFrom).Scan(&newer)
	if err != nil {
		return fmt.Errorf("检查单快照时间顺序: %w", err)
	}
	if newer {
		return fmt.Errorf("%w: 拒绝用较旧来源替换新快照", domain.ErrInvalidInput)
	}
	if err = prepareRetirement(ctx, tx, snapshot); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM hazard_snapshots
		WHERE hazard_type=$1 AND model_name=$2 AND id<>$3`, snapshot.HazardType, snapshot.ModelName, snapshot.ID)
	if err != nil {
		return fmt.Errorf("清理旧风险快照及关联结果: %w", err)
	}
	return nil
}

func prepareRetirement(ctx context.Context, tx pgx.Tx, snapshot hazard.Snapshot) error {
	_, err := tx.Exec(ctx, `SELECT set_config('ai_gdm.retiring_analyses',
		COALESCE(jsonb_agg(a.id)::text,'[]'),TRUE) FROM spatial_analyses a
		JOIN hazard_snapshots s ON s.id=a.snapshot_id
		WHERE s.hazard_type=$1 AND s.model_name=$2 AND s.id<>$3`,
		snapshot.HazardType, snapshot.ModelName, snapshot.ID)
	if err != nil {
		return fmt.Errorf("标记当前事务待回收的旧空间分析: %w", err)
	}
	_, err = tx.Exec(ctx, `DELETE FROM risk_assessments WHERE snapshot_id IN (
		SELECT id FROM hazard_snapshots WHERE hazard_type=$1 AND model_name=$2 AND id<>$3)`,
		snapshot.HazardType, snapshot.ModelName, snapshot.ID)
	if err != nil {
		return fmt.Errorf("回收旧快照固化风险评估: %w", err)
	}
	return nil
}
