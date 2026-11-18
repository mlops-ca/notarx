package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/nexusmigration"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

var hostileKeyNames = []string{"", ".", "..", ".hidden", ".conan", "a/../b", "a//b", strings.Repeat("x", 400), "tab\tname", "100%"}

func TestObjectKeyBuildersProduceKeysValidForEveryDriver(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, name := range hostileKeyNames {
		keys := map[string]string{
			"pypi":  pypiReplicationTargetObjectKey("repo", name, digest),
			"oci":   ociReplicationTargetObjectKey("repo", name, digest),
			"conan": nexusConanObjectKey(nexusmigration.Entry{ID: "entry", SessionID: "session"}, nexusmigration.Asset{SHA256: strings.Repeat("b", 64), TargetPath: name}),
		}
		for builder, key := range keys {
			if err := objectstore.ValidateKey(key); err != nil {
				t.Errorf("%s builder, name %.30q: key %.60q: %v", builder, name, key, err)
			}
		}
	}
}

func TestObjectKeyBuildersKeepKeysForOrdinaryNames(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	if got, want := pypiReplicationTargetObjectKey("repo", "pkg-1.0-py3-none-any.whl", digest), "native/pypi/replication/repo/"+strings.Repeat("a", 64)+"/pkg-1.0-py3-none-any.whl"; got != want {
		t.Errorf("pypi key = %q, want %q", got, want)
	}
	if got, want := ociReplicationTargetObjectKey("repo", "team/widget", digest), "native/oci/manifests/repo/team%2Fwidget/"+strings.Repeat("a", 64); got != want {
		t.Errorf("oci key = %q, want %q", got, want)
	}
}

func TestObjectKeyBuildersWriteToFilesystemDriver(t *testing.T) {
	cfg := objectstore.Defaults()
	cfg.Driver = objectstore.DriverFilesystem
	cfg.FSRoot = filepath.Join(t.TempDir(), "objects")
	cfg.FSReserveBytes = 0
	store, err := objectstore.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, name := range hostileKeyNames {
		for _, key := range []string{
			pypiReplicationTargetObjectKey("repo", name, digest),
			ociReplicationTargetObjectKey("repo", name, digest),
			nexusConanObjectKey(nexusmigration.Entry{ID: "entry", SessionID: "session"}, nexusmigration.Asset{SHA256: strings.Repeat("b", 64), TargetPath: name}),
		} {
			if err := store.Put(context.Background(), key, []byte("x")); err != nil {
				t.Errorf("name %.30q: Put(%.60q): %v", name, key, err)
			}
		}
	}
}
