// Package storetest holds the behavioural contract every objectstore.Store
// driver must satisfy (ADR 0017). Each driver's tests call RunContract with a
// factory that returns a fresh, empty, isolated store.
package storetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// Factory returns an empty Store that no other test shares. It may register
// cleanup with t.Cleanup.
type Factory func(t *testing.T) objectstore.Store

// listPageSize is the page size of the S3 listing API; the listing case writes
// more keys than this so pagination is exercised.
const listPageSize = 1000

// Digest returns the "sha256:<hex>" digest of data.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RunContract runs every contract case against stores built by factory.
func RunContract(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*testing.T, objectstore.Store)
	}{
		{"PutGetRoundTrip", putGetRoundTrip},
		{"EmptyObject", emptyObject},
		{"BinaryContent", binaryContent},
		{"LargeObject", largeObject},
		{"PutOverwritesExistingObject", putOverwrites},
		{"SizeMismatch", sizeMismatch},
		{"DigestMismatchIsRejected", digestMismatch},
		{"InvalidDigestIsRejected", invalidDigest},
		{"PutVerifiedReaderAndStat", putVerifiedAndStat},
		{"StatReportsSize", statReportsSize},
		{"SetVerifiedDigest", setVerifiedDigest},
		{"OpenReturnsSize", openReturnsSize},
		{"OpenRangeBounds", openRangeBounds},
		{"OpenRangeErrors", openRangeErrors},
		{"ListPrefixSemantics", listPrefix},
		{"ListOrderingPastOnePage", listOrderingPastOnePage},
		{"DeleteIsIdempotent", deleteIdempotent},
		{"NotFoundMapsToErrNotFound", notFound},
		{"UnusualKeys", unusualKeys},
		{"ConcurrentPutOfOneKey", concurrentPut},
		{"ContextCancellation", contextCancellation},
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

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return c
}

