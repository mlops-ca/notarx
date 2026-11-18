//go:build ocilive

package objectstore_test

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/objectstore/storetest"
)

// These tests run against Oracle Cloud Infrastructure Object Storage through its
// S3 compatibility API (ADR 0017, slice 5). They are gating: the result decides
// whether CopyObject with metadata REPLACE and multipart upload work on the
// compat API. A REPLACE failure selects the streaming fallback and is reported;
// a failure that the fallback cannot cover (multipart, or the fallback itself)
// opens a follow-up ADR for a native driver.
//
// Configuration comes from the OCI_OBJECT_STORE_* variables, which the live CI
// job reads from repository secrets: ENDPOINT
// (https://<namespace>.compat.objectstorage.<region>.oci.customer-oci.com),
// REGION, BUCKET (pre-created; the tests never create or delete it),
// ACCESS_KEY and SECRET_KEY (a Customer Secret Key pair). Every case writes under
// a throw-away random prefix and removes what it wrote.

var checksumModes = []string{objectstore.ChecksumWhenRequired, objectstore.ChecksumWhenSupported}

func ociConfig(t *testing.T, mode string) objectstore.Config {
	t.Helper()
	cfg, ok := storetest.ConfigFromEnv("OCI_OBJECT_STORE_")
	if !ok || os.Getenv("OCI_OBJECT_STORE_BUCKET") == "" {
		t.Skip("OCI_OBJECT_STORE_ENDPOINT, _BUCKET, _ACCESS_KEY and _SECRET_KEY are required")
	}
	cfg.AutoCreateBucket = false
	cfg.PathStyle = true
	cfg.ChecksumMode = mode
	if os.Getenv("OCI_OBJECT_STORE_REGION") == "" {
		t.Fatal("OCI_OBJECT_STORE_REGION must name the tenancy region: the compat API verifies the SigV4 region")
	}
	return cfg
}

// TestOCIContract runs the shared contract on OCI in both checksum modes. Its
// unusual-key case includes keys with slashes through CopyObject, and every
// single-part write in when_supported mode is an aws-chunked upload.
func TestOCIContract(t *testing.T) {
	for _, mode := range checksumModes {
		t.Run(mode, func(t *testing.T) {
			storetest.RunContract(t, storetest.S3Factory(ociConfig(t, mode)))
		})
	}
}

// TestOCIDigestMode records which way SetVerifiedDigest works on OCI and proves
// that both ways attach a digest to keys that contain slashes.
func TestOCIDigestMode(t *testing.T) {
	for _, mode := range checksumModes {
		t.Run(mode, func(t *testing.T) {
			opened, err := objectstore.Open(context.Background(), ociConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			store := opened.(*objectstore.S3Store)
			t.Logf("OCI digest attachment mode: %s", store.DigestMode())
			isolated := storetest.Prefixed(store, "ocilive-"+strconv.FormatInt(int64(os.Getpid()), 10)+"-"+mode+"/")
			t.Cleanup(func() { storetest.Purge(context.Background(), isolated) })

			body := []byte("OCI compat digest probe")
			key := "slash/in/the/key/object"
			if err = isolated.Put(context.Background(), key, body); err != nil {
				t.Fatal(err)
			}
			if err = isolated.SetVerifiedDigest(context.Background(), key, storetest.Digest(body)); err != nil {
				t.Fatalf("SetVerifiedDigest (%s mode): %v", store.DigestMode(), err)
			}
			info, err := isolated.Stat(context.Background(), key)
			if err != nil || info.Digest != storetest.Digest(body) {
				t.Fatalf("Stat after SetVerifiedDigest = %#v, %v", info, err)
			}
		})
	}
}

// TestOCIMultipart uploads above the multipart threshold at the default 16 MiB
// part size, in both checksum modes, and reads the object back whole. The
// threshold is lowered from 256 MiB so the test stays small; the part size, which
// is what the provider's limits constrain, is the default.
func TestOCIMultipart(t *testing.T) {
	if parts := objectstore.MultipartParts(objectstore.MaxMultipartObjectBytes(objectstore.DefaultMultipartPartSize), objectstore.DefaultMultipartPartSize); parts >= objectstore.MaxMultipartParts {
		t.Fatalf("the largest supported object needs %d parts at the default part size, want fewer than %d", parts, objectstore.MaxMultipartParts)
	}
	for _, mode := range checksumModes {
		t.Run(mode, func(t *testing.T) {
			cfg := ociConfig(t, mode)
			cfg.MultipartPartSize = objectstore.DefaultMultipartPartSize
			cfg.MultipartThreshold = objectstore.DefaultMultipartPartSize + 1
			opened, err := objectstore.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			isolated := storetest.Prefixed(opened, "ocilive-multipart-"+mode+"/")
			t.Cleanup(func() { storetest.Purge(context.Background(), isolated) })

			body := make([]byte, 40<<20+17) // three 16 MiB parts
			state := uint32(7)
			for i := range body {
				state = state*1664525 + 1013904223
				body[i] = byte(state >> 24)
			}
			if err = isolated.PutVerifiedReader(context.Background(), "big/object", bytes.NewReader(body), int64(len(body)), storetest.Digest(body)); err != nil {
				t.Fatalf("multipart upload at the default part size: %v", err)
			}
			got, err := isolated.Get(context.Background(), "big/object")
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("multipart object differs after the round trip: %v", err)
			}
			if info, statErr := isolated.Stat(context.Background(), "big/object"); statErr != nil || info.Digest != storetest.Digest(body) || info.Size != int64(len(body)) {
				t.Fatalf("Stat = %#v, %v", info, statErr)
			}
		})
	}
}
