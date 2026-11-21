package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/ocigraph"
	"github.com/mlops-ca/notarx/internal/repository"
)

// maxImportTags bounds the tags one unit may ask for.
const maxImportTags = 128

// importManifest is the formatManifest of an OCI unit: the tags to apply to the root manifest and the digests of its
// referrers. Which blobs are manifests, and everything else about the image, is read from the verified manifests.
type importManifest struct {
	Tags      []string `json:"tags,omitempty"`
	Referrers []string `json:"referrers,omitempty"`
}

// ImportStore is what the importer needs of the stores: the registry metadata and the hosted repository it publishes into.
type ImportStore interface {
	repository.NativeOCIStore
	GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error)
}

// NativeImporter implements formatmodule.Importer for OCI (ADR 0021, Remote targets). The unit is the root manifest
// (coordinate: the image name, digest: the manifest digest) with every child manifest and blob below it and the referrer
// manifests listed in the formatManifest, all supplied as blobs of the unit. Blob sharing across instances is impossible, so
// every blob the sender supplied is verified against its digest; the structure is read from the verified manifests and the
// unit must be exactly the closure they describe.
type NativeImporter struct {
	Store   ImportStore
	Objects objectstore.Store
}

// plannedManifest is a manifest of the unit with its verified bytes.
type plannedManifest struct {
	name, digest, mediaType, subject, artifactType string
	body                                           []byte
}

// record is the registry row of the manifest stored at key.
func (p *plannedManifest) record(repositoryID, key string) repository.OCIManifest {
	return repository.OCIManifest{RepositoryID: repositoryID, Name: p.name, Digest: p.digest, ObjectKey: key, MediaType: p.mediaType, SubjectDigest: p.subject, ArtifactType: p.artifactType, Size: int64(len(p.body))}
}

// importPlan is the closure of the unit as the verified manifests describe it.
type importPlan struct {
	name          string
	byDigest      map[string]formatmodule.BlobDescriptor
	manifests     map[string]*plannedManifest
	order         []*plannedManifest // children before the manifests that reference them
	blobs         map[string]bool
	manifestBytes int64
	open          func(formatmodule.BlobDescriptor) (io.ReadCloser, error)
}

// ImportUnit implements formatmodule.Importer.
func (i NativeImporter) ImportUnit(ctx context.Context, repositoryID string, unit formatmodule.ExportedUnit, open func(formatmodule.BlobDescriptor) (io.ReadCloser, error)) error {
	if i.Store == nil || i.Objects == nil {
		return repository.ErrDisabled
	}
	repo, err := i.Store.GetHostedRepository(ctx, repositoryID)
	if err != nil {
		return err
	}
	if repo.Format != repository.FormatOCI || repo.Type != repository.RepositoryTypeHosted {
		return formatmodule.ErrImportUnsupportedLayout
	}
	// A coordinate with an @ is not an image name and stays rejected.
	if unit.Format != repository.FormatOCI || !ocigraph.ValidName(unit.Coordinate) || !ocigraph.ValidDigest(unit.Digest) {
		return formatmodule.ErrImportInvalidUnit
	}
	var manifest importManifest
	if err = formatmodule.DecodeImportManifest(unit.Manifest, &manifest); err != nil {
		return err
	}
	if err = validateImportManifest(manifest, unit.Digest); err != nil {
		return err
	}
	plan, err := planImport(unit, manifest, open)
	if err != nil {
		return err
	}
	// A tag is applied only when it is absent or already points at this digest; a move is an explicit operator action. The
	// check here refuses a conflict before any byte is written; applyTag checks again under the lock of the tag.
	for _, tag := range manifest.Tags {
		if err = i.checkTag(ctx, repositoryID, unit.Coordinate, tag, unit.Digest); err != nil {
			return err
		}
	}
	for _, blob := range unit.Blobs {
		if plan.blobs[blob.Digest] {
			if err = i.publishBlob(ctx, repositoryID, unit.Coordinate, blob, open); err != nil {
				return err
			}
		}
	}
	// Every manifest is published by digest first, children before their parents and the referrers after the image, and the
	// tags are applied last: a failure part way never leaves a tagged image without its referrers.
	for _, planned := range plan.order {
		if err = i.publishManifest(ctx, repositoryID, unit.Coordinate, planned); err != nil {
			return err
		}
	}
	for _, tag := range manifest.Tags {
		if err = i.applyTag(ctx, repositoryID, plan.manifests[unit.Digest], tag); err != nil {
			return err
		}
	}
	return nil
}

// checkTag refuses a tag that points at another digest.
func (i NativeImporter) checkTag(ctx context.Context, repositoryID, name, tag, digest string) error {
	existing, err := i.Store.GetOCIManifest(ctx, repositoryID, name, tag)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return nil
	case err != nil:
		return err
	case existing.Digest != digest:
		return formatmodule.ErrImportConflict
	}
	return nil
}

