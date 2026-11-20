package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	discovery "github.com/mlops-ca/notarx/internal/ocidiscovery"
)

// memoryPayloadStore is an in-memory PayloadStore that counts its calls.
type memoryPayloadStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	gets    int
}

func newMemoryPayloadStore() *memoryPayloadStore {
	return &memoryPayloadStore{objects: map[string][]byte{}}
}

func (m *memoryPayloadStore) Put(_ context.Context, data []byte) (string, string, error) {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.objects[digest] = bytes.Clone(data)
	return digest, "meta/sha256/" + hex.EncodeToString(sum[:]), nil
}

func (m *memoryPayloadStore) Get(_ context.Context, digest string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	data, ok := m.objects[digest]
	if !ok {
		return nil, errors.New("not found")
	}
	return bytes.Clone(data), nil
}

type failingPayloadStore struct{ memoryPayloadStore }

func (*failingPayloadStore) Put(context.Context, []byte) (string, string, error) {
	return "", "", errors.New("store down")
}

func TestExternalizeKeepsSmallPayloadsInline(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	data := []byte("0123456789")

	inline, ref, err := store.externalize(t.Context(), data)
	if err != nil || ref.external() || !bytes.Equal(inline, data) {
		t.Fatalf("no payload store: inline=%q ref=%+v err=%v", inline, ref, err)
	}
	store.SetPayloadStore(payloads, len(data))
	inline, ref, err = store.externalize(t.Context(), data)
	if err != nil || ref.external() || !bytes.Equal(inline, data) || payloads.puts != 0 {
		t.Fatalf("at the threshold: inline=%q ref=%+v puts=%d err=%v", inline, ref, payloads.puts, err)
	}
	if digest, size, key := ref.columns(); digest != nil || size != nil || key != nil {
		t.Fatalf("inline columns must be NULL: %v %v %v", digest, size, key)
	}
}

func TestExternalizeMovesLargePayloadsAndInternalizeReadsThemBack(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 4)
	data := []byte("0123456789")

	inline, ref, err := store.externalize(t.Context(), data)
	if err != nil || !ref.external() || inline == nil || len(inline) != 0 {
		t.Fatalf("above the threshold: inline=%q (nil=%v) ref=%+v err=%v", inline, inline == nil, ref, err)
	}
	digest, size, key := ref.columns()
	if digest != ref.digest.String || size != int64(len(data)) || key != "meta/sha256/"+ref.digest.String[len("sha256:"):] {
		t.Fatalf("columns: %v %v %v", digest, size, key)
	}
	got, err := store.internalize(t.Context(), inline, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if got, err = store.internalize(t.Context(), []byte("inline"), payloadRef{}); err != nil || string(got) != "inline" || payloads.gets != 1 {
		t.Fatalf("inline passthrough: %q gets=%d err=%v", got, payloads.gets, err)
	}
}

func TestExternalizeJSONUsesThePlaceholder(t *testing.T) {
	store := &PostgresStore{}
	store.SetPayloadStore(newMemoryPayloadStore(), 8)
	small, ref, err := store.externalizeJSON(t.Context(), []byte(`{"a":1}`), "null")
	if err != nil || ref.external() || small != `{"a":1}` {
		t.Fatalf("small: %q %+v %v", small, ref, err)
	}
	large, ref, err := store.externalizeJSON(t.Context(), []byte(`{"a":"0123456789"}`), "null")
	if err != nil || !ref.external() || large != "null" {
		t.Fatalf("large: %q %+v %v", large, ref, err)
	}
}

func TestInternalizeFailures(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 1)
	_, ref, err := store.externalize(t.Context(), []byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}

	mismatched := ref
	mismatched.size.Int64++
	if _, err = store.internalize(t.Context(), nil, mismatched); !errors.Is(err, ErrPayloadMismatch) {
		t.Fatalf("size mismatch: %v", err)
	}
	missing := ref
	missing.digest.String = "sha256:" + string(bytes.Repeat([]byte("0"), 64))
	if _, err = store.internalize(t.Context(), nil, missing); err == nil {
		t.Fatal("a missing object must fail")
	}
	store.SetPayloadStore(nil, 0)
	if _, err = store.internalize(t.Context(), nil, ref); !errors.Is(err, ErrPayloadStoreUnavailable) {
		t.Fatalf("no payload store: %v", err)
	}
	store.SetPayloadStore(&failingPayloadStore{}, 1)
	if _, _, err = store.externalize(t.Context(), []byte("0123456789")); err == nil {
		t.Fatal("a failing store must fail the write")
	}
}

