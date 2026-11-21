package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func importTestDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type failingDeleteObjects struct {
	ObjectStore
	fail map[string]bool
}

func (s failingDeleteObjects) Delete(ctx context.Context, key string) error {
	if s.fail[key] {
		return errors.New("object store unavailable")
	}
	return s.ObjectStore.Delete(ctx, key)
}

// The reaper deletes the staging objects of expired and aborted sessions, leaves a live session and its objects alone,
// treats an object that is already gone as done, and keeps a session whose objects it could not delete for the next pass.
func TestImportSessionReaperRemovesStagingObjectsOfFinishedSessionsOnly(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	objects := objectstore.NewMemoryStore()
	now := time.Now().UTC()
	create := func(id, key string, expiresIn time.Duration, digests ...string) repository.ImportSession {
		t.Helper()
		blobs := make([]repository.ImportBlob, len(digests))
		for i, digest := range digests {
			blobs[i] = repository.ImportBlob{Digest: digest, Size: 3}
			if err := objects.Put(ctx, repository.ImportStagingKey(id, digest), []byte("abc")); err != nil {
				t.Fatal(err)
			}
		}
		session, _, err := store.CreateImportSession(ctx, repository.ImportSession{ID: id, TargetRepositoryID: "00000000-0000-7000-8000-000000000001", Principal: "peer", Format: repository.FormatRaw,
			Coordinate: "pkg@1.0", Digest: "sha256:abc", FormatManifest: json.RawMessage(`{}`), IdempotencyKey: key, CreatedAt: now, ExpiresAt: now.Add(expiresIn)}, blobs, repository.ImportLimits{})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	expired := create("00000000-0000-7000-8000-0000000000e1", "expired", -time.Minute, importTestDigest("e1"), importTestDigest("gone"))
	aborted := create("00000000-0000-7000-8000-0000000000a1", "aborted", time.Hour, importTestDigest("a1"))
	live := create("00000000-0000-7000-8000-0000000000b1", "live", time.Hour, importTestDigest("l1"))
	if err := store.AbortImportSession(ctx, aborted.ID, now); err != nil {
		t.Fatal(err)
	}
	// One object of the expired session was never uploaded: deleting it is not an error.
	if err := objects.Delete(ctx, repository.ImportStagingKey(expired.ID, importTestDigest("gone"))); err != nil {
		t.Fatal(err)
	}
	has := func(id, digest string) bool {
		_, err := objects.Stat(ctx, repository.ImportStagingKey(id, digest))
		return err == nil
	}

	broken := failingDeleteObjects{ObjectStore: objects, fail: map[string]bool{repository.ImportStagingKey(expired.ID, importTestDigest("e1")): true}}
	reaper := ImportSessionReaper{Store: store, Objects: broken, Now: func() time.Time { return now }}
	reaped, err := reaper.Run(ctx)
	if err == nil {
		t.Fatal("a failed delete was not reported")
	}
	if reaped != 1 || has(aborted.ID, importTestDigest("a1")) {
		t.Fatalf("the aborted session was not reaped: reaped=%d", reaped)
	}
	if got, _ := store.GetImportSession(ctx, expired.ID); got.State != repository.ImportSessionOpen {
		t.Fatalf("a session whose object could not be deleted was finished: %s", got.State)
	}

	reaper.Objects = objects
	if reaped, err = reaper.Run(ctx); err != nil || reaped != 1 {
		t.Fatalf("second pass reaped %d, %v", reaped, err)
	}
	if has(expired.ID, importTestDigest("e1")) {
		t.Fatal("the expired session's staging object survived")
	}
	if got, _ := store.GetImportSession(ctx, expired.ID); got.State != repository.ImportSessionExpired {
		t.Fatalf("expired session = %s", got.State)
	}
	if !has(live.ID, importTestDigest("l1")) {
		t.Fatal("a live session's staging object was deleted")
	}
	if got, _ := store.GetImportSession(ctx, live.ID); got.State != repository.ImportSessionOpen {
		t.Fatalf("live session = %s", got.State)
	}
	// Once the live session expires too it goes the same way; nothing is left to do after that.
	reaper.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if reaped, err = reaper.Run(ctx); err != nil || reaped != 1 || has(live.ID, importTestDigest("l1")) {
		t.Fatalf("late pass reaped %d, %v", reaped, err)
	}
	if reaped, err = reaper.Run(ctx); err != nil || reaped != 0 {
		t.Fatalf("idle pass reaped %d, %v", reaped, err)
	}
}

func TestImportSessionReaperBatchesAndStartIsANoOpWithoutDependencies(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	objects := objectstore.NewMemoryStore()
	now := time.Now().UTC()
	for _, id := range []string{"00000000-0000-7000-8000-0000000000c1", "00000000-0000-7000-8000-0000000000c2", "00000000-0000-7000-8000-0000000000c3"} {
		if _, _, err := store.CreateImportSession(ctx, repository.ImportSession{ID: id, TargetRepositoryID: id, Principal: "peer", Format: repository.FormatRaw, Coordinate: "p@1", Digest: "sha256:abc",
			FormatManifest: json.RawMessage(`{}`), IdempotencyKey: id, CreatedAt: now, ExpiresAt: now.Add(-time.Minute)}, nil, repository.ImportLimits{}); err != nil {
			t.Fatal(err)
		}
	}
	reaper := ImportSessionReaper{Store: store, Objects: objects, Batch: 2, Now: func() time.Time { return now }}
	if reaped, err := reaper.Run(ctx); err != nil || reaped != 3 {
		t.Fatalf("reaped %d, %v; want every session over two batches", reaped, err)
	}
	ImportSessionReaper{}.Start(ctx) // no store, no objects: nothing starts
}
