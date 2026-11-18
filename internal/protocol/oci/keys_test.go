package oci

import (
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

func TestManifestObjectKeyIsValidForEveryDriver(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, name := range []string{"team/widget", ".hidden", "..", "", strings.Repeat("x", 400), "a/../b", ".conan/x"} {
		if err := objectstore.ValidateKey(ManifestObjectKey("repo", name, digest)); err != nil {
			t.Errorf("name %.30q: %v", name, err)
		}
	}
	if got, want := ManifestObjectKey("repo", "team/widget", digest), "native/oci/manifests/repo/team%2Fwidget/"+strings.Repeat("a", 64); got != want {
		t.Errorf("ordinary key = %q, want %q", got, want)
	}
}
