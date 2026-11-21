package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// A store without server-side copy streams the source; every store refuses a source whose recorded digest or size
// differs from the claim.
func TestCopyVerifiedChecksTheRecordedDigestAndSize(t *testing.T) {
	ctx := context.Background()
	data := []byte("staged bytes")
	digest := digestOf(data)
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "streaming": streamingOnly{NewMemoryStore()}} {
		t.Run(name, func(t *testing.T) {
			if err := store.PutVerifiedReader(ctx, "staging/a", bytes.NewReader(data), int64(len(data)), digest); err != nil {
				t.Fatal(err)
			}
			if err := CopyVerified(ctx, store, "staging/a", "final/a", digest, int64(len(data))); err != nil {
				t.Fatal(err)
			}
			info, err := store.Stat(ctx, "final/a")
			if err != nil || info.Digest != digest || info.Size != int64(len(data)) {
				t.Fatalf("copy = %+v err=%v", info, err)
			}
			if err = CopyVerified(ctx, store, "staging/a", "final/b", digestOf([]byte("other")), int64(len(data))); !errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("a claim that differs from the recorded digest = %v", err)
			}
			if err = CopyVerified(ctx, store, "staging/a", "final/b", digest, int64(len(data))+1); !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("a size that differs from the recorded size = %v", err)
			}
			if err = CopyVerified(ctx, store, "staging/a", "final/b", "sha256:nothex", int64(len(data))); err == nil {
				t.Fatal("an invalid digest must be refused")
			}
			if err = CopyVerified(ctx, store, "staging/missing", "final/b", digest, int64(len(data))); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing source = %v", err)
			}
			if _, err = store.Stat(ctx, "final/b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a refused copy wrote the destination: %v", err)
			}
			// A source whose bytes are not the claimed ones is refused, however it was written.
			if err = store.Put(ctx, "staging/unverified", []byte("tampered bytes")); err != nil {
				t.Fatal(err)
			}
			if err = CopyVerified(ctx, store, "staging/unverified", "final/c", digest, int64(len(data))); !errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("tampered source = %v", err)
			}
		})
	}
}

// streamingOnly hides the Copier of its store so CopyVerified takes the streaming path.
type streamingOnly struct{ Store }

func TestS3CopyVerifiedMovesNoBytesThroughTheNode(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3(t)
	store := fake.store(fake.config())
	data := bytes.Repeat([]byte("0123456789"), 1000)
	digest := digestOf(data)
	if err := store.PutVerifiedReader(ctx, "staging/a", bytes.NewReader(data), int64(len(data)), digest); err != nil {
		t.Fatal(err)
	}
	before := len(fake.calls)
	if err := CopyVerified(ctx, store, "staging/a", "final/a", digest, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.calls[before:], ",")
	if !strings.Contains(calls, "CopyObject final/a") || strings.Contains(calls, "GET ") || strings.Contains(calls, "PutObject") {
		t.Fatalf("calls = %s, want a server-side CopyObject and no download or upload", calls)
	}
	info, err := store.Stat(ctx, "final/a")
	if err != nil || info.Digest != digest || info.Size != int64(len(data)) {
		t.Fatalf("copy = %+v err=%v", info, err)
	}
	if err = CopyVerified(ctx, store, "staging/a", "final/b", digestOf([]byte("other")), int64(len(data))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mismatching claim = %v", err)
	}
	if err = CopyVerified(ctx, store, "staging/none", "final/b", digest, int64(len(data))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source = %v", err)
	}
	if fake.object("final/b") != nil {
		t.Fatal("a refused copy wrote the destination")
	}
}

func TestS3CopyOfALargeObjectUsesPartCopies(t *testing.T) {
	ctx := context.Background()
	limit, part := singleCopyLimit, copyPartSize
	singleCopyLimit, copyPartSize = 1000, 1000
	t.Cleanup(func() { singleCopyLimit, copyPartSize = limit, part })
	fake := newFakeS3(t)
	store := fake.store(fake.config())
	data := bytes.Repeat([]byte("abcdefghij"), 250)
	digest := digestOf(data)
	if err := store.PutVerifiedReader(ctx, "staging/big", bytes.NewReader(data), int64(len(data)), digest); err != nil {
		t.Fatal(err)
	}
	before := len(fake.calls)
	if err := CopyVerified(ctx, store, "staging/big", "final/big", digest, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.calls[before:], ",")
	if strings.Count(calls, "UploadPartCopy final/big") != 3 || strings.Contains(calls, "GET ") {
		t.Fatalf("calls = %s, want 3 server-side part copies", calls)
	}
	reader, _, err := store.Open(ctx, "final/big")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("the part copies did not reassemble the object")
	}
	if info, statErr := store.Stat(ctx, "final/big"); statErr != nil || info.Digest != digest {
		t.Fatalf("digest metadata lost: %+v %v", info, statErr)
	}
	// A failing part aborts the upload and writes nothing.
	fake.failPart = 2
	if err = store.CopyObject(ctx, "staging/big", "final/broken"); err == nil {
		t.Fatal("a failing part copy must fail the copy")
	}
	if len(fake.uploads) != 0 || fake.object("final/broken") != nil {
		t.Fatalf("a failed copy left uploads=%d object=%v", len(fake.uploads), fake.object("final/broken"))
	}
}

// A service that does not implement CopyObject, or refuses it, leaves the copy to the streaming path.
func TestS3CopyVerifiedStreamsWhenTheServiceRefusesTheCopy(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3(t)
	store := fake.store(fake.config())
	data := []byte("refused copy")
	digest := digestOf(data)
	if err := store.PutVerifiedReader(ctx, "staging/a", bytes.NewReader(data), int64(len(data)), digest); err != nil {
		t.Fatal(err)
	}
	fake.rejectCopy = true
	if err := CopyVerified(ctx, store, "staging/a", "final/a", digest, int64(len(data))); err != nil {
		t.Fatalf("a refused CopyObject must fall back to streaming: %v", err)
	}
	if info, err := store.Stat(ctx, "final/a"); err != nil || info.Digest != digest {
		t.Fatalf("copy = %+v %v", info, err)
	}
}

// '+' in a key is escaped in the copy source, which the service would otherwise read as a space.
func TestS3CopySourceEscapesPlus(t *testing.T) {
	if got := copySource("bucket", "a+b/c d"); got != "bucket%2Fa%2Bb%2Fc%20d" {
		t.Fatalf("copySource = %q", got)
	}
	ctx := context.Background()
	fake := newFakeS3(t)
	store := fake.store(fake.config())
	data := []byte("plus")
	if err := store.PutVerifiedReader(ctx, "staging/a+b", bytes.NewReader(data), int64(len(data)), digestOf(data)); err != nil {
		t.Fatal(err)
	}
	if err := CopyVerified(ctx, store, "staging/a+b", "final/a+b", digestOf(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if len(fake.copySources) != 1 || !strings.Contains(fake.copySources[0], "%2B") {
		t.Fatalf("copy sources = %v", fake.copySources)
	}
}
