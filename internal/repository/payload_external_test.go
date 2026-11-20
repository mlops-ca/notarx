package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
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