// applyTag points the tag at the root manifest. It holds the lock that every writer of the tag takes, so the check and the
// write are not interleaved with a push of the same tag.
func (i NativeImporter) applyTag(ctx context.Context, repositoryID string, root *plannedManifest, tag string) error {
	release, err := i.Store.LockOCIObject(ctx, repository.OCITagLockKey(repositoryID, root.name, tag))
	if err != nil {
		return err
	}
	defer release()
	if err = i.checkTag(ctx, repositoryID, root.name, tag, root.digest); err != nil {
		return err
	}
	_, err = i.Store.PutOCIManifest(ctx, root.record(repositoryID, ManifestObjectKey(repositoryID, root.name, root.digest)), tag)
	return err
}

func validateImportManifest(manifest importManifest, root string) error {
	if len(manifest.Tags) > maxImportTags || len(manifest.Referrers) > ocigraph.MaxObjects {
		return formatmodule.ErrImportInvalidUnit
	}
	seen := map[string]bool{root: true}
	for _, referrer := range manifest.Referrers {
		if !ocigraph.ValidDigest(referrer) || seen[referrer] {
			return formatmodule.ErrImportInvalidUnit
		}
		seen[referrer] = true
	}
	tags := map[string]bool{}
	for _, tag := range manifest.Tags {
		if ocigraph.ValidDigest(tag) || !ocigraph.ValidReference(tag) || tags[tag] {
			return formatmodule.ErrImportInvalidUnit
		}
		tags[tag] = true
	}
	return nil
}

// planImport reads the root and referrer manifests and everything below them from the unit's verified bytes. The unit is
// invalid unless its blobs are exactly the closure those manifests describe, and a descriptor whose size differs from the
// blob it names is a mismatch.
func planImport(unit formatmodule.ExportedUnit, manifest importManifest, open func(formatmodule.BlobDescriptor) (io.ReadCloser, error)) (*importPlan, error) {
	plan := &importPlan{name: unit.Coordinate, byDigest: make(map[string]formatmodule.BlobDescriptor, len(unit.Blobs)), manifests: map[string]*plannedManifest{}, blobs: map[string]bool{}, open: open}
	for _, blob := range unit.Blobs {
		if _, duplicate := plan.byDigest[blob.Digest]; duplicate || !ocigraph.ValidDigest(blob.Digest) || blob.Size < 0 {
			return nil, formatmodule.ErrImportInvalidUnit
		}
		plan.byDigest[blob.Digest] = blob
	}
	if err := plan.addManifest(unit.Digest, "", 0); err != nil {
		return nil, err
	}
	image := make(map[string]bool, len(plan.manifests))
	for digest := range plan.manifests {
		image[digest] = true
	}
	for _, referrer := range manifest.Referrers {
		if image[referrer] {
			return nil, formatmodule.ErrImportInvalidUnit
		}
		if err := plan.addManifest(referrer, "", 0); err != nil {
			return nil, err
		}
	}
	// A referrer points at a manifest of the image it is shipped with.
	for _, referrer := range manifest.Referrers {
		if !image[plan.manifests[referrer].subject] {
			return nil, formatmodule.ErrImportInvalidUnit
		}
	}
	if len(plan.manifests)+len(plan.blobs) != len(plan.byDigest) {
		return nil, formatmodule.ErrImportInvalidUnit // a blob no manifest references
	}
	return plan, nil
}

// addManifest verifies and reads one manifest, then its children and blobs. mediaType is what the referencing descriptor
// claims, empty for a root.
func (p *importPlan) addManifest(digest, mediaType string, depth int) error {
	if planned, seen := p.manifests[digest]; seen {
		if mediaType != "" && planned.mediaType != mediaType {
			return formatmodule.ErrImportInvalidUnit
		}
		return nil
	}
	blob, supplied := p.byDigest[digest]
	if !supplied || depth > ocigraph.MaxDepth || blob.Size > ocigraph.MaxManifestBytes || p.blobs[digest] {
		return formatmodule.ErrImportInvalidUnit
	}
	if p.manifestBytes += blob.Size; p.manifestBytes > ocigraph.MaxInventoryManifestBytes {
		return formatmodule.ErrImportInvalidUnit
	}
	body, err := p.read(blob)
	if err != nil {
		return err
	}
	document, err := decodeManifestDocument(body)
	if err != nil || document.SchemaVersion != 2 {
		return formatmodule.ErrImportInvalidUnit
	}
	resolved := document.MediaType
	if resolved == "" {
		resolved = mediaType
	}
	if resolved == "" {
		resolved = ocigraph.OCIManifest
		if len(document.Manifests) > 0 {
			resolved = ocigraph.OCIIndex
		}
	}
	if !isManifestMediaType(resolved) || mediaType != "" && mediaType != resolved {
		return formatmodule.ErrImportInvalidUnit
	}
	planned := &plannedManifest{name: p.name, digest: digest, mediaType: resolved, artifactType: document.ArtifactType, body: body}
	if subject, has := subjectDigest(document); has {
		if !ocigraph.ValidDigest(subject) {
			return formatmodule.ErrImportInvalidUnit
		}
		planned.subject = subject
	}
	p.manifests[digest] = planned
	for _, child := range document.Manifests {
		if err = p.addDescriptor(child, depth+1); err != nil {
			return err
		}
	}
	for _, item := range blobDescriptors(document) {
		if err = p.addDescriptor(item, depth+1); err != nil {
			return err
		}
	}
	p.order = append(p.order, planned)
	return nil
}

