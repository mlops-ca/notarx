package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryImportSessionStore(t *testing.T) {
	store := NewMemoryStore()
	exerciseImportSessionStore(t, store, "memory-"+uuid.NewString()[:8], func() int64 {
		store.mu.RLock()
		defer store.mu.RUnlock()
		var staged int64
		for _, session := range store.importSessions {
			if session.State.Live() && session.ExpiresAt.After(time.Now()) {
				staged += session.Bytes
			}
		}
		return staged
	})
}

type importFixture struct {
	t         *testing.T
	store     ImportSessionStore
	principal string
	now       time.Time
	limits    ImportLimits
}

func (f importFixture) create(key, coordinate string, blobs ...ImportBlob) (ImportSession, bool, error) {
	return f.createAs(f.principal, key, coordinate, blobs...)
}

func (f importFixture) createAs(principal, key, coordinate string, blobs ...ImportBlob) (ImportSession, bool, error) {
	f.t.Helper()
	return f.store.CreateImportSession(context.Background(), f.session(principal, key, coordinate), blobs, f.limits)
}

// session is the request of a principal for a key: every (principal, key) has its own target repository, so the keys of
// one test never collide with another's, and a key reused by the same principal for the same target is a replay.
func (f importFixture) session(principal, key, coordinate string) ImportSession {
	return ImportSession{ID: uuid.NewString(), TargetRepositoryID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(principal+"/"+key)).String(), Principal: principal, Format: FormatRaw,
		Coordinate: coordinate, Digest: "sha256:" + strings.Repeat("a", 64), FormatManifest: json.RawMessage(`{"claimed":true}`), IdempotencyKey: key,
		ChainPath: []PromotionChainHop{{Repository: "dev", RuleID: "rule-1", At: f.now}}, Evidence: json.RawMessage(`{"scan":"clean"}`),
		CreatedAt: f.now, ExpiresAt: f.now.Add(time.Hour)}
}

func blob(digest string, size int64) ImportBlob { return ImportBlob{Digest: digest, Size: size} }