func TestResolvePayloadsFetchesOnlyExternalisedSlotsWithinTheConcurrencyBound(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 1)
	const count = 40
	results := make([][]byte, count)
	slots := make([]payloadSlot, count)
	want := make([][]byte, count)
	for i := range slots {
		want[i] = []byte("payload-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		inline, ref, err := store.externalize(t.Context(), want[i])
		if err != nil || len(inline) != 0 {
			t.Fatalf("externalize %d: %q %v", i, inline, err)
		}
		slots[i] = payloadSlot{ref: ref, set: func(data []byte) { results[i] = data }}
	}
	// A slot whose payload is inline is never fetched and never set.
	inlineSet := false
	slots = append(slots, payloadSlot{set: func([]byte) { inlineSet = true }})

	var inFlight, peak atomic.Int32
	store.SetPayloadStore(&slowPayloadStore{memoryPayloadStore: payloads, inFlight: &inFlight, peak: &peak}, 1)
	if err := store.resolvePayloads(t.Context(), slots); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if !bytes.Equal(results[i], want[i]) {
			t.Fatalf("slot %d = %q, want %q", i, results[i], want[i])
		}
	}
	if inlineSet {
		t.Fatal("an inline slot must be skipped")
	}
	if got := peak.Load(); got > payloadFetchConcurrency || got < 2 {
		t.Fatalf("peak concurrent reads = %d, want 2..%d", got, payloadFetchConcurrency)
	}
}

// slowPayloadStore records how many reads run at once.
type slowPayloadStore struct {
	*memoryPayloadStore
	inFlight, peak *atomic.Int32
}

func (s *slowPayloadStore) Get(ctx context.Context, digest string) ([]byte, error) {
	now := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		peak := s.peak.Load()
		if now <= peak || s.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	return s.memoryPayloadStore.Get(ctx, digest)
}

func TestResolvePayloadsReportsAFailedRead(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 1)
	_, ref, err := store.externalize(t.Context(), []byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	payloads.objects = map[string][]byte{}
	if err := store.resolvePayloads(t.Context(), []payloadSlot{{ref: ref, set: func([]byte) {}}}); err == nil {
		t.Fatal("a missing object must fail the list")
	}
}

func TestInlineLimitIsClampedToWhatTheInlineColumnsAccept(t *testing.T) {
	store := &PostgresStore{}
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 64<<20)
	if _, ref, err := store.externalize(t.Context(), make([]byte, MaxInlineBytes)); err != nil || ref.external() {
		t.Fatalf("a payload at the limit stays inline: %+v %v", ref, err)
	}
	if _, ref, err := store.externalize(t.Context(), make([]byte, MaxInlineBytes+1)); err != nil || !ref.external() {
		t.Fatalf("a payload above the limit must be externalised whatever the setting: %+v %v", ref, err)
	}
}

func TestPayloadRefMatchesOnlyItsOwnBytes(t *testing.T) {
	store := &PostgresStore{}
	store.SetPayloadStore(newMemoryPayloadStore(), 1)
	_, ref, err := store.externalize(t.Context(), []byte("chart metadata"))
	if err != nil || !ref.external() {
		t.Fatalf("payload not externalised: %v", err)
	}
	if !ref.matches([]byte("chart metadata")) || ref.matches([]byte("chart metadatA")) || ref.matches([]byte("short")) || (payloadRef{}).matches([]byte("chart metadata")) {
		t.Fatal("a reference must match exactly the bytes it names")
	}
}

