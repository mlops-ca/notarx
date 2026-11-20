package storetest

import (
	"bytes"
	"errors"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// RunMetaContract exercises the meta/sha256/<hex> namespace (ADR 0023 section 9) over
// the driver a factory builds: payloads below and above the inline threshold
// round-trip, writes are idempotent, an absent digest is ErrNotFound and bytes that do
// not hash to their key are rejected on read.
func RunMetaContract(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*testing.T, objectstore.Store)
	}{
		{"RoundTripBelowAndAboveThreshold", metaRoundTrip},
		{"PutIsIdempotent", metaIdempotent},
		{"MissingDigest", metaMissing},
		{"DigestMismatchOnReadIsRejected", metaMismatch},
		{"InvalidDigest", metaInvalidDigest},
		{"DeleteRemovesObject", metaDelete},
		{"ListReturnsEveryMetaObject", metaList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := factory(t)
			if store == nil {
				t.Fatal("factory returned a nil store")
			}
			tc.run(t, store)
		})
	}
}

func metaRoundTrip(t *testing.T, store objectstore.Store) {
	for _, size := range []int{1, 16384, 16385, 1 << 20} {
		for _, cacheBytes := range []int64{0, 1} { // default LRU, and one too small to hold anything
			meta := objectstore.NewMetaStore(store, cacheBytes)
			payload := bytes.Repeat([]byte{byte(size % 251)}, size)
			digest, key, err := meta.Put(ctx(t), payload)
			if err != nil {
				t.Fatalf("size %d: put: %v", size, err)
			}
			if digest != Digest(payload) || key != objectstore.MetaPrefix+digest[len("sha256:"):] {
				t.Fatalf("size %d: digest %s key %s", size, digest, key)
			}
			got, err := meta.Get(ctx(t), digest)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("size %d: get = %d bytes, %v", size, len(got), err)
			}
			// A fresh store has an empty LRU, so this read goes to the driver.
			got, err = objectstore.NewMetaStore(store, cacheBytes).Get(ctx(t), digest)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("size %d: uncached get = %d bytes, %v", size, len(got), err)
			}
		}
	}
}

func metaIdempotent(t *testing.T, store objectstore.Store) {
	meta := objectstore.NewMetaStore(store, 0)
	first, _, err := meta.Put(ctx(t), []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := meta.Put(ctx(t), []byte("same"))
	if err != nil || first != second {
		t.Fatalf("second put = %q, %v; want %q", second, err, first)
	}
}

func metaMissing(t *testing.T, store objectstore.Store) {
	meta := objectstore.NewMetaStore(store, 0)
	if _, err := meta.Get(ctx(t), Digest([]byte("absent"))); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func metaMismatch(t *testing.T, store objectstore.Store) {
	digest := Digest([]byte("expected"))
	key, err := objectstore.MetaKey(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx(t), key, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	meta := objectstore.NewMetaStore(store, 0)
	if _, err := meta.Get(ctx(t), digest); !errors.Is(err, objectstore.ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}
	// A rejected object is not cached: a later read still fails.
	if _, err := meta.Get(ctx(t), digest); !errors.Is(err, objectstore.ErrDigestMismatch) {
		t.Fatalf("second read err = %v, want ErrDigestMismatch", err)
	}
}

func metaInvalidDigest(t *testing.T, store objectstore.Store) {
	meta := objectstore.NewMetaStore(store, 0)
	for _, digest := range []string{"", "sha256:zz", "md5:00", "sha256:" + string(bytes.Repeat([]byte("a"), 63))} {
		if _, err := meta.Get(ctx(t), digest); err == nil {
			t.Errorf("Get(%q) accepted an invalid digest", digest)
		}
		if err := meta.Delete(ctx(t), digest); err == nil {
			t.Errorf("Delete(%q) accepted an invalid digest", digest)
		}
	}
}

func metaDelete(t *testing.T, store objectstore.Store) {
	meta := objectstore.NewMetaStore(store, 0)
	digest, _, err := meta.Put(ctx(t), []byte("to delete"))
	if err != nil {
		t.Fatal(err)
	}
	if err := meta.Delete(ctx(t), digest); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.Get(ctx(t), digest); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after delete", err)
	}
}

func metaList(t *testing.T, store objectstore.Store) {
	meta := objectstore.NewMetaStore(store, 0)
	empty, err := meta.List(ctx(t))
	if err != nil || len(empty) != 0 {
		t.Fatalf("an empty namespace listed %v, %v", empty, err)
	}
	want := map[string]bool{}
	for _, payload := range []string{"first", "second", "third"} {
		digest, _, err := meta.Put(ctx(t), []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		want[digest] = true
	}
	// Objects outside the namespace, and a stray key under the prefix that is not a digest, are not listed.
	if err := store.Put(ctx(t), "data/other", []byte("other")); err != nil {
		t.Fatal(err)
	}
	// The filesystem driver refuses such a key, which is as good: it cannot be listed either.
	_ = store.Put(ctx(t), objectstore.MetaPrefix+"not-a-digest", []byte("stray"))
	got, err := meta.List(ctx(t))
	if err != nil || len(got) != len(want) {
		t.Fatalf("listed %v, %v; want %d digests", got, err, len(want))
	}
	for _, digest := range got {
		if !want[digest] {
			t.Fatalf("listed unexpected %q", digest)
		}
	}
	for digest := range want {
		if err := meta.Delete(ctx(t), digest); err != nil {
			t.Fatal(err)
		}
	}
	if got, err = meta.List(ctx(t)); err != nil || len(got) != 0 {
		t.Fatalf("after deleting everything listed %v, %v", got, err)
	}
}
