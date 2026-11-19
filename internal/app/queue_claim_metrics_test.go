package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestQueueClaimScannedRowsHistogram(t *testing.T) {
	metrics := &Metrics{}
	metrics.ObserveQueueClaimScannedRows(repository.LifecycleJobScan, 5)
	metrics.ObserveQueueClaimScannedRows(repository.LifecycleJobScan, 5000)
	metrics.ObserveQueueClaimScannedRows("", 1)
	metrics.ObserveQueueClaimScannedRows("bogus", 1)
	metrics.ObserveQueueClaimScannedRows(repository.LifecycleJobScan, -1)
	var out strings.Builder
	metrics.queueClaims.write(&out)
	for _, want := range []string{
		"# TYPE notarx_queue_claim_scanned_rows histogram",
		`notarx_queue_claim_scanned_rows_bucket{kind="scan",le="10"} 1`,
		`notarx_queue_claim_scanned_rows_bucket{kind="scan",le="+Inf"} 2`,
		`notarx_queue_claim_scanned_rows_sum{kind="scan"} 5005`,
		`notarx_queue_claim_scanned_rows_count{kind="all"} 1`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}

func TestMergeWakesFansInAndIgnoresNilChannels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mergeWakes(ctx, []<-chan struct{}{nil, nil}) != nil {
		t.Fatal("nil channels must merge to nil")
	}
	first, second := make(chan struct{}, 1), make(chan struct{}, 1)
	merged := mergeWakes(ctx, []<-chan struct{}{first, nil, second})
	second <- struct{}{}
	select {
	case <-merged:
	case <-time.After(5 * time.Second):
		t.Fatal("the merged wake did not fire")
	}
	close(first)
}

type recoveryCounts struct{ replication int }

func (r *recoveryCounts) RecoverExpiredReplicationPlans(context.Context, time.Time) (int, error) {
	r.replication++
	return 2, nil
}

func TestLifecycleJobRecoveryAlsoRecoversReplicationPlans(t *testing.T) {
	store := repository.NewMemoryStore()
	counts := &recoveryCounts{}
	recovered, err := LifecycleJobRecovery{Store: store, Replications: counts}.Run(context.Background())
	if err != nil || recovered != 2 || counts.replication != 1 {
		t.Fatalf("recovered=%d replication calls=%d err=%v", recovered, counts.replication, err)
	}
}
