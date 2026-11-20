//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

// metaSweepFixture is a store whose payloads go to a real meta namespace over the in-memory object store.
type metaSweepFixture struct {
	store  *PostgresStore
	meta   *objectstore.MetaStore
	repoID string
}

func newMetaSweepFixture(t *testing.T) metaSweepFixture {
	t.Helper()
	store := newPostgresNuGetStore(t)
	meta := objectstore.NewMetaStore(objectstore.NewMemoryStore(), 0)
	store.SetPayloadStore(meta, 1)
	repo := ceilingRepo(t, store, VisibilityPrivate)
	return metaSweepFixture{store: store, meta: meta, repoID: repo.ID}
}

// publish writes a payload through the store's own externalisation and records a row that references it, as a
// publish transaction does: the object first, then the row.
func (f metaSweepFixture) publish(t *testing.T, data []byte) (digest string, rowID string) {
	t.Helper()
	_, ref, err := f.store.externalize(t.Context(), data)
	if err != nil || !ref.external() {
		t.Fatalf("externalize: %+v %v", ref, err)
	}
	rowID = uuid.NewString()
	if _, err := f.store.db.ExecContext(t.Context(), `INSERT INTO repository_vex_document (id, repository_id, format, body_digest, body_key) VALUES ($1::uuid,$2::uuid,'openvex',$3,$4)`,
		rowID, f.repoID, ref.digest.String, ref.key.String); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.store.db.Exec(`DELETE FROM repository_vex_document WHERE id=$1::uuid`, rowID) })
	return ref.digest.String, rowID
}

func (f metaSweepFixture) unreference(t *testing.T, rowID string) {
	t.Helper()
	if _, err := f.store.db.ExecContext(t.Context(), `DELETE FROM repository_vex_document WHERE id=$1::uuid`, rowID); err != nil {
		t.Fatal(err)
	}
}

func (f metaSweepFixture) exists(t *testing.T, digest string) bool {
	t.Helper()
	_, err := f.meta.Get(t.Context(), digest)
	return err == nil
}

