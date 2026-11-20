//go:build integration

package repository_test

import (
	"os"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestRawVersionPostgresStoreContract(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL contract tests")
	}
	runRawVersionStoreContract(t, func(t *testing.T) rawVersionContractStore {
		t.Helper()
		store, err := repository.NewPostgresStore(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		})
		return store
	})
}
