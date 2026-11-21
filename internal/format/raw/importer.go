package raw

import (
	"context"
	"errors"
	"io"
	"strings"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/v2contract"
)

// importManifest is the formatManifest of a Raw unit: where the file goes and how it is served. It carries no size or
// digest, which are recomputed from the bytes.
type importManifest struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType,omitempty"`
}

// parseImportManifest reads the manifest strictly: an unknown field, a trailing value or a path that is not canonical is
// an invalid unit.
func parseImportManifest(raw []byte) (importManifest, error) {
	var manifest importManifest
	if err := formatmodule.DecodeImportManifest(raw, &manifest); err != nil {
		return importManifest{}, err
	}
	canonical, ok := v2contract.CanonicalRawPath(manifest.Path)
	if !ok || canonical != manifest.Path {
		return importManifest{}, formatmodule.ErrImportInvalidUnit
	}
	if len(manifest.ContentType) > 255 || strings.ContainsAny(manifest.ContentType, "\r\n\x00") {
		return importManifest{}, formatmodule.ErrImportInvalidUnit
	}
	if manifest.ContentType == "" {
		manifest.ContentType = "application/octet-stream"
	}
	return manifest, nil
}

// ImportUnit implements formatmodule.Importer for a flat Raw repository: the unit is one file, its coordinate the path of
// the manifest, its only blob the file. The bytes are hashed while they are copied into their content-addressed object, so
// a digest or size that differs from the claim stores nothing and publishes nothing. A path that already holds the same
// digest is a replay; another digest at the path is a conflict.
func (m *Module) ImportUnit(ctx context.Context, repositoryID string, unit formatmodule.ExportedUnit, open func(formatmodule.BlobDescriptor) (io.ReadCloser, error)) error {
	if m.store == nil || m.objects == nil || m.repos == nil {
		return repository.ErrDisabled
	}
	repo, err := m.repos.GetHostedRepository(ctx, repositoryID)
	if err != nil {
		return err
	}
	if repo.Format != repository.FormatRaw || repo.Type != repository.RepositoryTypeHosted || repo.RawLayout == repository.RawLayoutVersioned {
		return formatmodule.ErrImportUnsupportedLayout
	}
	manifest, err := parseImportManifest(unit.Manifest)
	if err != nil {
		return err
	}
	// In a flat repository a name ending in .sha256 or .sha512 is the verification sidecar of a path and is never served as
	// a file of its own, so an asset stored under it would be unreachable.
	if rawChecksumExtension(manifest.Path) != "" {
		return formatmodule.ErrImportInvalidUnit
	}
	if len(unit.Blobs) != 1 || unit.Blobs[0].Digest != unit.Digest || unit.Format != repository.FormatRaw {
		return formatmodule.ErrImportInvalidUnit
	}
	if unit.Coordinate != manifest.Path {
		return formatmodule.ErrImportMismatch
	}
	blob := unit.Blobs[0]
	objectCtx, release, err := repository.LockObjectKeys(ctx, []string{blob.Digest, "import:" + repositoryID + ":" + manifest.Path}, m.store, repository.FormatRaw, m.store.LockRawObject)
	if err != nil {
		return err
	}
	defer release()
	if replayed, existingErr := m.importedAlready(objectCtx, repositoryID, manifest.Path, blob.Digest); existingErr != nil || replayed {
		return existingErr
	}
	key := NativeObjectKey(blob.Digest)
	if err = m.store.StageRawObject(objectCtx, repository.RawObject{RepositoryID: repositoryID, Digest: blob.Digest, ObjectKey: key, Size: blob.Size}); err != nil {
		return errors.Join(formatmodule.ErrPublicationStage, err)
	}
	if err = formatmodule.StoreImportedBlob(objectCtx, m.objects, blob, key, open); err != nil {
		if errors.Is(err, formatmodule.ErrImportMismatch) {
			return err
		}
		return errors.Join(formatmodule.ErrPublicationUpload, err)
	}
	_, err = m.store.PutRawAssetIf(objectCtx, repository.RawAsset{RepositoryID: repositoryID, Path: manifest.Path, ObjectKey: key, Digest: blob.Digest, Size: blob.Size, ContentType: manifest.ContentType}, repository.RawWriteCondition{RequireAbsent: true})
	if errors.Is(err, repository.ErrPreconditionFailed) {
		// Another writer took the path between the check and the write.
		if replayed, existingErr := m.importedAlready(objectCtx, repositoryID, manifest.Path, blob.Digest); existingErr != nil || replayed {
			return existingErr
		}
	}
	return err
}

// importedAlready reports whether the path holds the digest already (a replay: nothing to do) and refuses a path that holds
// another digest.
func (m *Module) importedAlready(ctx context.Context, repositoryID, path, digest string) (bool, error) {
	existing, err := m.store.GetRawAsset(ctx, repositoryID, path)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case existing.Digest == digest:
		return true, nil
	}
	return false, formatmodule.ErrImportConflict
}
