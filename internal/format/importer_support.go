package format

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// DecodeImportManifest reads the formatManifest of an import unit into target strictly, as every Importer must: an
// unknown field or a value after the first is an invalid unit.
func DecodeImportManifest(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrImportInvalidUnit
	}
	return nil
}

// StoreImportedBlob writes the blob into its content-addressed key. A blob that was staged in the same object store is
// copied there on the digest recorded when it was staged, so no byte passes through this node and no local temporary
// space is used (objectstore.CopyVerified); a blob that was not (the sender's open reads it from elsewhere), or whose
// staged object carries no recorded digest, is streamed through open into a verified write that hashes the bytes. A digest
// or size the bytes contradict is ErrImportMismatch and nothing is stored under key.
func StoreImportedBlob(ctx context.Context, objects objectstore.Store, blob BlobDescriptor, key string, open func(BlobDescriptor) (io.ReadCloser, error)) error {
	err := copyStaged(ctx, objects, blob, key, open)
	if errors.Is(err, objectstore.ErrDigestMismatch) || errors.Is(err, objectstore.ErrSizeMismatch) {
		return errors.Join(ErrImportMismatch, err)
	}
	return err
}

func copyStaged(ctx context.Context, objects objectstore.Store, blob BlobDescriptor, key string, open func(BlobDescriptor) (io.ReadCloser, error)) error {
	if blob.ObjectKey != "" {
		err := objectstore.CopyVerified(ctx, objects, blob.ObjectKey, key, blob.Digest, blob.Size)
		if !errors.Is(err, objectstore.ErrNotFound) && !errors.Is(err, objectstore.ErrNoRecordedDigest) {
			return err
		}
	}
	reader, err := open(blob)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	return objects.PutVerifiedReader(ctx, key, reader, blob.Size, blob.Digest)
}