func TestPayloadsEqualWithoutReadingTheStore(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 1)
	externalRef := func(data string) payloadRef {
		_, ref, err := store.externalize(t.Context(), []byte(data))
		if err != nil || !ref.external() {
			t.Fatalf("payload not externalised: %v", err)
		}
		return ref
	}
	cases := []struct {
		name       string
		a, b       string
		aExt, bExt bool
		aRef, bRef string
		want       bool
	}{
		{name: "both inline equal", a: "same", b: "same", want: true},
		{name: "both inline differ", a: "one", b: "two"},
		{name: "external against equal inline", aExt: true, aRef: "same", b: "same", want: true},
		{name: "external against different inline", aExt: true, aRef: "same", b: "other"},
		{name: "external against empty inline", aExt: true, aRef: "same", b: ""},
		{name: "inline against equal external", a: "same", bExt: true, bRef: "same", want: true},
		{name: "inline against different external", a: "other", bExt: true, bRef: "same"},
		{name: "equal externals", aExt: true, aRef: "same", bExt: true, bRef: "same", want: true},
		{name: "different externals", aExt: true, aRef: "one", bExt: true, bRef: "two"},
	}
	for _, c := range cases {
		a, b := []byte(c.a), []byte(c.b)
		var aRef, bRef payloadRef
		if c.aExt {
			aRef, a = externalRef(c.aRef), nil
		}
		if c.bExt {
			bRef, b = externalRef(c.bRef), nil
		}
		if got := payloadsEqual(a, aRef, b, bRef); got != c.want {
			t.Errorf("%s: equal=%v want %v", c.name, got, c.want)
		}
	}
	if payloads.gets != 0 {
		t.Fatalf("comparison read the object store %d times", payloads.gets)
	}
}

func TestStagedPayloadStoresOnceAndFallsBackOnChange(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 4)
	large := []byte("a payload above the threshold")
	staged, err := store.stagePayload(t.Context(), large)
	if err != nil || !staged.ref.external() || payloads.puts != 1 {
		t.Fatalf("staged=%+v puts=%d err=%v", staged, payloads.puts, err)
	}
	inline, ref, err := staged.externalizeIn(t.Context(), store, nil, large)
	if err != nil || !ref.external() || len(inline) != 0 || payloads.puts != 1 {
		t.Fatalf("unchanged payload must reuse the staged object: puts=%d err=%v", payloads.puts, err)
	}
	if _, ref, err = staged.externalizeIn(t.Context(), store, nil, []byte("a different payload above it")); err != nil || !ref.external() || payloads.puts != 2 {
		t.Fatalf("changed payload must be stored now: puts=%d err=%v", payloads.puts, err)
	}
	if inline, ref, err = (stagedPayload{}).externalizeIn(t.Context(), store, nil, []byte("tiny")); err != nil || ref.external() || string(inline) != "tiny" {
		t.Fatalf("a small payload stays inline: %q %v", inline, err)
	}
	store.SetPayloadStore(&failingPayloadStore{}, 1)
	if _, err = store.stagePayload(t.Context(), large); err == nil {
		t.Fatal("a failed object write must fail the staging")
	}
}

func TestInternalizeWithPrefersThePrefetchedPayload(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 1)
	data := []byte("prefetched payload")
	_, ref, err := store.externalize(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	pre := payloadPrefetch{}
	pre.add(ref, data)
	pre.add(payloadRef{}, []byte("inline payloads are never prefetched"))
	if len(pre) != 1 {
		t.Fatalf("only an externalised payload is prefetched: %d", len(pre))
	}
	got, err := store.internalizeWith(t.Context(), nil, ref, pre)
	if err != nil || !bytes.Equal(got, data) || payloads.gets != 0 {
		t.Fatalf("the prefetched payload must be used: %q gets=%d err=%v", got, payloads.gets, err)
	}
	if got, err = store.internalizeWith(t.Context(), nil, ref, nil); err != nil || !bytes.Equal(got, data) || payloads.gets != 1 {
		t.Fatalf("a payload that was not prefetched is read: %q gets=%d err=%v", got, payloads.gets, err)
	}
	pre[ref.digest.String] = []byte("stale")
	if got, err = store.internalizeWith(t.Context(), nil, ref, pre); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("a prefetched payload of another size must not be trusted: %q %v", got, err)
	}
}

func TestStagedPayloadOnContextIsReusedByTheCommit(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 4)
	data := []byte("a chart.yaml above the inline threshold")
	ctx, err := store.stageOnContext(context.Background(), data)
	if err != nil || payloads.puts != 1 {
		t.Fatalf("stage: puts=%d err=%v", payloads.puts, err)
	}
	// The context a publication lock returns is derived from the staged one.
	type key struct{}
	derived := context.WithValue(ctx, key{}, "lock")
	staged, err := store.stagedFor(derived, data)
	if err != nil || !staged.ref.external() || payloads.puts != 1 {
		t.Fatalf("the commit must reuse the staged object: puts=%d ref=%v err=%v", payloads.puts, staged.ref, err)
	}
	if _, err = store.stagedFor(derived, []byte("other bytes above the threshold")); err != nil || payloads.puts != 2 {
		t.Fatalf("a changed payload is stored by the commit: puts=%d err=%v", payloads.puts, err)
	}
	if _, err = store.stagedFor(context.Background(), data); err != nil || payloads.puts != 3 {
		t.Fatalf("an unstaged context stores the payload: puts=%d err=%v", payloads.puts, err)
	}
}

