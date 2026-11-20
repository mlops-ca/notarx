package objectstore_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/objectstore/storetest"
)

func TestMemoryStoreMetaContract(t *testing.T) {
	storetest.RunMetaContract(t, func(*testing.T) objectstore.Store { return objectstore.NewMemoryStore() })
}

func TestFileStoreMetaContract(t *testing.T) {
	storetest.RunMetaContract(t, func(t *testing.T) objectstore.Store {
		cfg := objectstore.Defaults()
		cfg.Driver = objectstore.DriverFilesystem
		cfg.FSRoot = filepath.Join(t.TempDir(), "objects")
		cfg.FSReserveBytes = 0
		store, err := objectstore.NewFileStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

func TestMetaStoreEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := t.Context()
	store := objectstore.NewMemoryStore()
	meta := objectstore.NewMetaStore(store, 10)
	a, _, _ := meta.Put(ctx, []byte("aaaaaa"))
	b, _, _ := meta.Put(ctx, []byte("bbbbbb")) // evicts a: 12 bytes exceed the bound of 10
	// Remove both objects behind the cache: only a cached entry can still be read.
	for _, digest := range []string{a, b} {
		key, _ := objectstore.MetaKey(digest)
		_ = store.Delete(ctx, key)
	}
	if _, err := meta.Get(ctx, a); err == nil {
		t.Error("evicted entry was served from the cache")
	}
	if got, err := meta.Get(ctx, b); err != nil || string(got) != "bbbbbb" {
		t.Errorf("recent entry = %q, %v", got, err)
	}
	// A payload larger than the whole bound is never cached.
	big, _, _ := meta.Put(ctx, []byte("0123456789abcdef"))
	key, _ := objectstore.MetaKey(big)
	_ = store.Delete(ctx, key)
	if _, err := meta.Get(ctx, big); err == nil {
		t.Error("oversized payload was cached")
	}
}

func TestMetaStoreCachedBytesAreCopies(t *testing.T) {
	ctx := t.Context()
	meta := objectstore.NewMetaStore(objectstore.NewMemoryStore(), 0)
	digest, _, err := meta.Put(ctx, []byte("immutable"))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := meta.Get(ctx, digest)
	first[0] = 'X'
	second, err := meta.Get(ctx, digest)
	if err != nil || string(second) != "immutable" {
		t.Fatalf("cache returned mutated bytes %q, %v", second, err)
	}
}

func TestMetaStoreNormalizesTheDigestSpelling(t *testing.T) {
	ctx := t.Context()
	store := objectstore.NewMemoryStore()
	meta := objectstore.NewMetaStore(store, 0)
	digest, _, err := meta.Put(ctx, []byte("spelling"))
	if err != nil {
		t.Fatal(err)
	}
	upper := "sha256:" + strings.ToUpper(strings.TrimPrefix(digest, "sha256:"))
	if got, err := meta.Get(ctx, upper); err != nil || string(got) != "spelling" {
		t.Fatalf("Get with an uppercase digest = %q, %v", got, err)
	}
	if err := meta.Delete(ctx, upper); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.Get(ctx, digest); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("after deleting by the uppercase spelling, Get = %v, want ErrNotFound (a stale cache entry survived)", err)
	}
}

// slowGetStore holds every Get after it has read the object until release is closed, so a test
// can interleave a Delete with a read that is already in flight.
type slowGetStore struct {
	objectstore.Store
	read    chan struct{}
	release chan struct{}
}

func (s *slowGetStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.Store.Get(ctx, key)
	if err == nil {
		s.read <- struct{}{}
		<-s.release
	}
	return data, err
}

func TestMetaStoreDeleteIsNotUndoneByAReadInFlight(t *testing.T) {
	ctx := t.Context()
	inner := objectstore.NewMemoryStore()
	digest, _, err := objectstore.NewMetaStore(inner, 0).Put(ctx, []byte("swept"))
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowGetStore{Store: inner, read: make(chan struct{}), release: make(chan struct{})}
	meta := objectstore.NewMetaStore(slow, 0)
	done := make(chan error, 1)
	go func() {
		_, err := meta.Get(ctx, digest)
		done <- err
	}()
	<-slow.read // the read has the bytes and has not cached them yet
	if err := meta.Delete(ctx, digest); err != nil {
		t.Fatal(err)
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatalf("the in-flight read failed: %v", err)
	}
	if _, err := meta.Get(ctx, digest); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound: the racing read re-cached the object", err)
	}
}

func TestIsMetaObjectKey(t *testing.T) {
	hex := strings.Repeat("a", 64)
	for key, want := range map[string]bool{
		"meta/sha256/" + hex:                     true,
		"meta/sha256/" + strings.Repeat("A", 64): false,
		"meta/sha256/" + hex + "/x":              false,
		"meta/sha256/" + hex[:63]:                false,
		"meta/sha256/" + strings.Repeat("g", 64): false,
		"data/sha256/" + hex:                     false,
		"":                                       false,
	} {
		if got := objectstore.IsMetaObjectKey(key); got != want {
			t.Errorf("IsMetaObjectKey(%q) = %v, want %v", key, got, want)
		}
	}
}
