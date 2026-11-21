package oci

import (
	"encoding/json"

	"github.com/mlops-ca/notarx/internal/ocigraph"
)

// decodeManifestDocument reads the descriptors of a manifest or index leniently: it admits artifact manifests with custom
// config and layer types and a subject, which ocigraph.Decode refuses.
func decodeManifestDocument(body []byte) (ocigraph.Document, error) {
	var document ocigraph.Document
	err := json.Unmarshal(body, &document)
	return document, err
}

// isManifestMediaType reports whether a descriptor of this media type names a manifest or an index rather than a blob.
func isManifestMediaType(mediaType string) bool {
	return ocigraph.IsManifest(mediaType) || ocigraph.IsIndex(mediaType)
}

// blobDescriptors are the config and layers of a manifest that are blobs of the repository: a foreign layer that is fetched
// from URLs is not.
func blobDescriptors(d ocigraph.Document) []ocigraph.Descriptor {
	all := make([]ocigraph.Descriptor, 0, 1+len(d.Layers))
	if d.Config != nil {
		all = append(all, *d.Config)
	}
	all = append(all, d.Layers...)
	held := all[:0]
	for _, item := range all {
		if len(item.URLs) == 0 {
			held = append(held, item)
		}
	}
	return held
}

// subjectDigest is the digest of the subject descriptor of a manifest; false when the manifest has none.
func subjectDigest(d ocigraph.Document) (string, bool) {
	if len(d.Subject) == 0 || string(d.Subject) == "null" {
		return "", false
	}
	var subject ocigraph.Descriptor
	if json.Unmarshal(d.Subject, &subject) != nil {
		return "", true
	}
	return subject.Digest, true
}
