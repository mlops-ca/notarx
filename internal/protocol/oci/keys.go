package oci

import (
	"net/url"
	"strings"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// ManifestObjectKey is the object key of a manifest stored in a repository. The
// image name is user controlled, so it is one percent-encoded path segment, and
// a name that would still be refused by an object store driver (dot-leading,
// over-long) is encoded by objectstore.SafeKeySegment instead. Every producer of
// manifest keys uses this one function.
func ManifestObjectKey(repositoryID, name, digest string) string {
	segment := url.PathEscape(name)
	if !objectstore.KeySegmentUsable(segment) {
		segment = objectstore.SafeKeySegment(name)
	}
	return "native/oci/manifests/" + repositoryID + "/" + segment + "/" + strings.TrimPrefix(digest, "sha256:")
}