func mustPut(t *testing.T, store objectstore.Store, key string, data []byte) {
	t.Helper()
	if err := store.Put(ctx(t), key, data); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func mustGet(t *testing.T, store objectstore.Store, key string) []byte {
	t.Helper()
	data, err := store.Get(ctx(t), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return data
}

func readAll(t *testing.T, reader io.ReadCloser) []byte {
	t.Helper()
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read: %v close: %v", err, closeErr)
	}
	return data
}

// nonSeekable hides Seek so drivers must handle a plain stream.
type nonSeekable struct{ io.Reader }

func putGetRoundTrip(t *testing.T, store objectstore.Store) {
	want := []byte("notarx contract: verified bytes")
	mustPut(t, store, "contract/round-trip", want)
	if got := mustGet(t, store, "contract/round-trip"); !bytes.Equal(got, want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}
	if err := store.PutReader(ctx(t), "contract/round-trip-reader", nonSeekable{bytes.NewReader(want)}, int64(len(want))); err != nil {
		t.Fatalf("PutReader: %v", err)
	}
	if got := mustGet(t, store, "contract/round-trip-reader"); !bytes.Equal(got, want) {
		t.Fatalf("Get after PutReader = %q, want %q", got, want)
	}
}

func emptyObject(t *testing.T, store objectstore.Store) {
	mustPut(t, store, "contract/empty", nil)
	if got := mustGet(t, store, "contract/empty"); len(got) != 0 {
		t.Fatalf("Get = %q, want empty", got)
	}
	info, err := store.Stat(ctx(t), "contract/empty")
	if err != nil || info.Size != 0 {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	if err = store.PutVerifiedReader(ctx(t), "contract/empty-verified", bytes.NewReader(nil), 0, Digest(nil)); err != nil {
		t.Fatalf("PutVerifiedReader(empty): %v", err)
	}
	reader, size, err := store.Open(ctx(t), "contract/empty-verified")
	if err != nil || size != 0 || len(readAll(t, reader)) != 0 {
		t.Fatalf("Open(empty) size=%d err=%v", size, err)
	}
}

func binaryContent(t *testing.T, store objectstore.Store) {
	want := make([]byte, 0, 4096)
	for i := range 4096 {
		want = append(want, byte(i*31))
	}
	want = append(want, 0, 0xff, '\r', '\n', 0)
	mustPut(t, store, "contract/binary", want)
	if got := mustGet(t, store, "contract/binary"); !bytes.Equal(got, want) {
		t.Fatalf("binary content differs (%d bytes, want %d)", len(got), len(want))
	}
}

// largeObject is big enough to cross a lowered multipart threshold (the S3
// integration runs set one) and to need several buffers on every driver.
func largeObject(t *testing.T, store objectstore.Store) {
	want := make([]byte, 12<<20+321)
	state := uint32(1)
	for i := range want {
		state = state*1664525 + 1013904223
		want[i] = byte(state >> 24)
	}
	digest := Digest(want)
	if err := store.PutVerifiedReader(ctx(t), "contract/large", bytes.NewReader(want), int64(len(want)), digest); err != nil {
		t.Fatalf("PutVerifiedReader(large): %v", err)
	}
	info, err := store.Stat(ctx(t), "contract/large")
	if err != nil || info.Size != int64(len(want)) || info.Digest != digest {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	reader, size, err := store.OpenRange(ctx(t), "contract/large", 5<<20-7, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, reader); size != int64(len(want)) || !bytes.Equal(got, want[5<<20-7:5<<20-7+4096]) {
		t.Fatalf("range across a part boundary differs (size %d)", size)
	}
	if got := mustGet(t, store, "contract/large"); !bytes.Equal(got, want) {
		t.Fatal("large object differs after the round trip")
	}
}

func putOverwrites(t *testing.T, store objectstore.Store) {
	mustPut(t, store, "contract/overwrite", []byte("first version"))
	mustPut(t, store, "contract/overwrite", []byte("2nd"))
	if got := mustGet(t, store, "contract/overwrite"); string(got) != "2nd" {
		t.Fatalf("Get = %q, want the second version", got)
	}
}

func sizeMismatch(t *testing.T, store objectstore.Store) {
	data := []byte("ten bytes!")
	digest := Digest(data)
	for name, size := range map[string]int64{"declared too large": int64(len(data)) + 5, "declared too small": int64(len(data)) - 4} {
		key := "contract/size-mismatch"
		if err := store.PutReader(ctx(t), key, nonSeekable{bytes.NewReader(data)}, size); err == nil {
			t.Errorf("%s: PutReader accepted a size mismatch", name)
		}
		if err := store.PutVerifiedReader(ctx(t), key, nonSeekable{bytes.NewReader(data)}, size, digest); err == nil {
			t.Errorf("%s: PutVerifiedReader accepted a size mismatch", name)
		}
		if err := store.PutVerifiedReader(ctx(t), key, bytes.NewReader(data), size, digest); err == nil {
			t.Errorf("%s: PutVerifiedReader accepted a size mismatch on a seekable reader", name)
		}
		if _, err := store.Get(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("%s: a rejected write left an object behind: %v", name, err)
		}
	}
	if err := store.PutReader(ctx(t), "contract/negative-size", bytes.NewReader(nil), -1); err == nil {
		t.Error("PutReader accepted a negative size")
	}
}

func digestMismatch(t *testing.T, store objectstore.Store) {
	data := []byte("the real bytes")
	other := Digest([]byte("different bytes"))
	key := "contract/digest-mismatch"
	if err := store.PutVerifiedReader(ctx(t), key, bytes.NewReader(data), int64(len(data)), other); err == nil {
		t.Fatal("PutVerifiedReader accepted a digest that does not match the bytes")
	}
	if _, err := store.Get(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("a rejected write left an object behind: %v", err)
	}
	// A rejected overwrite must leave the previous object intact.
	original := []byte("original")
	if err := store.PutVerifiedReader(ctx(t), key, bytes.NewReader(original), int64(len(original)), Digest(original)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutVerifiedReader(ctx(t), key, nonSeekable{bytes.NewReader(data)}, int64(len(data)), other); err == nil {
		t.Fatal("PutVerifiedReader accepted a mismatching overwrite")
	}
	if got := mustGet(t, store, key); !bytes.Equal(got, original) {
		t.Fatalf("object after rejected overwrite = %q, want %q", got, original)
	}
}

func invalidDigest(t *testing.T, store objectstore.Store) {
	data := []byte("bytes")
	for _, digest := range []string{"", "sha256:", "sha256:zz", "md5:" + strings.Repeat("0", 32), strings.TrimPrefix(Digest(data), "sha256:")} {
		if err := store.PutVerifiedReader(ctx(t), "contract/invalid-digest", bytes.NewReader(data), int64(len(data)), digest); err == nil {
			t.Errorf("PutVerifiedReader accepted malformed digest %q", digest)
		}
	}
	// SetVerifiedDigest refuses them too and leaves the object as it was.
	mustPut(t, store, "contract/invalid-digest-target", data)
	for _, digest := range []string{"", "abc", "sha256:zz", "sha256:" + strings.Repeat("a", 63), strings.TrimPrefix(Digest(data), "sha256:")} {
		if err := store.SetVerifiedDigest(ctx(t), "contract/invalid-digest-target", digest); err == nil {
			t.Errorf("SetVerifiedDigest accepted malformed digest %q", digest)
		}
	}
	if info, err := store.Stat(ctx(t), "contract/invalid-digest-target"); err != nil || (info.Digest != "" && info.Digest != Digest(data)) {
		t.Errorf("a refused digest changed the object: %#v %v", info, err)
	}
}

func putVerifiedAndStat(t *testing.T, store objectstore.Store) {
	data := []byte("verified payload")
	digest := Digest(data)
	if err := store.PutVerifiedReader(ctx(t), "contract/verified", nonSeekable{bytes.NewReader(data)}, int64(len(data)), digest); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx(t), "contract/verified")
	if err != nil || info.Size != int64(len(data)) || info.Digest != digest {
		t.Fatalf("Stat = %#v, %v; want size %d digest %s", info, err, len(data), digest)
	}
	if got := mustGet(t, store, "contract/verified"); !bytes.Equal(got, data) {
		t.Fatalf("Get = %q", got)
	}
}

func statReportsSize(t *testing.T, store objectstore.Store) {
	data := []byte("plain object")
	mustPut(t, store, "contract/plain", data)
	info, err := store.Stat(ctx(t), "contract/plain")
	if err != nil || info.Size != int64(len(data)) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	// A plain write carries no verified digest; a driver may report none or
	// the digest of the bytes, but never a different one.
	if info.Digest != "" && info.Digest != Digest(data) {
		t.Fatalf("Stat digest = %q for an unverified object", info.Digest)
	}
}

func setVerifiedDigest(t *testing.T, store objectstore.Store) {
	data := []byte("object whose digest is attached later")
	digest := Digest(data)
	mustPut(t, store, "contract/late-digest", data)
	if err := store.SetVerifiedDigest(ctx(t), "contract/late-digest", digest); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx(t), "contract/late-digest")
	if err != nil || info.Digest != digest || info.Size != int64(len(data)) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	if got := mustGet(t, store, "contract/late-digest"); !bytes.Equal(got, data) {
		t.Fatalf("bytes changed by SetVerifiedDigest: %q", got)
	}
	// Setting it again is harmless.
	if err = store.SetVerifiedDigest(ctx(t), "contract/late-digest", digest); err != nil {
		t.Fatalf("second SetVerifiedDigest: %v", err)
	}
	if err = store.SetVerifiedDigest(ctx(t), "contract/never-written", digest); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("SetVerifiedDigest on a missing key = %v, want ErrNotFound", err)
	}
	if _, err = store.Get(ctx(t), "contract/never-written"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("SetVerifiedDigest created a missing object: %v", err)
	}
}

func openReturnsSize(t *testing.T, store objectstore.Store) {
	data := []byte("open me")
	mustPut(t, store, "contract/open", data)
	reader, size, err := store.Open(ctx(t), "contract/open")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, reader); size != int64(len(data)) || !bytes.Equal(got, data) {
		t.Fatalf("Open size=%d body=%q", size, got)
	}
}

