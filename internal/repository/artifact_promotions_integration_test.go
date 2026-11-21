//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func promotionIntegrationStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPostgresPromotionProvenanceFollowsTheJobInOneTransaction(t *testing.T) {
	store := promotionIntegrationStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "prov-source-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "prov-target-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "prov-" + suffix, EndpointURL: "https://events.example.test/p",
		SecretCiphertext: "encrypted-secret", Enabled: true, EventTypes: []WebhookEventType{WebhookEventArtifactPromoted, WebhookEventPromotionFailed}})
	if err != nil {
		t.Fatal(err)
	}
	var jobIDs []string
	t.Cleanup(func() {
		for _, id := range jobIDs {
			_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE payload->>'operationId'=$1`, id)
			_, _ = store.db.ExecContext(context.Background(), `DELETE FROM artifact_promotions WHERE job_id=$1::uuid`, id)
		}
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM webhook_subscriptions WHERE id=$1`, subscription.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM lifecycle_jobs WHERE repository_id=$1`, target.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id IN ($1,$2)`, source.ID, target.ID)
	})
	digest := "sha256:" + strings.Repeat("a", 64)
	enqueue := func(key string) (LifecycleJob, bool) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"format": "raw", "sourceRepositoryId": source.ID, "path": "a.bin", "digest": digest})
		job, replayed, enqueueErr := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, key+suffix), LifecycleJob{
			ID: uuid.NewString(), RepositoryID: target.ID, Kind: LifecycleJobPromotion, IdempotencyKey: key + suffix, Payload: payload, MaxAttempts: 1})
		if enqueueErr != nil {
			t.Fatal(enqueueErr)
		}
		jobIDs = append(jobIDs, job.ID)
		return job, replayed
	}
	rowOf := func(job LifecycleJob) ArtifactPromotion {
		t.Helper()
		row, getErr := store.GetArtifactPromotionByJob(ctx, job.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		return row
	}

	job, replayed := enqueue("ok")
	row := rowOf(job)
	if replayed || row.State != ArtifactPromotionQueued || row.Attempt != 1 || row.TargetRepositoryID != target.ID || row.RequestedBy != "alice" || row.OriginRepository != "source" {
		t.Fatalf("replayed=%t row=%+v", replayed, row)
	}
	again, replayed := enqueue("ok")
	if !replayed || again.ID != job.ID || rowOf(again).ID != row.ID {
		t.Fatalf("replay job=%+v replayed=%t", again, replayed)
	}
	var rows int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM artifact_promotions WHERE target_repository_id=$1::uuid`, target.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != job.ID || rowOf(job).State != ArtifactPromotionRunning {
		t.Fatalf("claimed=%+v err=%v state=%s", claimed, err, rowOf(job).State)
	}
	if err = store.CompleteLifecycleJob(ctx, job.ID, claimed[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	done := rowOf(job)
	if done.State != ArtifactPromotionCompleted || done.CompletedAt.IsZero() {
		t.Fatalf("done=%+v", done)
	}
	var eventCount int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE type='artifact.promoted' AND payload->>'promotionId'=$1 AND dedupe_key LIKE '%|'||$1`, done.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("artifact.promoted events=%d err=%v", eventCount, err)
	}

	// Tightening the target policy between enqueue and execution refuses the job.
	refused, _ := enqueue("refused")
	policy, err := store.GetRepositorySecurityPolicy(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := policy.Version
	policy.Enabled, policy.RequireSBOM = true, true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, target.ID, policy, version); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("refused job was claimed: %+v err=%v", claimed, err)
	}
	stored, err := store.GetLifecycleJob(ctx, target.ID, refused.ID)
	if err != nil || stored.State != LifecycleJobFailed || !strings.HasPrefix(stored.LastError, PromotionPolicyRefusalPrefix) || !strings.Contains(stored.LastError, "sbom_required") {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
	failed := rowOf(refused)
	if failed.State != ArtifactPromotionFailed {
		t.Fatalf("failed row=%+v", failed)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE type='promotion.failed' AND payload->>'promotionId'=$1`, failed.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("promotion.failed events=%d err=%v", eventCount, err)
	}

	// chain_path is append-only.
	hop := PromotionChainHop{Repository: "source", At: time.Now().UTC()}
	hops, _ := json.Marshal([]PromotionChainHop{hop})
	if _, err = store.db.ExecContext(ctx, `UPDATE artifact_promotions SET chain_path=$2::jsonb WHERE id=$1::uuid`, done.ID, hops); err != nil {
		t.Fatalf("append: %v", err)
	}
	more, _ := json.Marshal([]PromotionChainHop{hop, {Repository: "target", At: time.Now().UTC()}})
	if _, err = store.db.ExecContext(ctx, `UPDATE artifact_promotions SET chain_path=$2::jsonb WHERE id=$1::uuid`, done.ID, more); err != nil {
		t.Fatalf("second append: %v", err)
	}
	for name, rewrite := range map[string]string{"empty": `[]`, "rewritten": `[{"instance":"","repository":"other","ruleId":"","at":"2026-01-01T00:00:00Z"},{"instance":"","repository":"target","ruleId":"","at":"2026-01-01T00:00:00Z"}]`} {
		if _, err = store.db.ExecContext(ctx, `UPDATE artifact_promotions SET chain_path=$2::jsonb WHERE id=$1::uuid`, done.ID, rewrite); err == nil {
			t.Fatalf("%s chain_path was accepted", name)
		}
	}
}

// TestPostgresArtifactPromotionsKeepTheirPlans pins the plans of the idempotent-replay lookup and the pending-state
// sweep (ADR 0023 section 5): both must use their index on a table the size of a busy registry.
func TestPostgresArtifactPromotionsKeepTheirPlans(t *testing.T) {
	store := promotionIntegrationStore(t)
	ctx := context.Background()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	target := uuid.NewString()
	// 30000 settled promotions over 300 targets, and 20 unfinished ones: the sweep reads the few, the replay lookup one target.
	if _, err = conn.ExecContext(ctx, `INSERT INTO artifact_promotions (id,target_repository_id,format,coordinate,digest,requested_by,state,idempotency_key,created_at)
SELECT gen_random_uuid(),(SELECT ('00000000-0000-4000-8000-'||lpad((n%300)::text,12,'0'))::uuid),'raw','file-'||n||'.bin','sha256:'||md5(n::text),'alice',CASE WHEN n<=20 THEN 'queued' ELSE 'completed' END,'key-'||n,now()-n*interval '1 second'
FROM generate_series(1,30000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO artifact_promotions (id,target_repository_id,format,coordinate,digest,requested_by,state,idempotency_key) VALUES (gen_random_uuid(),$1::uuid,'raw','known.bin','sha256:known','alice','completed','key-known')`, target); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "ANALYZE artifact_promotions"); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name, index, query string
		args               []any
	}{
		{"replay by target, coordinate and digest", "artifact_promotions_target_coordinate_digest_idx",
			`SELECT ` + artifactPromotionColumns + ` FROM artifact_promotions WHERE target_repository_id=$1::uuid AND coordinate=$2 AND digest=$3`, []any{target, "known.bin", "sha256:known"}},
		{"replay by idempotency key", "artifact_promotions_idempotency_key_attempt_key",
			`SELECT ` + artifactPromotionColumns + ` FROM artifact_promotions WHERE idempotency_key=$1 ORDER BY attempt DESC LIMIT 1`, []any{"key-known"}},
		{"pending-state sweep", "artifact_promotions_pending_idx",
			`SELECT id FROM artifact_promotions WHERE state IN ('pending_approval','queued','running')`, nil},
		{"provenance of a job", "artifact_promotions_job_idx",
			`SELECT ` + artifactPromotionColumns + ` FROM artifact_promotions WHERE job_id=$1::uuid ORDER BY attempt DESC LIMIT 1`, []any{uuid.NewString()}},
	} {
		t.Run(check.name, func(t *testing.T) {
			plan, planErr := explaincheck.Plan(ctx, conn, check.query, check.args...)
			if planErr != nil {
				t.Fatal(planErr)
			}
			if !plan.UsesIndex(check.index) {
				t.Errorf("plan uses indexes %v, want %s", plan.Indexes(), check.index)
			}
			for _, relation := range plan.SequentialScans() {
				if relation == "artifact_promotions" {
					t.Errorf("Seq Scan on %s", relation)
				}
			}
		})
	}
}

// promotionRetryFixture is a source and a target repository and an enqueue helper for the claim and retry tests below.
func promotionRetryFixture(t *testing.T, store *PostgresStore) (target HostedRepository, enqueue func(key string) LifecycleJob) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "retry-source-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err = store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "retry-target-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	var jobIDs []string
	t.Cleanup(func() {
		for _, id := range jobIDs {
			_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE payload->>'operationId'=$1`, id)
			_, _ = store.db.ExecContext(context.Background(), `DELETE FROM artifact_promotions WHERE job_id=$1::uuid`, id)
		}
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM lifecycle_jobs WHERE repository_id=$1`, target.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id IN ($1,$2)`, source.ID, target.ID)
	})
	digest := "sha256:" + strings.Repeat("a", 64)
	enqueue = func(key string) LifecycleJob {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"format": "raw", "sourceRepositoryId": source.ID, "path": "a.bin", "digest": digest})
		job, _, enqueueErr := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, key+suffix), LifecycleJob{
			ID: uuid.NewString(), RepositoryID: target.ID, Kind: LifecycleJobPromotion, IdempotencyKey: key + suffix, Payload: payload, MaxAttempts: 1})
		if enqueueErr != nil {
			t.Fatal(enqueueErr)
		}
		jobIDs = append(jobIDs, job.ID)
		return job
	}
	return target, enqueue
}

