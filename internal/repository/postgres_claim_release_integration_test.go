//go:build integration

package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A released job is actionable again at once, with the attempt of its claim returned; a lease that is no
// longer the caller's cannot be released.
func TestPostgresReleaseLifecycleJobLeaseReturnsTheJobUnrun(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	repositoryID := fixture.repository("release")
	fixture.enqueue(repositoryID, LifecycleJobScan, 1)
	ctx := context.Background()
	var id string
	if err := store.db.QueryRowContext(ctx, `UPDATE lifecycle_jobs SET state='running',started_at=now(),next_attempt_at=NULL,
		lease_expires_at=now()+interval '10 minutes',lease_token='parked-token',attempts=attempts+1
		WHERE repository_id=$1::uuid RETURNING id::text`, repositoryID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLifecycleJobLease(ctx, id, "another-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release with a foreign token: %v, want ErrNotFound", err)
	}
	if err := store.ReleaseLifecycleJobLease(ctx, id, "parked-token"); err != nil {
		t.Fatal(err)
	}
	var state, token string
	var attempts int
	var due bool
	if err := store.db.QueryRowContext(ctx, `SELECT state,lease_token,attempts,next_attempt_at<=clock_timestamp() FROM lifecycle_jobs WHERE id=$1::uuid`, id).Scan(&state, &token, &attempts, &due); err != nil {
		t.Fatal(err)
	}
	if state != string(LifecycleJobPending) || token != "" || attempts != 0 || !due {
		t.Fatalf("released job state=%s token=%q attempts=%d due=%t, want pending, no lease, 0 attempts, due", state, token, attempts, due)
	}
	if err := store.ReleaseLifecycleJobLease(ctx, id, "parked-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second release: %v, want ErrNotFound", err)
	}
	// A job that had run before goes back pending with its earlier attempts; an expired lease is the
	// scheduler's to recover.
	if _, err := store.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='running',attempts=2,lease_expires_at=now()+interval '10 minutes',lease_token='second-token' WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLifecycleJobLease(ctx, id, "second-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state,attempts FROM lifecycle_jobs WHERE id=$1::uuid`, id).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != string(LifecycleJobPending) || attempts != 1 {
		t.Fatalf("released retry state=%s attempts=%d, want pending with 1 attempt", state, attempts)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='running',lease_expires_at=now()-interval '1 second',lease_token='expired-token' WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLifecycleJobLease(ctx, id, "expired-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release of an expired lease: %v, want ErrNotFound", err)
	}
}

func TestPostgresReleaseReplicationPlanLeaseReturnsThePlanUnrun(t *testing.T) {
	store := openClaimStore(t)
	ctx := context.Background()
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "release-source-" + uuid.NewString(), Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "release-target-" + uuid.NewString(), Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM replication_plans WHERE source_repository_id=$1 OR target_repository_id=$2`, source.ID, target.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id IN ($1,$2)`, source.ID, target.ID)
	})
	digest := "sha256:" + strings.Repeat("c", 64)
	plan, _, err := store.CreateReplicationPlan(ctx, ReplicationPlan{
		ID: uuid.NewString(), SourceRepositoryID: source.ID, TargetRepositoryID: target.ID,
		Format: FormatRaw, Coordinate: "releases/release.bin", Digest: digest, IdempotencyKey: "release-" + uuid.NewString(),
	}, []ReplicationCheckpoint{{ObjectKey: "native/raw/release", Digest: digest, Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE replication_plans SET state='running',started_at=now(),lease_expires_at=now()+interval '10 minutes',lease_token='parked-token',attempts=attempts+1 WHERE id=$1::uuid`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.ReleaseReplicationPlanLease(ctx, plan.ID, "another-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release with a foreign token: %v, want ErrNotFound", err)
	}
	if err = store.ReleaseReplicationPlanLease(ctx, plan.ID, "parked-token"); err != nil {
		t.Fatal(err)
	}
	released, err := store.GetReplicationPlan(ctx, source.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if released.State != "pending" || released.LeaseToken != "" || released.Attempts != 0 || !released.NextAttemptAt.IsZero() || !released.LeaseExpiresAt.Before(time.Now().Add(time.Second)) {
		t.Fatalf("released plan %+v, want pending, no lease, 0 attempts, claimable now", released)
	}
}