func TestStageOnContextReportsAFailedPut(t *testing.T) {
	store := &PostgresStore{}
	store.SetPayloadStore(&failingPayloadStore{}, 1)
	if _, err := store.stageOnContext(context.Background(), []byte("large payload")); err == nil {
		t.Fatal("a failed put must fail the staging")
	}
}

func TestInternalizeStoredReadsTheStoreOnlyWhenAllowed(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 1)
	data := []byte("manifest above the threshold")
	_, ref, err := store.externalize(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.internalizeStored(context.Background(), nil, ref, nil, false); !errors.Is(err, errPayloadNotPrefetched) {
		t.Fatalf("a miss must restart: %v", err)
	}
	if got, err := store.internalizeStored(context.Background(), nil, ref, nil, true); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the last attempt reads the store: %q %v", got, err)
	}
}

func TestInternalizePrefetchedNeverReadsTheStore(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 1)
	data := []byte("manifest above the threshold")
	_, ref, err := store.externalize(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	pre := payloadPrefetch{}
	if got, err := store.internalizePrefetched([]byte("inline"), payloadRef{}, pre); err != nil || string(got) != "inline" {
		t.Fatalf("an inline payload is returned as is: %q %v", got, err)
	}
	if _, err := store.internalizePrefetched(nil, ref, pre); !errors.Is(err, errPayloadNotPrefetched) {
		t.Fatalf("a payload missing from the prefetch must ask for one: %v", err)
	}
	pre.add(ref, data)
	if got, err := store.internalizePrefetched(nil, ref, pre); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("prefetched payload: %q %v", got, err)
	}
	if payloads.gets != 0 {
		t.Fatalf("the object store was read %d times", payloads.gets)
	}
	store.SetPayloadStore(nil, 0)
	if _, err := store.internalizePrefetched(nil, ref, payloadPrefetch{}); !errors.Is(err, ErrPayloadStoreUnavailable) {
		t.Fatalf("without a payload store: %v", err)
	}
}

func TestSameStoredOCISnapshotNeedsThePriorPayloadOnlyWhenDigestsDiffer(t *testing.T) {
	payloads := newMemoryPayloadStore()
	store := &PostgresStore{}
	store.SetPayloadStore(payloads, 1)
	mk := func(owner string) discovery.Snapshot {
		return discovery.Snapshot{ID: "6f1c8a52-0000-4000-8000-000000000001", Owner: owner, Kind: "catalog"}
	}
	encode := func(s discovery.Snapshot) ([]byte, payloadRef) {
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		_, ref, err := store.externalize(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		return data, ref
	}
	snapshot := mk("alice")
	data, ref := encode(snapshot)
	// Equal digests: the same snapshot, decided without any payload.
	if same, err := store.sameStoredOCISnapshot(nil, ref, data, ref, snapshot, nil); err != nil || !same {
		t.Fatalf("equal digests: %v %v", same, err)
	}
	other := mk("bob")
	otherData, otherRef := encode(other)
	if _, err := store.sameStoredOCISnapshot(nil, ref, otherData, otherRef, other, nil); !errors.Is(err, errPayloadNotPrefetched) {
		t.Fatalf("different digests need the stored payload: %v", err)
	}
	pre := payloadPrefetch{}
	pre.add(ref, data)
	if same, err := store.sameStoredOCISnapshot(nil, ref, otherData, otherRef, other, pre); err != nil || same {
		t.Fatalf("a different snapshot must not match: %v %v", same, err)
	}
	// An inline prior is compared as a snapshot.
	if same, err := store.sameStoredOCISnapshot(data, payloadRef{}, data, ref, snapshot, nil); err != nil || !same {
		t.Fatalf("inline prior: %v %v", same, err)
	}
	if same, _ := store.sameStoredOCISnapshot(data, payloadRef{}, otherData, otherRef, other, nil); same {
		t.Fatal("an inline prior of another snapshot must not match")
	}
}
