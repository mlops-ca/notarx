package objectstore_test

import (
	"path/filepath"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/objectstore/storetest"
)

func TestFileStoreContract(t *testing.T) {
	storetest.RunContract(t, func(t *testing.T) objectstore.Store {
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