func openRangeBounds(t *testing.T, store objectstore.Store) {
	data := []byte("0123456789")
	mustPut(t, store, "contract/range", data)
	cases := []struct {
		offset, length int64
		want           string
	}{
		{0, 10, "0123456789"},
		{0, 1, "0"},
		{3, 4, "3456"},
		{9, 1, "9"},
		{10, 0, ""},
		{4, 0, ""},
		{0, 0, ""},
	}
	for _, tc := range cases {
		reader, size, err := store.OpenRange(ctx(t), "contract/range", tc.offset, tc.length)
		if err != nil {
			t.Fatalf("OpenRange(%d,%d): %v", tc.offset, tc.length, err)
		}
		if got := readAll(t, reader); size != int64(len(data)) || string(got) != tc.want {
			t.Fatalf("OpenRange(%d,%d) size=%d body=%q, want %q", tc.offset, tc.length, size, got, tc.want)
		}
	}
}

func openRangeErrors(t *testing.T, store objectstore.Store) {
	mustPut(t, store, "contract/range-errors", []byte("0123456789"))
	for _, tc := range [][2]int64{{-1, 1}, {0, -1}, {11, 0}, {0, 11}, {5, 6}, {10, 1}} {
		reader, _, err := store.OpenRange(ctx(t), "contract/range-errors", tc[0], tc[1])
		if err == nil {
			_ = reader.Close()
			t.Errorf("OpenRange(%d,%d) succeeded, want an error", tc[0], tc[1])
		}
	}
	if _, _, err := store.OpenRange(ctx(t), "contract/range-missing", 0, 0); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("OpenRange on a missing key = %v, want ErrNotFound", err)
	}
}

