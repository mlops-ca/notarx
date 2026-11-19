package objectstore_test

import (
	"path/filepath"
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