// unreadablePolicies fails every read of the target's admission policy, as a store outage would.
type unreadablePolicies struct{ postgresClaimedPromotions }

func (unreadablePolicies) GetRepositorySecurityPolicy(context.Context, string) (RepositorySecurityPolicy, error) {
	return RepositorySecurityPolicy{}, errors.New("policy store unavailable")
}

// The claim re-evaluates the target's admission policy as it is at claim time. A policy tightened after the job was
// enqueued refuses it and the failed attempt is recorded; a policy that cannot be read is not a verdict, so the job goes
// back to the queue unrun with the attempt its claim counted returned, and the promotion stays queued.
func TestPostgresPromotionClaimPathRefusesOnTightenedPolicyAndReleasesOnTransientError(t *testing.T) {
	store := promotionIntegrationStore(t)
	ctx := context.Background()
	target, enqueue := promotionRetryFixture(t, store)

	transient := enqueue("transient")
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != transient.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	kept, err := refuseClaimedPromotions(ctx, unreadablePolicies{postgresClaimedPromotions{store}}, claimed)
	if err != nil || len(kept) != 0 {
		t.Fatalf("a job whose policy could not be read was kept or errored: kept=%+v err=%v", kept, err)
	}
	released, err := store.GetLifecycleJob(ctx, target.ID, transient.ID)
	if err != nil || released.State != LifecycleJobPending || released.Attempts != 0 {
		t.Fatalf("released job = %+v err=%v; want pending with the attempt returned", released, err)
	}
	if row, getErr := store.GetArtifactPromotionByJob(ctx, transient.ID); getErr != nil || row.State != ArtifactPromotionQueued || row.Attempt != 1 {
		t.Fatalf("row after the release = %+v err=%v; want queued, attempt 1", row, getErr)
	}

	// The same job is claimed again once the policy is readable, and a policy tightened since the enqueue refuses it.
	policy, err := store.GetRepositorySecurityPolicy(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := policy.Version
	policy.Enabled, policy.RequireSBOM = true, true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, target.ID, policy, version); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("a job the tightened policy refuses was claimed: %+v err=%v", claimed, err)
	}
	refused, err := store.GetLifecycleJob(ctx, target.ID, transient.ID)
	if err != nil || refused.State != LifecycleJobFailed || !strings.HasPrefix(refused.LastError, PromotionPolicyRefusalPrefix) {
		t.Fatalf("refused job = %+v err=%v", refused, err)
	}
	if row, getErr := store.GetArtifactPromotionByJob(ctx, transient.ID); getErr != nil || row.State != ArtifactPromotionFailed {
		t.Fatalf("row after the refusal = %+v err=%v", row, getErr)
	}
}

