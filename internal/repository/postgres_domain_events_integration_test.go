//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/partitions"
)

func domainEventsStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func countDomainEvents(t *testing.T, store *PostgresStore, dedupeKey string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events WHERE dedupe_key=$1`, dedupeKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Emit commits with the state change and rolls back with it: no event and no delivery survive a rollback, a type
// nobody consumes writes no row, and a matching subscription gets exactly one delivery.
func TestPostgresEmitIsTransactionalAndConsumerGated(t *testing.T) {
	store := domainEventsStore(t)
	ctx := context.Background()
	repoID := uuid.NewString()
	coordinate := "emit/" + uuid.NewString()
	digest := "sha256:" + strings.Repeat("c", 64)
	event := func(eventType events.Type, version string) events.Event {
		return events.Event{Type: eventType, RepositoryID: repoID, Format: "raw", Coordinate: coordinate, Digest: digest, Version: version}
	}

	// Nobody asked for artifact.promoted: Emit reports false and writes nothing, even in a committed transaction.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	quiet := event(events.ArtifactPromoted, "p1")
	if written, emitErr := events.Emit(ctx, tx, quiet); emitErr != nil || written {
		t.Fatalf("written=%v err=%v", written, emitErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, quiet.DedupeKey()); n != 0 {
		t.Fatalf("an event without a consumer was written (%d rows)", n)
	}

	subscription, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "emit-" + uuid.NewString(), EndpointURL: "https://events.example.test/e",
		SecretCiphertext: "sealed", EventTypes: []WebhookEventType{WebhookEventArtifactPromoted}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM webhook_subscriptions WHERE id=$1`, subscription.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE repository_id=$1`, repoID)
	})

	rolledBack := event(events.ArtifactPromoted, "p-rollback")
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if written, emitErr := events.Emit(ctx, tx, rolledBack); emitErr != nil || !written {
		t.Fatalf("written=%v err=%v", written, emitErr)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, rolledBack.DedupeKey()); n != 0 {
		t.Fatalf("a rolled-back event survived (%d rows)", n)
	}
	page, err := store.PageWebhookDeliveries(ctx, WebhookDeliveryQuery{SubscriptionID: subscription.ID, Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("a rolled-back delivery survived: %+v %v", page.Items, err)
	}

	committed := event(events.ArtifactPromoted, "p-commit")
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if written, emitErr := events.Emit(ctx, tx, committed); emitErr != nil || !written {
		t.Fatalf("written=%v err=%v", written, emitErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, committed.DedupeKey()); n != 1 {
		t.Fatalf("committed event rows = %d", n)
	}
	page, err = store.PageWebhookDeliveries(ctx, WebhookDeliveryQuery{SubscriptionID: subscription.ID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].EventType != WebhookEventArtifactPromoted {
		t.Fatalf("deliveries = %+v %v", page.Items, err)
	}
	// The delivery resolves its event through the partition-pruned join.
	claims, err := store.ClaimWebhookDeliveries(ctx, "emit-test", time.Now(), time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, claim := range claims {
		if claim.Subscription.ID == subscription.ID {
			found = claim.Event.Coordinate == coordinate && claim.Event.Digest == digest && claim.Event.RepositoryID == repoID && claim.Event.Format == "raw"
		}
	}
	if !found {
		t.Fatalf("claim did not carry the event identity: %+v", claims)
	}

	// The dedupe key is not unique: a second emission of the same key is a second row (the reconciler of slice 1b
	// guards with NOT EXISTS), while the version keeps a different transition apart.
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = events.Emit(ctx, tx, committed); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, committed.DedupeKey()); n != 2 {
		t.Fatalf("a repeated key was rejected or merged (%d rows)", n)
	}
}

// An internal consumer reads the log from its cursor, in order, and a consumer without a cursor starts at the present.
func TestPostgresDomainEventCursor(t *testing.T) {
	store := domainEventsStore(t)
	ctx := context.Background()
	consumer := "cursor-" + ids.New()
	repoID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_event_cursors WHERE consumer=$1`, consumer)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE repository_id=$1`, repoID)
	})
	types := []events.Type{events.ArtifactPublished}
	emit := func(coordinate string) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = events.Emit(ctx, tx, events.Event{Type: events.ArtifactPublished, RepositoryID: repoID, Format: "raw", Coordinate: coordinate}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	emit("before-cursor")
	if got, err := store.ReadEvents(ctx, consumer, types, 0, 10); err != nil || len(got) != 0 {
		t.Fatalf("history replayed: %v %v", got, err)
	}
	emit("one")
	emit("two")
	emit("three")
	// Events younger than the settle time are not read yet.
	if got, err := store.ReadEvents(ctx, consumer, types, time.Hour, 10); err != nil || len(got) != 0 {
		t.Fatalf("unsettled events read: %v %v", got, err)
	}
	got, err := store.ReadEvents(ctx, consumer, types, 0, 2)
	if err != nil || len(got) != 2 || got[0].Event.Coordinate != "one" || got[1].Event.Coordinate != "two" {
		t.Fatalf("batch = %+v %v", got, err)
	}
	if err = store.AdvanceCursor(ctx, consumer, got[1].Position); err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceCursor(ctx, consumer, got[0].Position); err != nil {
		t.Fatal(err)
	}
	got, err = store.ReadEvents(ctx, consumer, types, 0, 10)
	if err != nil || len(got) != 1 || got[0].Event.Coordinate != "three" {
		t.Fatalf("after the cursor = %+v %v", got, err)
	}
}

// The partition manager keeps the log partitioned: current partitions exist, the DEFAULT partition stays empty, and a
// partition past the 14 day retention is dropped.
func TestPostgresDomainEventsPartitionsAndRetention(t *testing.T) {
	store := domainEventsStore(t)
	ctx := context.Background()
	spec := partitions.Spec{Table: "domain_events", Column: "occurred_at", Granularity: partitions.Month, Retention: 14 * 24 * time.Hour}
	old := time.Now().UTC().AddDate(-1, 0, 0)
	oldStart := partitions.Month.Start(old)
	oldName := spec.Name(oldStart)
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+oldName+` PARTITION OF domain_events FOR VALUES FROM ('`+oldStart.Format("2006-01-02 15:04:05+00")+`') TO ('`+partitions.Month.Next(oldStart).Format("2006-01-02 15:04:05+00")+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO domain_events (id,type,dedupe_key,occurred_at) VALUES ($1,'artifact.released','retention-probe',$2)`, uuid.NewString(), oldStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	manager, err := partitions.New(store.db, []partitions.Spec{spec}, partitions.Options{Owner: "domain-events-" + ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	report, err := manager.Maintain(ctx)
	if err != nil || !report.Leader {
		t.Fatalf("maintain = %+v %v", report, err)
	}
	var exists bool
	if err = store.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, oldName).Scan(&exists); err != nil || exists {
		t.Fatalf("the expired partition %s survived (%v, %v)", oldName, exists, err)
	}
	var inDefault int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events_default`).Scan(&inDefault); err != nil || inDefault != 0 {
		t.Fatalf("DEFAULT partition holds %d rows (%v)", inDefault, err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, spec.Name(partitions.Month.Start(time.Now().UTC()))).Scan(&exists); err != nil || !exists {
		t.Fatalf("the current partition does not exist (%v, %v)", exists, err)
	}
}

// A re-scan swaps the summary and emits a new artifact.scan.completed event in the same transaction.
func TestPostgresSwapScanResultEmitsScanCompleted(t *testing.T) {
	store := domainEventsStore(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "scan-events-" + uuid.NewString(), Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "scan-events-" + uuid.NewString(), EndpointURL: "https://events.example.test/s",
		SecretCiphertext: "sealed", EventTypes: []WebhookEventType{WebhookEventArtifactScanCompleted}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM webhook_subscriptions WHERE id=$1`, subscription.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE repository_id=$1`, repo.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM artifact_vulnerability WHERE repository_id=$1`, repo.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id=$1`, repo.ID)
	})
	digest := "sha256:" + strings.Repeat("d", 64)
	swap := func(high int) ScanResult {
		t.Helper()
		result := ScanResult{ID: ids.New(), RepositoryID: repo.ID, ArtifactDigest: digest, Scanner: "trivy", Status: ScanStatusVulnerable, High: high, ScannedAt: time.Now().UTC()}
		if _, swapErr := store.SwapScanResult(ctx, result); swapErr != nil {
			t.Fatal(swapErr)
		}
		return result
	}
	first, second := swap(1), swap(1)
	for _, result := range []ScanResult{first, second} {
		if n := countDomainEvents(t, store, string(events.ArtifactScanCompleted)+"|"+repo.ID+"||"+digest+"|"+result.ID); n != 1 {
			t.Fatalf("scan %s emitted %d events, want 1 (a re-scan is a new event even with unchanged counts)", result.ID, n)
		}
	}
	var format, previous string
	if err = store.db.QueryRowContext(ctx, `SELECT format, COALESCE(payload->'previous'->>'id','') FROM domain_events WHERE dedupe_key=$1`,
		string(events.ArtifactScanCompleted)+"|"+repo.ID+"||"+digest+"|"+second.ID).Scan(&format, &previous); err != nil || format != "raw" || previous != first.ID {
		t.Fatalf("second event format=%q previous=%q err=%v", format, previous, err)
	}
}
