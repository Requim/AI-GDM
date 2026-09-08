package collection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

type retentionStub struct {
	store        *lhasaAnalysisStoreStub
	calls        int
	err          error
	beforeCommit bool
}

func (s *retentionStub) RetainArtifact(context.Context, provenance.Provenance) error {
	s.calls++
	s.beforeCommit = s.store.saveCalls == 0 || s.store.saveErr != nil
	return s.err
}

func TestLHASARetentionRunsOnlyAfterSuccessfulCommit(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{true: "failed_commit", false: "success"}[failure], func(t *testing.T) {
			now := lhasaNow()
			artifact := lhasaFixtureArtifact(now.Add(-time.Hour))
			snapshot, zones := lhasaFixtureAnalysis(artifact, "transform-v1")
			store := &lhasaAnalysisStoreStub{latestErr: domain.ErrNotFound}
			if failure {
				store.saveErr = errors.New("commit failed")
			}
			retainer := &retentionStub{store: store, err: errors.New("filesystem cleanup pending")}
			collector := newLHASATestCollector(t, &lhasaDiscoveryStub{artifact: artifact},
				&lhasaFetcherStub{artifact: artifact}, &lhasaProcessorStub{snapshot: snapshot, zones: zones}, store, now).
				WithRetention(retainer, nil)
			got, _, err := collector.Collect(context.Background())
			if failure {
				if err == nil || retainer.calls != 0 {
					t.Fatalf("失败时进行了文件清理: %v", err)
				}
				return
			}
			if err != nil || got.ID != snapshot.ID || retainer.calls != 1 || retainer.beforeCommit {
				t.Fatalf("提交后清理语义错误: snapshot=%s err=%v retainer=%+v", got.ID, err, retainer)
			}
			if store.reconcileCalls != 0 {
				t.Fatal("替代完成前使旧边界失效")
			}
		})
	}
}