// addDescriptor follows one descriptor of a manifest: a manifest is read, a blob is recorded, and a foreign layer that is
// fetched from URLs is not part of the unit.
func (p *importPlan) addDescriptor(item ocigraph.Descriptor, depth int) error {
	if !ocigraph.ValidDigest(item.Digest) || item.Size == nil || *item.Size < 0 {
		return formatmodule.ErrImportInvalidUnit
	}
	if len(item.URLs) > 0 {
		return nil
	}
	blob, supplied := p.byDigest[item.Digest]
	if !supplied {
		return formatmodule.ErrImportInvalidUnit
	}
	if blob.Size != *item.Size {
		return formatmodule.ErrImportMismatch
	}
	if isManifestMediaType(item.MediaType) {
		return p.addManifest(item.Digest, item.MediaType, depth)
	}
	if _, isManifest := p.manifests[item.Digest]; isManifest {
		return formatmodule.ErrImportInvalidUnit
	}
	p.blobs[item.Digest] = true
	return nil
}

// read returns the bytes of a manifest blob after verifying their digest and size.
func (p *importPlan) read(blob formatmodule.BlobDescriptor) ([]byte, error) {
	reader, err := p.open(blob)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(io.LimitReader(reader, blob.Size+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if int64(len(body)) != blob.Size || "sha256:"+hex.EncodeToString(sum[:]) != blob.Digest {
		return nil, formatmodule.ErrImportMismatch
	}
	return body, nil
}

// publishBlob makes one blob visible in the repository: a blob the repository holds is left alone, any other is written
// from the bytes the sender supplied, verified against the digest, into its content-addressed object. A blob that other
// repositories hold is never mounted from them: the sender must prove it holds the bytes.
func (i NativeImporter) publishBlob(ctx context.Context, repositoryID, name string, blob formatmodule.BlobDescriptor, open func(formatmodule.BlobDescriptor) (io.ReadCloser, error)) error {
	existing, err := i.Store.GetOCIBlob(ctx, repositoryID, blob.Digest)
	if err == nil {
		return sameSize(existing.Size, blob.Size)
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	key := repository.OCIBlobObjectKey(blob.Digest)
	release, err := i.Store.LockOCIObject(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	if err = i.Store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: repositoryID, ObjectKey: key, Digest: blob.Digest, Size: blob.Size}); err != nil {
		return errors.Join(formatmodule.ErrPublicationStage, err)
	}
	if err = formatmodule.StoreImportedBlob(ctx, i.Objects, blob, key, open); err != nil {
		if errors.Is(err, formatmodule.ErrImportMismatch) {
			return err
		}
		return errors.Join(formatmodule.ErrPublicationUpload, err)
	}
	// The registry records a blob through the upload that completed it, so the import opens one that is already finished.
	id := ids.New()
	if _, err = i.Store.CreateOCIUpload(ctx, repository.OCIUpload{ID: id, RepositoryID: repositoryID, Name: name, ObjectKey: "native/oci/uploads/" + id, State: "open", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		return err
	}
	_, err = i.Store.CompleteOCIUpload(ctx, id, repository.OCIBlob{Digest: blob.Digest, ObjectKey: key, Size: blob.Size})
	return err
}

func sameSize(held, claimed int64) error {
	if held != claimed {
		return formatmodule.ErrImportMismatch
	}
	return nil
}

// publishManifest writes a verified manifest into its target-owned object and registers it by digest.
func (i NativeImporter) publishManifest(ctx context.Context, repositoryID, name string, planned *plannedManifest) error {
	key := ManifestObjectKey(repositoryID, name, planned.digest)
	release, err := i.Store.LockOCIObject(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	size := int64(len(planned.body))
	if err = i.Store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: repositoryID, ObjectKey: key, Digest: planned.digest, Size: size}); err != nil {
		return errors.Join(formatmodule.ErrPublicationStage, err)
	}
	if err = i.Objects.PutVerifiedReader(ctx, key, bytes.NewReader(planned.body), size, planned.digest); err != nil {
		return errors.Join(formatmodule.ErrPublicationUpload, err)
	}
	_, err = i.Store.PutOCIManifest(ctx, planned.record(repositoryID, key), planned.digest)
	return err
}
