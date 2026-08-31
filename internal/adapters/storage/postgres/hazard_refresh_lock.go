package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/ports"
)

const analysisRefreshUnlockTimeout = 5 * time.Second

type analysisRefreshLease struct {
	connection *pgx.Conn
	key        int64
	once       sync.Once
	err        error
}

var _ ports.HazardAnalysisRefreshLease = (*analysisRefreshLease)(nil)

// LockAnalysisRefresh 使用 PostgreSQL 会话锁串行化同一分析族的完整刷新过程。
func (r *HazardRepository) LockAnalysisRefresh(ctx context.Context,
	selector hazard.AnalysisSelector,
) (ports.HazardAnalysisRefreshLease, error) {
	if err := validateAnalysisSelector(selector); err != nil {
		return nil, err
	}
	connection, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("获取灾害分析刷新数据库连接: %w", err)
	}
	key := analysisRefreshLockKey(selector)
	if _, err = connection.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return nil, errors.Join(fmt.Errorf("获取灾害分析刷新锁: %w", err),
			closeLockedConnection(connection))
	}
	return &analysisRefreshLease{connection: connection, key: key}, nil
}

func (l *analysisRefreshLease) Release() error {
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), analysisRefreshUnlockTimeout)
		defer cancel()
		var unlocked bool
		err := l.connection.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", l.key).Scan(&unlocked)
		if err == nil && unlocked {
			l.err = closeLockedConnection(l.connection)
			return
		}
		l.err = errors.Join(err, closeLockedConnection(l.connection))
		if l.err == nil {
			l.err = fmt.Errorf("PostgreSQL 未释放灾害分析刷新锁")
		}
	})
	return l.err
}

func closeLockedConnection(connection *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), analysisRefreshUnlockTimeout)
	defer cancel()
	return connection.Close(ctx)
}

func analysisRefreshLockKey(selector hazard.AnalysisSelector) int64 {
	payload := strings.Join([]string{string(selector.HazardType), selector.ModelName,
		selector.TransformVersion, selector.Provider, selector.Dataset}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
