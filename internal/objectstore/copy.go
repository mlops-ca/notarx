package objectstore

import (
	"context"
	"io"
)

// Copier is implemented by a store that copies an object to another key of the same store without the bytes passing
// through the caller (S3 CopyObject, an in-process copy). CopyVerified prefers it over streaming.
type Copier interface {
	// CopyObject copies the object at src to dst, replacing dst. The copy carries the metadata of the source, its
	// verified digest included, so the caller must have checked that digest first (CopyVerified does).
	CopyObject(ctx context.Context, src, dst string) error
}

// CopyVerified copies the object at src to dst on the strength of the digest recorded when src was written: it refuses a
// source that is missing (ErrNotFound), whose recorded digest differs from digest or has no digest (ErrDigestMismatch),
// or whose size differs from size (ErrSizeMismatch). A store that implements Copier moves no bytes through the node;
// another store streams the source into a verified write.
func CopyVerified(ctx context.Context, store Store, src, dst, digest string, size int64) error {
	if err := validDigestSyntax(digest); err != nil {
		return err
	}
	info, err := store.Stat(ctx, src)
	if err != nil {
		return err
	}
	if info.Digest != digest {
		return ErrDigestMismatch
	}
	if info.Size != size {
		return ErrSizeMismatch
	}
	if copier, ok := store.(Copier); ok {
		return copier.CopyObject(ctx, src, dst)
	}
	reader, _, err := store.Open(ctx, src)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	return store.PutVerifiedReader(ctx, dst, io.LimitReader(reader, size+1), size, digest)
}

// CopyObject implements Copier for the in-memory store.
func (s *MemoryStore) CopyObject(ctx context.Context, src, dst string) error {
	object, err := s.lookup(ctx, src)
	if err != nil {
		return err
	}
	return s.store(ctx, dst, append([]byte(nil), object.data...), object.digest)
}
