//go:build integration

package objectstore_test

import (
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore/storetest"
)

// TestS3StoreContract runs the shared contract against the S3 driver and the
// service named by TEST_OBJECT_STORE_* (RustFS in make integration-test, and
// RustFS and MinIO in the object-store contract CI job). The checksum mode and
// addressing come from TEST_OBJECT_STORE_CHECKSUM_MODE and _PATH_STYLE.
func TestS3StoreContract(t *testing.T) {
	cfg, ok := storetest.ConfigFromEnv("TEST_OBJECT_STORE_")
	if !ok {
		t.Skip("TEST_OBJECT_STORE_ENDPOINT and credentials are required")
	}
	storetest.RunContract(t, storetest.S3Factory(cfg))
}

// TestS3StoreMetaContract runs the meta/sha256 namespace cases of ADR 0023 section 9
// against the same service.
func TestS3StoreMetaContract(t *testing.T) {
	cfg, ok := storetest.ConfigFromEnv("TEST_OBJECT_STORE_")
	if !ok {
		t.Skip("TEST_OBJECT_STORE_ENDPOINT and credentials are required")
	}
	storetest.RunMetaContract(t, storetest.S3Factory(cfg))
}