// Retrying a failed promotion job records the retry as the next attempt of the same key and job: the failed row keeps its
// outcome and its promotion.failed event, the retry is queued, and its own outcome is emitted under its own row.
func TestPostgresRetryOfAFailedPromotionRecordsTheNextAttempt(t *testing.T) {
	store := promotionIntegrationStore(t)
	ctx := context.Background()
	target, enqueue := promotionRetryFixture(t, store)
	subscription, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "retry-" + uuid.NewString()[:8], EndpointURL: "https://events.example.test/r",
		SecretCiphertext: "encrypted-secret", Enabled: true, EventTypes: []WebhookEventType{WebhookEventArtifactPromoted, WebhookEventPromotionFailed}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM webhook_subscriptions WHERE id=$1`, subscription.ID)
	})
	job := enqueue("retry")
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err = store.FailLifecycleJob(ctx, job.ID, claimed[0].LeaseToken, "target unavailable"); err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil || failed.State != ArtifactPromotionFailed || failed.Attempt != 1 {
		t.Fatalf("failed row = %+v err=%v", failed, err)
	}
	if _, err = store.RetryLifecycleJob(ctx, target.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil || retried.ID == failed.ID || retried.Attempt != 2 || retried.State != ArtifactPromotionQueued || retried.IdempotencyKey != failed.IdempotencyKey || !retried.CompletedAt.IsZero() {
		t.Fatalf("retry row = %+v err=%v (failed %+v)", retried, err, failed)
	}
	var state string
	if err = store.db.QueryRowContext(ctx, `SELECT state FROM artifact_promotions WHERE id=$1::uuid`, failed.ID).Scan(&state); err != nil || state != string(ArtifactPromotionFailed) {
		t.Fatalf("the failed attempt was rewritten: state=%q err=%v", state, err)
	}
	claimed, err = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("retry claimed=%+v err=%v", claimed, err)
	}
	if err = store.CompleteLifecycleJob(ctx, job.ID, claimed[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if row, getErr := store.GetArtifactPromotionByJob(ctx, job.ID); getErr != nil || row.ID != retried.ID || row.State != ArtifactPromotionCompleted {
		t.Fatalf("row after the retry completed = %+v err=%v", row, getErr)
	}
	for eventType, promotionID := range map[string]string{"promotion.failed": failed.ID, "artifact.promoted": retried.ID} {
		var n int
		if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE type=$1 AND payload->>'promotionId'=$2`, eventType, promotionID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s events for %s = %d err=%v, want 1", eventType, promotionID, n, err)
		}
	}
}