// dg is a well-formed digest named by a short label.
func dg(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// exerciseImportSessionStore is the contract of ImportSessionStore, run against the in-memory and PostgreSQL stores.
// stagedNow reports the claimed bytes of every live session of the store, the ones of other tests included (a shared test
// database), so the staging quota can be set relative to them.
func exerciseImportSessionStore(t *testing.T, store ImportSessionStore, principal string, stagedNow func() int64) {
	t.Helper()
	ctx := context.Background()
	f := importFixture{t: t, store: store, principal: principal, now: time.Now().UTC().Truncate(time.Microsecond),
		limits: ImportLimits{MaxBlobBytes: 100, MaxSessionBytes: 250, MaxBlobsPerSession: 3, MaxOpenSessionsPerPrincipal: 3}}
	missingDigests := func(id string) []string {
		t.Helper()
		missing, err := store.MissingImportBlobs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		digests := make([]string, len(missing))
		for i, m := range missing {
			digests[i] = m.Digest
		}
		return digests
	}

	// A session records the claims as received, the deduplicated blobs in digest order and their total.
	session, replayed, err := f.create("k1", "pkg@1.0", blob(dg("b"), 40), blob(dg("a"), 10), blob(dg("b"), 40))
	if err != nil || replayed {
		t.Fatalf("create = %v replayed=%t", err, replayed)
	}
	if session.State != ImportSessionOpen || session.Bytes != 50 || session.Principal != principal || session.Coordinate != "pkg@1.0" || string(session.FormatManifest) == "" ||
		len(session.ChainPath) != 1 || session.ChainPath[0].RuleID != "rule-1" || !strings.Contains(string(session.Evidence), "clean") || !session.ExpiresAt.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("session = %+v", session)
	}
	if got, getErr := store.GetImportSession(ctx, session.ID); getErr != nil || got.ID != session.ID || got.Bytes != 50 {
		t.Fatalf("get = %+v %v", got, getErr)
	}
	if _, getErr := store.GetImportSession(ctx, uuid.NewString()); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("unknown session = %v", getErr)
	}
	if got := missingDigests(session.ID); len(got) != 2 || got[0] != min(dg("a"), dg("b")) || got[1] != max(dg("a"), dg("b")) {
		t.Fatalf("missing = %v", got)
	}
	if got, blobErr := store.GetImportBlob(ctx, session.ID, dg("a")); blobErr != nil || got.Size != 10 || got.Verified || got.StagingKey != ImportStagingKey(session.ID, dg("a")) {
		t.Fatalf("blob = %+v %v", got, blobErr)
	}
	if all, listErr := store.ListImportBlobs(ctx, session.ID); listErr != nil || len(all) != 2 || all[0].Digest != min(dg("a"), dg("b")) || all[1].Digest != max(dg("a"), dg("b")) {
		t.Fatalf("list = %+v %v", all, listErr)
	}
	if _, listErr := store.ListImportBlobs(ctx, uuid.NewString()); !errors.Is(listErr, ErrNotFound) {
		t.Fatalf("list of an unknown session = %v", listErr)
	}
	if _, blobErr := store.GetImportBlob(ctx, session.ID, dg("zzz")); !errors.Is(blobErr, ErrNotFound) {
		t.Fatalf("unclaimed blob = %v", blobErr)
	}
	if _, blobErr := store.GetImportBlob(ctx, uuid.NewString(), dg("a")); !errors.Is(blobErr, ErrNotFound) {
		t.Fatalf("blob of an unknown session = %v", blobErr)
	}
	if _, missErr := store.MissingImportBlobs(ctx, uuid.NewString()); !errors.Is(missErr, ErrNotFound) {
		t.Fatalf("missing of an unknown session = %v", missErr)
	}

	// The idempotency key: the same request finds its session, any other request under the key conflicts.
	again, replayed, err := f.create("k1", "pkg@1.0", blob(dg("a"), 10))
	if err != nil || !replayed || again.ID != session.ID {
		t.Fatalf("replay = %+v replayed=%t %v", again, replayed, err)
	}
	if _, _, err = f.create("k1", "other@1.0", blob(dg("a"), 10)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("a different request under the key = %v", err)
	}
	if _, _, err = f.createAs(principal+"-other", "k1", "pkg@1.0"); err != nil {
		t.Fatalf("the same key of another principal and target must not collide: %v", err)
	}
	// The same key on the same target for another principal is a conflict, not a replay.
	hijack := f.session(principal+"-thief", "k1", "pkg@1.0")
	hijack.TargetRepositoryID = session.TargetRepositoryID
	if _, _, err = store.CreateImportSession(ctx, hijack, nil, f.limits); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("another principal replaying the key = %v", err)
	}

	// Invalid claims.
	for name, claims := range map[string][]ImportBlob{
		"empty digest":   {blob("", 1)},
		"short digest":   {blob("sha256:b", 1)},
		"path traversal": {blob("sha256:../../../blobs/sha256/"+strings.Repeat("a", 64), 1)},
		"upper case":     {blob("sha256:"+strings.Repeat("A", 64), 1)},
		"negative size":  {blob(dg("z"), -1)},
		"conflicting":    {blob(dg("z"), 1), blob(dg("z"), 2)},
	} {
		if _, _, err = f.create("invalid-"+name, "pkg@1.0", claims...); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}

	// Every limit is refused with its typed error and writes nothing.
	refused := func(key string, want error, blobs ...ImportBlob) {
		t.Helper()
		request := f.session(f.principal, key, "limit@1.0")
		if _, _, createErr := store.CreateImportSession(ctx, request, blobs, f.limits); !errors.Is(createErr, want) {
			t.Fatalf("%s = %v, want %v", key, createErr, want)
		}
		if _, getErr := store.GetImportSession(ctx, request.ID); !errors.Is(getErr, ErrNotFound) {
			t.Fatalf("%s left a session behind: %v", key, getErr)
		}
	}
	refused("big-blob", ErrImportBlobTooLarge, blob(dg("big"), 101))
	refused("big-session", ErrImportSessionTooLarge, blob(dg("1"), 100), blob(dg("2"), 100), blob(dg("3"), 51))
	refused("many-blobs", ErrImportTooManyBlobs, blob(dg("1"), 1), blob(dg("2"), 1), blob(dg("3"), 1), blob(dg("4"), 1))
	// The principal holds one live session (k1) and two more fit; the fourth is refused, another principal is not.
	for _, key := range []string{"open-2", "open-3"} {
		if _, _, err = f.create(key, "open@1.0", blob(dg("o"), 1)); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	refused("open-4", ErrImportTooManySessions, blob(dg("o"), 1))
	if _, _, err = f.createAs(principal+"-second", "open-4", "open@1.0", blob(dg("o"), 1)); err != nil {
		t.Fatalf("another principal is refused: %v", err)
	}
	// An aborted session frees its slot.
	if err = store.AbortImportSession(ctx, session.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.create("open-4", "open@1.0", blob(dg("o"), 1)); err != nil {
		t.Fatalf("a slot freed by an abort was refused: %v", err)
	}

	// The staging quota counts the claimed bytes of every live session.
	quota := f
	quota.limits = ImportLimits{StagingQuotaBytes: stagedNow() + 40}
	quota.principal = principal + "-quota"
	if _, _, err = quota.create("q1", "quota@1.0", blob(dg("q"), 40)); err != nil {
		t.Fatalf("a session that fits the quota: %v", err)
	}
	if _, _, err = quota.createAs(principal+"-quota2", "q2", "quota@1.0", blob(dg("q"), 1)); !errors.Is(err, ErrImportStagingQuota) {
		t.Fatalf("a session over the quota = %v", err)
	}

	// A dead session gives its key to the retry: a sender whose push stalled or was aborted starts again under the same key.
	unlimited := f
	unlimited.limits = ImportLimits{}
	retry, replayed, err := unlimited.create("k1", "pkg@1.0", blob(dg("a"), 10))
	if err != nil || replayed || retry.ID == session.ID {
		t.Fatalf("a retry after the abort = %+v replayed=%t %v", retry, replayed, err)
	}
	if old, getErr := store.GetImportSession(ctx, session.ID); getErr != nil || old.State != ImportSessionAborted {
		t.Fatalf("the aborted session must stay for the reaper: %+v %v", old, getErr)
	}
	if again, replayed, createErr := unlimited.create("k1", "pkg@1.0"); createErr != nil || !replayed || again.ID != retry.ID {
		t.Fatalf("the retry is itself replayable: %+v replayed=%t %v", again, replayed, createErr)
	}
	late := unlimited
	late.now = f.now.Add(2 * time.Hour)
	if lateRetry, replayed, createErr := late.create("k1", "pkg@1.0", blob(dg("a"), 10)); createErr != nil || replayed || lateRetry.ID == retry.ID {
		t.Fatalf("a retry after the session lapsed = %+v replayed=%t %v", lateRetry, replayed, createErr)
	}
	// The key of a dead session is freed whatever the retry asks for, and only if the retry is created: a retry that a limit
	// refuses leaves the key where it was.
	dead := f.session(principal+"-dead", "dead-key", "dead@1.0")
	if _, _, err = store.CreateImportSession(ctx, dead, nil, f.limits); err != nil {
		t.Fatal(err)
	}
	if err = store.AbortImportSession(ctx, dead.ID, f.now); err != nil {
		t.Fatal(err)
	}
	different := f.session(principal+"-dead", "dead-key", "other@2.0")
	different.Digest = dg("republished")
	tight := f.limits
	tight.MaxBlobBytes = 1
	if _, _, err = store.CreateImportSession(ctx, different, []ImportBlob{blob(dg("big"), 5)}, tight); !errors.Is(err, ErrImportBlobTooLarge) {
		t.Fatalf("a refused retry = %v", err)
	}
	if again, replayed, createErr := store.CreateImportSession(ctx, f.session(principal+"-dead", "dead-key", "dead@1.0"), nil, f.limits); createErr != nil || replayed || again.ID == dead.ID {
		t.Fatalf("the dead key after a refused retry: %+v replayed=%t %v", again, replayed, createErr)
	}
	if err = store.AbortImportSession(ctx, dead.ID, f.now); err != nil {
		t.Fatalf("the dead session must stay reachable: %v", err)
	}
	// Claims that overflow the total are refused as too large, not wrapped around.
	huge := f.session(principal+"-huge", "huge", "huge@1.0")
	if _, _, err = store.CreateImportSession(ctx, huge, []ImportBlob{blob(dg("h1"), math.MaxInt64), blob(dg("h2"), 2)}, ImportLimits{}); !errors.Is(err, ErrImportSessionTooLarge) {
		t.Fatalf("an overflowing claim = %v", err)
	}
	// Aborting twice changes nothing, not even the update time.
	if err = store.AbortImportSession(ctx, retry.ID, f.now); err != nil {
		t.Fatal(err)
	}
	first, _ := store.GetImportSession(ctx, retry.ID)
	if err = store.AbortImportSession(ctx, retry.ID, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if second, _ := store.GetImportSession(ctx, retry.ID); !second.UpdatedAt.Equal(first.UpdatedAt) || second.State != ImportSessionAborted {
		t.Fatalf("a second abort changed the session: %+v -> %+v", first, second)
	}
	// An omitted manifest is stored as an empty object.
	bare := f.session(principal+"-bare", "bare", "bare@1.0")
	bare.FormatManifest = nil
	if stored, _, createErr := store.CreateImportSession(ctx, bare, nil, f.limits); createErr != nil || string(stored.FormatManifest) != "{}" {
		t.Fatalf("a session without a manifest = %+v %v", stored, createErr)
	}

	// Blobs: the size must match the claim, an unclaimed digest is unknown, recording twice changes nothing.
	live, _, err := f.createAs(principal+"-blobs", "blobs", "blobs@1.0", blob(dg("x"), 5), blob(dg("y"), 7))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordImportBlob(ctx, live.ID, dg("x"), 6, f.now); !errors.Is(err, ErrImportBlobSizeMismatch) {
		t.Fatalf("a wrong size = %v", err)
	}
	if err = store.RecordImportBlob(ctx, live.ID, dg("nope"), 5, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unclaimed blob = %v", err)
	}
	if err = store.RecordImportBlob(ctx, uuid.NewString(), dg("x"), 5, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown session = %v", err)
	}
	if err = store.BeginImportCommit(ctx, live.ID, f.now); !errors.Is(err, ErrImportBlobsMissing) {
		t.Fatalf("commit with blobs missing = %v", err)
	}
	for range 2 {
		if err = store.RecordImportBlob(ctx, live.ID, dg("x"), 5, f.now); err != nil {
			t.Fatal(err)
		}
	}
	if got := missingDigests(live.ID); len(got) != 1 || got[0] != dg("y") {
		t.Fatalf("missing after one blob = %v", got)
	}
	if err = store.RecordImportBlob(ctx, live.ID, dg("y"), 7, f.now.Add(2*time.Hour)); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("a blob after the expiry = %v", err)
	}
	if err = store.RecordImportBlob(ctx, live.ID, dg("y"), 7, f.now); err != nil {
		t.Fatal(err)
	}
	if got := missingDigests(live.ID); len(got) != 0 {
		t.Fatalf("missing after every blob = %v", got)
	}

	// Commit: open to committing (twice is fine), no more blobs, then committed; a final state refuses everything.
	if err = store.BeginImportCommit(ctx, live.ID, f.now.Add(2*time.Hour)); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("a commit after the expiry = %v", err)
	}
	for range 2 {
		if err = store.BeginImportCommit(ctx, live.ID, f.now); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.AbortImportSession(ctx, live.ID, f.now); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("aborting a session under its commit = %v", err)
	}
	if err = store.RecordImportBlob(ctx, live.ID, dg("y"), 7, f.now); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("a blob while committing = %v", err)
	}
	if err = store.CompleteImportSession(ctx, live.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetImportSession(ctx, live.ID); got.State != ImportSessionCommitted {
		t.Fatalf("state = %s", got.State)
	}
	if err = store.CompleteImportSession(ctx, live.ID, f.now); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("completing twice = %v", err)
	}
	if err = store.AbortImportSession(ctx, live.ID, f.now); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("aborting a committed session = %v", err)
	}
	if err = store.AbortImportSession(ctx, uuid.NewString(), f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("aborting an unknown session = %v", err)
	}

	exerciseImportReaper(t, f)
}