func listPrefix(t *testing.T, store objectstore.Store) {
	// No key is also the directory part of another: file-backed services
	// (file-backed S3 services, the filesystem driver) cannot hold both "a" and "a/1".
	keys := []string{"a/1", "a/2", "a/sub/3", "ab/4", "b/5"}
	for _, key := range keys {
		mustPut(t, store, key, []byte(key))
	}
	cases := map[string][]string{
		"":       {"a/1", "a/2", "a/sub/3", "ab/4", "b/5"},
		"a":      {"a/1", "a/2", "a/sub/3", "ab/4"},
		"a/":     {"a/1", "a/2", "a/sub/3"},
		"a/sub/": {"a/sub/3"},
		"b/5":    {"b/5"},
		"c":      {},
		"a/s":    {"a/sub/3"},
	}
	for prefix, want := range cases {
		got, err := store.List(ctx(t), prefix)
		if err != nil {
			t.Fatalf("List(%q): %v", prefix, err)
		}
		sort.Strings(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("List(%q) = %v, want %v", prefix, got, want)
		}
	}
}

func listOrderingPastOnePage(t *testing.T, store objectstore.Store) {
	const total = listPageSize + 137
	want := make([]string, total)
	var group sync.WaitGroup
	sem := make(chan struct{}, 16)
	errs := make(chan error, total)
	for i := range total {
		want[i] = fmt.Sprintf("contract/list/%05d", i)
		group.Add(1)
		sem <- struct{}{}
		go func() {
			defer group.Done()
			defer func() { <-sem }()
			if err := store.Put(ctx(t), want[i], []byte{byte(i)}); err != nil {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.List(ctx(t), "contract/list/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("List returned %d keys, want %d", len(got), total)
	}
	// Listings are lexicographic: the backup walker requires ordered keys.
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func deleteIdempotent(t *testing.T, store objectstore.Store) {
	mustPut(t, store, "contract/delete", []byte("x"))
	data := []byte("y")
	if err := store.PutVerifiedReader(ctx(t), "contract/delete-verified", bytes.NewReader(data), 1, Digest(data)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"contract/delete", "contract/delete-verified"} {
		for attempt := range 3 {
			if err := store.Delete(ctx(t), key); err != nil {
				t.Fatalf("Delete(%q) attempt %d: %v", key, attempt, err)
			}
		}
		if _, err := store.Get(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Get after Delete(%q) = %v, want ErrNotFound", key, err)
		}
		if _, err := store.Stat(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Stat after Delete(%q) = %v, want ErrNotFound", key, err)
		}
	}
	if err := store.Delete(ctx(t), "contract/never-existed"); err != nil {
		t.Fatalf("Delete of a missing key: %v", err)
	}
	// A deleted key can be written again and carries no stale digest.
	mustPut(t, store, "contract/delete-verified", []byte("again"))
	info, err := store.Stat(ctx(t), "contract/delete-verified")
	if err != nil || (info.Digest != "" && info.Digest != Digest([]byte("again"))) {
		t.Fatalf("Stat after rewrite = %#v, %v", info, err)
	}
}

func notFound(t *testing.T, store objectstore.Store) {
	const key = "contract/missing/object"
	if _, err := store.Get(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
	if _, err := store.Stat(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("Stat = %v, want ErrNotFound", err)
	}
	if reader, _, err := store.Open(ctx(t), key); !errors.Is(err, objectstore.ErrNotFound) {
		if err == nil {
			_ = reader.Close()
		}
		t.Errorf("Open = %v, want ErrNotFound", err)
	}
	if _, _, err := store.OpenRange(ctx(t), key, 0, 0); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("OpenRange = %v, want ErrNotFound", err)
	}
	if err := store.SetVerifiedDigest(ctx(t), key, Digest(nil)); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("SetVerifiedDigest = %v, want ErrNotFound", err)
	}
	keys, err := store.List(ctx(t), "contract/missing/")
	if err != nil || len(keys) != 0 {
		t.Errorf("List of an empty prefix = %v, %v", keys, err)
	}
}

func unusualKeys(t *testing.T, store objectstore.Store) {
	keys := []string{
		"unusual/with space/and  double",
		"unusual/ünï-cödé/日本",
		"unusual/percent%41/plus+sign/equals=amp&at@tilde~",
		"unusual/semi;colon,comma!bang'quote(paren)*star",
		"unusual/dots.in.name/trailing.dot.",
		// Several long segments, each under the 255-byte name limit of
		// file-backed services, for a key of about 400 bytes.
		"unusual/" + strings.Repeat("s", 120) + "/" + strings.Repeat("t", 120) + "/" + strings.Repeat("u", 120) + "/end",
		"unusual/UPPER/lower/MiXeD",
	}
	for i, key := range keys {
		data := []byte(fmt.Sprintf("payload %d for %s", i, key))
		if err := store.PutVerifiedReader(ctx(t), key, bytes.NewReader(data), int64(len(data)), Digest(data)); err != nil {
			t.Fatalf("PutVerifiedReader(%q): %v", key, err)
		}
		if got := mustGet(t, store, key); !bytes.Equal(got, data) {
			t.Fatalf("Get(%q) = %q, want %q", key, got, data)
		}
		// The digest rewrite path escapes the key separately from Put.
		if err := store.SetVerifiedDigest(ctx(t), key, Digest(data)); err != nil {
			t.Fatalf("SetVerifiedDigest(%q): %v", key, err)
		}
		if info, err := store.Stat(ctx(t), key); err != nil || info.Digest != Digest(data) {
			t.Fatalf("Stat(%q) = %#v, %v", key, info, err)
		}
	}
	got, err := store.List(ctx(t), "unusual/")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("List(unusual/) = %v, want %v", got, want)
	}
	for _, key := range keys {
		if err = store.Delete(ctx(t), key); err != nil {
			t.Fatalf("Delete(%q): %v", key, err)
		}
	}
}

func concurrentPut(t *testing.T, store objectstore.Store) {
	const writers = 12
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 1024*(i+1))
	}
	var group sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})
	for _, payload := range payloads {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs <- store.PutVerifiedReader(ctx(t), "contract/concurrent", bytes.NewReader(payload), int64(len(payload)), Digest(payload))
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}
	// Last writer wins, and the result is exactly one writer's bytes.
	got := mustGet(t, store, "contract/concurrent")
	matched := false
	for _, payload := range payloads {
		matched = matched || bytes.Equal(got, payload)
	}
	if !matched {
		t.Fatalf("object holds %d bytes that match no writer", len(got))
	}
	info, err := store.Stat(ctx(t), "contract/concurrent")
	if err != nil || info.Digest != Digest(got) || info.Size != int64(len(got)) {
		t.Fatalf("Stat = %#v, %v; digest must describe the surviving bytes", info, err)
	}
}

