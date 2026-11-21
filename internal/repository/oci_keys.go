package repository

import "strings"

// OCIBlobObjectKey is the object key of a blob: blobs are content-addressed across the instance. Every producer of blob keys
// and the import validation use this one function.
func OCIBlobObjectKey(digest string) string {
	return "native/oci/blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

// OCITagLockKey is the object lock (NativeOCIStore.LockOCIObject) that serializes the writers of one tag, so a writer that
// must not move a tag (an import) can check and write it without another writer moving it in between. It is taken after any
// manifest object lock, never before.
func OCITagLockKey(repositoryID, name, tag string) string {
	return "oci-tag:" + repositoryID + ":" + name + ":" + tag
}