// exerciseImportReaper checks which sessions the reaper takes and what it hands to the object store.
func exerciseImportReaper(t *testing.T, f importFixture) {
	t.Helper()
	ctx := context.Background()
	store := f.store
	f.principal += "-reaper"
	mk := func(key string, blobs ...ImportBlob) ImportSession {
		t.Helper()
		session, _, err := f.createAs(f.principal+key, key, "reap@1.0", blobs...)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	expired := mk("expired", blob(dg("e1"), 1), blob(dg("e2"), 2))
	aborted := mk("aborted", blob(dg("a1"), 1))
	live := mk("live", blob(dg("l1"), 1))
	committed := mk("committed", blob(dg("c1"), 1))
	if err := store.AbortImportSession(ctx, aborted.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordImportBlob(ctx, committed.ID, dg("c1"), 1, f.now); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginImportCommit(ctx, committed.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteImportSession(ctx, committed.ID, f.now); err != nil {
		t.Fatal(err)
	}
	// The sessions made above expire an hour after f.now; "expired" is made past its expiry by reaping from the future,
	// and the live one is kept by giving it a later expiry.
	late := ImportSession{ID: uuid.NewString(), TargetRepositoryID: uuid.NewString(), Principal: f.principal, Format: FormatRaw, Coordinate: "late@1.0", Digest: "sha256:" + strings.Repeat("b", 64),
		FormatManifest: json.RawMessage(`{}`), IdempotencyKey: "late", CreatedAt: f.now, ExpiresAt: f.now.Add(48 * time.Hour)}
	if _, _, err := store.CreateImportSession(ctx, late, []ImportBlob{blob(dg("late"), 1)}, ImportLimits{}); err != nil {
		t.Fatal(err)
	}
	_ = live

	var removed []string
	remove := func(_ context.Context, keys []string) error {
		removed = append(removed, keys...)
		return nil
	}
	mine := func(keys []string) []string {
		var out []string
		for _, key := range keys {
			for _, id := range []string{expired.ID, aborted.ID, live.ID, committed.ID, late.ID} {
				if strings.Contains(key, id) {
					out = append(out, key)
				}
			}
		}
		return out
	}
	reapAt := f.now.Add(2 * time.Hour)
	// A remove that fails leaves the session as it is and is reported.
	failing := func(context.Context, []string) error { return errors.New("object store down") }
	if _, err := store.ReapImportSessions(ctx, reapAt, 10000, failing); err == nil {
		t.Fatal("a failed removal was not reported")
	}
	if got, _ := store.GetImportSession(ctx, expired.ID); got.State != ImportSessionOpen {
		t.Fatalf("a session whose staging objects could not be removed was finished: %s", got.State)
	}
	finished, err := store.ReapImportSessions(ctx, reapAt, 10000, remove)
	if err != nil || finished < 4 {
		t.Fatalf("reap finished %d, %v", finished, err)
	}
	keys := mine(removed)
	want := map[string]bool{ImportStagingKey(expired.ID, dg("e1")): true, ImportStagingKey(expired.ID, dg("e2")): true, ImportStagingKey(aborted.ID, dg("a1")): true,
		ImportStagingKey(live.ID, dg("l1")): true, ImportStagingKey(committed.ID, dg("c1")): true}
	if len(keys) != len(want) {
		t.Fatalf("staging keys removed = %v", keys)
	}
	for _, key := range keys {
		if !want[key] {
			t.Fatalf("unexpected staging key %s removed (the later-expiring session must be left alone)", key)
		}
	}
	for id, state := range map[string]ImportSessionState{expired.ID: ImportSessionExpired, live.ID: ImportSessionExpired, aborted.ID: ImportSessionAborted, committed.ID: ImportSessionCommitted, late.ID: ImportSessionOpen} {
		if got, getErr := store.GetImportSession(ctx, id); getErr != nil || got.State != state {
			t.Fatalf("session %s after the reap = %v %v, want %s", id, got.State, getErr, state)
		}
	}
	// The keys are emptied: the next pass finds nothing of these, and the expired session frees its capacity.
	removed = nil
	if _, err = store.ReapImportSessions(ctx, reapAt, 10000, remove); err != nil || len(mine(removed)) != 0 {
		t.Fatalf("second reap removed %v, %v", mine(removed), err)
	}
	if err = store.RecordImportBlob(ctx, expired.ID, dg("e1"), 1, f.now); !errors.Is(err, ErrImportSessionClosed) {
		t.Fatalf("a blob of an expired session = %v", err)
	}
	// Once the later session expires too it is reaped like the others.
	removed = nil
	if _, err = store.ReapImportSessions(ctx, f.now.Add(72*time.Hour), 10000, remove); err != nil || len(mine(removed)) != 1 {
		t.Fatalf("the late session's reap removed %v, %v", mine(removed), err)
	}
	if got, _ := store.GetImportSession(ctx, late.ID); got.State != ImportSessionExpired {
		t.Fatalf("late session = %s", got.State)
	}

	// A commit that is still running keeps its staging objects: BeginImportCommit pushes the expiry out by the grace.
	soon := f.session(f.principal+"-commit", "commit", "commit@1.0")
	soon.ExpiresAt = f.now.Add(5 * time.Minute)
	if _, _, err = store.CreateImportSession(ctx, soon, []ImportBlob{blob(dg("m1"), 4)}, ImportLimits{}); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordImportBlob(ctx, soon.ID, dg("m1"), 4, f.now); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginImportCommit(ctx, soon.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetImportSession(ctx, soon.ID); got.ExpiresAt.Before(f.now.Add(ImportCommitGrace)) {
		t.Fatalf("a committing session expires at %v, before the grace %v", got.ExpiresAt, f.now.Add(ImportCommitGrace))
	}
	removed = nil
	if _, err = store.ReapImportSessions(ctx, f.now.Add(10*time.Minute), 10000, remove); err != nil || strings.Contains(strings.Join(removed, " "), soon.ID) {
		t.Fatalf("a committing session was reaped inside its grace: %v %v", removed, err)
	}
	if got, _ := store.GetImportSession(ctx, soon.ID); got.State != ImportSessionCommitting {
		t.Fatalf("state = %s", got.State)
	}
	if _, err = store.ReapImportSessions(ctx, f.now.Add(2*time.Hour), 10000, remove); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetImportSession(ctx, soon.ID); got.State != ImportSessionExpired {
		t.Fatalf("a commit stuck past its grace = %s", got.State)
	}

	// A session whose objects cannot be deleted does not hold up the others of the pass.
	stuck, healthy := mk("stuck", blob(dg("s1"), 1)), mk("healthy", blob(dg("h1"), 1))
	selective := func(_ context.Context, keys []string) error {
		for _, key := range keys {
			if strings.Contains(key, stuck.ID) {
				return errors.New("object store refuses " + key)
			}
		}
		return nil
	}
	finished, err = store.ReapImportSessions(ctx, f.now.Add(72*time.Hour), 10000, selective)
	if err == nil || finished < 1 {
		t.Fatalf("finished %d, %v; want the healthy session finished and the failure reported", finished, err)
	}
	if got, _ := store.GetImportSession(ctx, healthy.ID); got.State != ImportSessionExpired {
		t.Fatalf("a healthy session behind a stuck one = %s", got.State)
	}
	if got, _ := store.GetImportSession(ctx, stuck.ID); got.State != ImportSessionOpen {
		t.Fatalf("the stuck session = %s", got.State)
	}
}