func contextCancellation(t *testing.T, store objectstore.Store) {
	data := []byte("cancellation")
	mustPut(t, store, "contract/cancel", data)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	checks := map[string]func() error{
		"Get":    func() error { _, err := store.Get(cancelled, "contract/cancel"); return err },
		"Put":    func() error { return store.Put(cancelled, "contract/cancel-new", data) },
		"Stat":   func() error { _, err := store.Stat(cancelled, "contract/cancel"); return err },
		"List":   func() error { _, err := store.List(cancelled, "contract/"); return err },
		"Delete": func() error { return store.Delete(cancelled, "contract/cancel") },
		"Open": func() error {
			reader, _, err := store.Open(cancelled, "contract/cancel")
			if reader != nil {
				_ = reader.Close()
			}
			return err
		},
		"PutVerifiedReader": func() error {
			return store.PutVerifiedReader(cancelled, "contract/cancel-new", bytes.NewReader(data), int64(len(data)), Digest(data))
		},
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a cancelled context = %v, want context.Canceled", name, err)
		}
	}
	if got := mustGet(t, store, "contract/cancel"); !bytes.Equal(got, data) {
		t.Fatalf("a cancelled Delete changed the object: %q", got)
	}
	if _, err := store.Get(ctx(t), "contract/cancel-new"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("a cancelled write left an object behind: %v", err)
	}
}
