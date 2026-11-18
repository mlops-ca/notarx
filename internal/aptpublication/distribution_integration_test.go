//go:build integration

package aptpublication

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func TestDistributionPostgresRustFS(t *testing.T) {
	databaseURL, endpoint := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_OBJECT_STORE_ENDPOINT")
	if databaseURL == "" || endpoint == "" {
		t.Skip("isolated PostgreSQL and RustFS required")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	objects, err := objectstore.NewS3StoreWithCredentials(endpoint, os.Getenv("TEST_OBJECT_STORE_ACCESS_KEY"), os.Getenv("TEST_OBJECT_STORE_SECRET_KEY"), "apt-distribution-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:20])
	if err != nil {
		t.Fatal(err)
	}
	if err = objects.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	exerciseAPTDistribution(t, store, objects)
}
