package objectstore_test

import (
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/objectstore/storetest"
)

func TestMemoryStoreContract(t *testing.T) {
	storetest.RunContract(t, func(*testing.T) objectstore.Store { return objectstore.NewMemoryStore() })
}