func (f metaSweepFixture) age(t *testing.T, digest string, by time.Duration) {
	t.Helper()
	if _, err := f.store.db.ExecContext(t.Context(), `UPDATE meta_object_pins SET seen_at = seen_at - make_interval(secs => $2) WHERE digest = $1`, digest, by.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func (f metaSweepFixture) sweep(t *testing.T, digest string, grace time.Duration) MetaSweepOutcome {
	t.Helper()
	outcome, err := f.store.SweepMetaObject(t.Context(), digest, grace, func(ctx context.Context) error { return f.meta.Delete(ctx, digest) })
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func TestPostgresMetaSweepHonoursTheGraceAndNeverDeletesAReferencedObject(t *testing.T) {
	f := newMetaSweepFixture(t)
	payload := []byte("sweep payload " + uuid.NewString())
	digest, rowID := f.publish(t, payload)

	// A just-written object is young, referenced or not.
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepYoung {
		t.Fatalf("a fresh object: %s", got)
	}
	// An old object a row still references is kept.
	f.age(t, digest, 2*time.Hour)
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepReferenced || !f.exists(t, digest) {
		t.Fatalf("a referenced object: %s", got)
	}
	// Once nothing references it the object is deleted, after its grace period and not before.
	f.unreference(t, rowID)
	f.age(t, digest, -3*time.Hour) // written again just now
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepYoung || !f.exists(t, digest) {
		t.Fatalf("an unreferenced object within the grace: %s", got)
	}
	f.age(t, digest, 3*time.Hour)
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepDeleted || f.exists(t, digest) {
		t.Fatalf("an unreferenced old object: %s", got)
	}
	var pins int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM meta_object_pins WHERE digest=$1`, digest).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("the pin of a deleted object survived: %d %v", pins, err)
	}
}

func TestPostgresMetaSweepPinsAnObjectNoWriterPinned(t *testing.T) {
	f := newMetaSweepFixture(t)
	digest, _, err := f.meta.Put(t.Context(), []byte("legacy payload "+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	// The object was written by a node that does not pin: the first sweep that sees it starts its grace period.
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepYoung || !f.exists(t, digest) {
		t.Fatalf("first sweep of an unpinned object: %s", got)
	}
	f.age(t, digest, 2*time.Hour)
	if got := f.sweep(t, digest, time.Hour); got != MetaSweepDeleted || f.exists(t, digest) {
		t.Fatalf("second sweep after the grace: %s", got)
	}
}

// A writer of the same bytes that arrives while the sweep deletes the object waits for the delete and then puts the
// object again, so the row it goes on to write never dangles.
func TestPostgresMetaSweepWaitsForNoWriterOfTheSameBytes(t *testing.T) {
	f := newMetaSweepFixture(t)
	payload := []byte("contended payload " + uuid.NewString())
	digest, rowID := f.publish(t, payload)
	f.unreference(t, rowID)
	f.age(t, digest, 2*time.Hour)

	deleting := make(chan struct{})
	release := make(chan struct{})
	swept := make(chan MetaSweepOutcome, 1)
	go func() {
		outcome, err := f.store.SweepMetaObject(context.Background(), digest, time.Hour, func(ctx context.Context) error {
			close(deleting)
			<-release
			return f.meta.Delete(ctx, digest)
		})
		if err != nil {
			t.Error(err)
		}
		swept <- outcome
	}()
	<-deleting
	var writerDone atomic.Bool
	written := make(chan struct{})
	go func() {
		defer close(written)
		if _, ref, err := f.store.externalize(context.Background(), payload); err != nil || !ref.external() {
			t.Errorf("externalize: %+v %v", ref, err)
		}
		writerDone.Store(true)
	}()
	time.Sleep(300 * time.Millisecond)
	if writerDone.Load() {
		t.Fatal("a writer of the object the sweep is deleting did not wait for it")
	}
	close(release)
	if got := <-swept; got != MetaSweepDeleted {
		t.Fatalf("sweep: %s", got)
	}
	<-written
	if !f.exists(t, digest) {
		t.Fatal("the writer's object is gone: the delete ran after its put")
	}
}

func TestPostgresMetaSweepNeverDeletesWhatConcurrentPublishesReference(t *testing.T) {
	f := newMetaSweepFixture(t)
	payloads := make([][]byte, 4)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("racing payload %d %s", i, uuid.NewString()))
	}
	var stop atomic.Bool
	var sweeping sync.WaitGroup
	sweeping.Add(2)
	for range 2 {
		go func() {
			defer sweeping.Done()
			for !stop.Load() {
				for i := range payloads {
					sum := metaDigest(payloads[i])
					// The grace period is an hour and the test ages the pins itself, so an object the writer is not
					// publishing is deletable and the pin protocol alone protects the one it is.
					_, _ = f.store.SweepMetaObject(context.Background(), sum, time.Hour, func(ctx context.Context) error { return f.meta.Delete(ctx, sum) })
				}
			}
		}()
	}
	for i := range 60 {
		digest, rowID := f.publish(t, payloads[i%len(payloads)])
		if !f.exists(t, digest) {
			stop.Store(true)
			sweeping.Wait()
			t.Fatalf("iteration %d: a referenced object was deleted", i)
		}
		f.unreference(t, rowID)
		// Old again and unreferenced: from here the sweep may delete the object until the next iteration pins it.
		f.age(t, digest, 2*time.Hour)
	}
	stop.Store(true)
	sweeping.Wait()
}

func metaDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestPostgresMetaSweepLeaseIsHeldByOneNodeAtATime(t *testing.T) {
	store := newPostgresNuGetStore(t)
	t.Cleanup(func() { _, _ = store.db.Exec(`DELETE FROM meta_sweep_lease`) })
	if _, err := store.db.Exec(`DELETE FROM meta_sweep_lease`); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	got, err := store.AcquireMetaSweepLease(ctx, "node-a", time.Minute, time.Hour)
	if err != nil || !got {
		t.Fatalf("first acquire: %v %v", got, err)
	}
	if got, err = store.AcquireMetaSweepLease(ctx, "node-b", time.Minute, time.Hour); err != nil || got {
		t.Fatalf("a second node acquired a held lease: %v %v", got, err)
	}
	if err = store.RenewMetaSweepLease(ctx, "node-b", time.Minute); err != ErrNotFound {
		t.Fatalf("a non-owner renewed the lease: %v", err)
	}
	if err = store.RenewMetaSweepLease(ctx, "node-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	// A sweep that did not complete frees the lease for the next node at once.
	if err = store.FinishMetaSweepLease(ctx, "node-a", false); err != nil {
		t.Fatal(err)
	}
	if got, err = store.AcquireMetaSweepLease(ctx, "node-b", time.Minute, time.Hour); err != nil || !got {
		t.Fatalf("acquire after an incomplete sweep: %v %v", got, err)
	}
	// A completed one makes the fleet wait for the interval.
	if err = store.FinishMetaSweepLease(ctx, "node-b", true); err != nil {
		t.Fatal(err)
	}
	if got, err = store.AcquireMetaSweepLease(ctx, "node-a", time.Minute, time.Hour); err != nil || got {
		t.Fatalf("acquired within the interval of a completed sweep: %v %v", got, err)
	}
	if got, err = store.AcquireMetaSweepLease(ctx, "node-a", time.Minute, 0); err != nil || !got {
		t.Fatalf("acquire after the interval: %v %v", got, err)
	}
	// An expired lease is taken over.
	if _, err = store.db.Exec(`UPDATE meta_sweep_lease SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if got, err = store.AcquireMetaSweepLease(ctx, "node-b", time.Minute, 0); err != nil || !got {
		t.Fatalf("takeover of an expired lease: %v %v", got, err)
	}
}

func TestPostgresMetaSweepPinListingAndReferenceIndexes(t *testing.T) {
	f := newMetaSweepFixture(t)
	digest := fmt.Sprintf("sha256:%064x", time.Now().UnixNano())
	if _, err := f.store.db.Exec(`INSERT INTO meta_object_pins (digest, seen_at) VALUES ($1, now() - interval '2 hours')`, digest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.store.db.Exec(`DELETE FROM meta_object_pins WHERE digest=$1`, digest) })
	pins, err := f.store.MetaObjectPinsOlderThan(t.Context(), "", time.Hour, 1000)
	if err != nil || !slices.Contains(pins, digest) {
		t.Fatalf("old pins %v %v", pins, err)
	}
	if err = f.store.ForgetMetaObjectPin(t.Context(), digest, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.store.db.QueryRow(`SELECT count(*) FROM meta_object_pins WHERE digest=$1`, digest).Scan(&count); err != nil || count != 1 {
		t.Fatalf("a pin younger than the grace was forgotten: %d %v", count, err)
	}
	if err = f.store.ForgetMetaObjectPin(t.Context(), digest, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err = f.store.db.QueryRow(`SELECT count(*) FROM meta_object_pins WHERE digest=$1`, digest).Scan(&count); err != nil || count != 0 {
		t.Fatalf("an old pin survived: %d %v", count, err)
	}
	// The reference check reads every body_key from its index, never the table.
	var plan string
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(`EXPLAIN `+metaObjectReferencedQuery, "meta/sha256/"+fmt.Sprintf("%064x", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	for _, entry := range metaReferenceTables {
		if want := entry.table + "_body_key_idx"; !strings.Contains(plan, want) {
			t.Errorf("the reference check does not use %s:\n%s", want, plan)
		}
	}
}
