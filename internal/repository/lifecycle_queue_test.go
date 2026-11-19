package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestLifecycleJobRegistryDeclaresRepoConcurrencyAndChannels(t *testing.T) {
	if got := LifecycleJobRepoConcurrency(LifecycleJobReclaim); got != 1 {
		t.Fatalf("reclaim concurrency=%d", got)
	}
	if got := LifecycleJobRepoConcurrency(LifecycleJobScan); got != 4 {
		t.Fatalf("scan concurrency=%d", got)
	}
	if got := LifecycleJobRepoConcurrency("unregistered"); got != DefaultLifecycleRepoConcurrency {
		t.Fatalf("unregistered concurrency=%d", got)
	}
	if got := LifecycleJobChannel(LifecycleJobPromotion); got != "notarx_jobs_promotion" {
		t.Fatalf("channel=%q", got)
	}
	for _, kind := range LifecycleJobKinds() {
		if !knownPostgresNotificationChannel(LifecycleJobChannel(kind)) {
			t.Errorf("the notifier does not listen on %s", LifecycleJobChannel(kind))
		}
	}
	if !knownPostgresNotificationChannel(WebhookDeliveryChannel) {
		t.Error("the notifier does not listen on the webhook delivery channel")
	}
	if knownPostgresNotificationChannel("notarx_lifecycle_jobs") {
		t.Error("the shared lifecycle channel was replaced by per-kind channels")
	}
}

func TestMemoryLifecycleClaimHonoursRepoConcurrency(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "queue", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(kind LifecycleJobKind, n int) {
		for i := 0; i < n; i++ {
			key := uuid.NewString()
			if _, _, err := store.EnqueueLifecycleJob(ctx, LifecycleJob{ID: uuid.NewString(), RepositoryID: repo.ID, Kind: kind, IdempotencyKey: key, Payload: []byte(`{"format":"raw"}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	enqueue(LifecycleJobScan, 6)
	jobs, err := store.ClaimLifecycleJobsByKind(ctx, LifecycleJobScan, 10)
	if err != nil || len(jobs) != 4 {
		t.Fatalf("scan claim=%d err=%v, want the scan RepoConcurrency of 4", len(jobs), err)
	}
	enqueue(LifecycleJobReclaim, 1)
	if jobs, err = store.ClaimLifecycleJobsByKind(ctx, LifecycleJobReclaim, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("reclaim claimed beside four running scans: %d err=%v", len(jobs), err)
	}
}

func TestQueueClaimObserverReceivesScannedRows(t *testing.T) {
	var holder queueClaimObserverHolder
	holder.record(LifecycleJobScan, 3) // no observer set: a no-op
	var got int
	holder.set(func(_ LifecycleJobKind, scanned int) { got = scanned })
	holder.record(LifecycleJobScan, 7)
	if got != 7 {
		t.Fatalf("observed %d", got)
	}
	holder.set(nil)
	holder.record(LifecycleJobScan, 9)
	if got != 7 {
		t.Fatalf("a cleared observer still received %d", got)
	}
}
