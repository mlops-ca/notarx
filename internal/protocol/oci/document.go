package oci

import (
	"encoding/json"

	"github.com/mlops-ca/notarx/internal/ocigraph"
)

// descriptor is one content descriptor of a manifest, read leniently: only what promotion, import and release sets need.
type descriptor struct {
	MediaType string   `json:"mediaType"`
	Digest    string   `json:"digest"`
	Size      *int64   `json:"size"`
	URLs      []string `json:"urls"`
}

// manifestDocument is the part of an image manifest or index that names other objects. It admits artifact manifests with
// custom config and layer types, which ocigraph.Decode does not.
type manifestDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        *descriptor  `json:"config"`
	Layers        []descriptor `json:"layers"`
	Manifests     []descriptor `json:"manifests"`
	Subject       *descriptor  `json:"subject"`
	ArtifactType  string       `json:"artifactType"`
}

func decodeManifestDocument(body []byte) (manifestDocument, error) {
	var document manifestDocument
	err := json.Unmarshal(body, &document)
	return document, err
}

// isManifestMediaType reports whether a descriptor of this media type names a manifest or an index rather than a blob.
func isManifestMediaType(mediaType string) bool {
	return ocigraph.IsManifest(mediaType) || ocigraph.IsIndex(mediaType)
}

// blobDescriptors are the config and layers: the objects a manifest references that are blobs.
func (d manifestDocument) blobDescriptors() []descriptor {
	out := make([]descriptor, 0, 1+len(d.Layers))
	if d.Config != nil {
		out = append(out, *d.Config)
	}
	return append(out, d.Layers...)
}
