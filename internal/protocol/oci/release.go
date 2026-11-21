package oci

import (
	"context"
	"errors"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/ocigraph"
	"github.com/mlops-ca/notarx/internal/repository"
)

// ReleaseSet implements formatmodule.ReleaseSet for OCI: the release of a manifest digest is the manifest and, for an index,
// every child manifest below it that the repository holds (ADR 0021, Format capabilities and scope). Blobs are not members: a
// promotion mounts them with the manifest that references them. Referrers are not members either.
type ReleaseSet struct {
	Store interface {
		GetOCIManifest(ctx context.Context, repositoryID, name, reference string) (repository.OCIManifest, error)
	}
	Objects objectstore.Store
}

// ReleaseMembers returns the manifest first, then its children in the order the indexes list them. A child the repository
// does not hold is left out: the index is promoted as the source holds it. An unknown manifest is repository.ErrNotFound.
func (r ReleaseSet) ReleaseMembers(ctx context.Context, repositoryID, name, digest string) ([]formatmodule.ReleaseMember, error) {
	if r.Store == nil || r.Objects == nil {
		return nil, repository.ErrDisabled
	}
	if !ocigraph.ValidDigest(digest) {
		return nil, repository.ErrNotFound
	}
	members := []formatmodule.ReleaseMember{}
	seen := map[string]bool{}
	var walk func(digest string, depth int) error
	walk = func(digest string, depth int) error {
		if seen[digest] || depth > ocigraph.MaxDepth || len(members) >= ocigraph.MaxObjects {
			return nil
		}
		seen[digest] = true
		manifest, err := r.Store.GetOCIManifest(ctx, repositoryID, name, digest)
		if err != nil {
			if depth > 0 && errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			return err
		}
		members = append(members, formatmodule.ReleaseMember{Coordinate: name, Digest: manifest.Digest})
		if !ocigraph.IsIndex(manifest.MediaType) {
			return nil
		}
		body, err := r.Objects.Get(ctx, manifest.ObjectKey)
		if err != nil {
			return err
		}
		document, err := decodeManifestDocument(body)
		if err != nil {
			return nil
		}
		for _, child := range document.Manifests {
			if err = walk(child.Digest, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(digest, 0); err != nil {
		return nil, err
	}
	return members, nil
}
