package objectstore

import (
	"context"
	"errors"
	"io"
)

// ErrNoRecordedDigest is returned by CopyVerified for a source that carries no digest (it was not written verified, or its
// store lost the record): its bytes cannot be trusted without being read, so the caller streams them into a verified write.
var ErrNoRecordedDigest = errors.New("the source object has no recorded digest")

// ErrCopyUnavailable is returned by a Copier whose service does not do (or refused) the server-side copy of this object;
// CopyVerified then streams the object instead.
var ErrCopyUnavailable = errors.New("server-side copy is unavailable")

// Copier is implemented by a store that copies an object to another key of the same store without the bytes passing
// through the caller (S3 CopyObject, an in-process copy). CopyVerified prefers it over streaming.
type Copier interface {
	// CopyObject copies the object at src, which has size bytes, to dst, replacing dst. The copy carries the metadata of
	// the source, its verified digest included, so the caller must have checked that digest first (CopyVerified does).
	CopyObject(ctx context.Context, src, dst string, size int64) error
}

// CopyVerified copies the object at src to dst on the strength of the digest recorded when src was written: it refuses a
// source that is missing (ErrNotFound), that has no recorded digest (ErrNoRecordedDigest), whose recorded digest differs
// from digest (ErrDigestMismatch), or whose size differs from size (ErrSizeMismatch). A store that implements Copier moves no bytes through the node;
// another store streams the source into a verified write.
func CopyVerified(ctx context.Context, store Store, src, dst, digest string, size int64) error {
	if err := validDigestSyntax(digest); err != nil {
		return err
	}
	info, err := store.Stat(ctx, src)
	if err != nil {
		return err
	}
	if info.Digest == "" {
		return ErrNoRecordedDigest
	}
	if info.Digest != digest {
		return ErrDigestMismatch
	}
	if info.Size != size {
		return ErrSizeMismatch
	}
	if copier, ok := store.(Copier); ok {
		if err = copier.CopyObject(ctx, src, dst, size); !errors.Is(err, ErrCopyUnavailable) {
			return err
		}
	}
	reader, _, err := store.Open(ctx, src)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	return store.PutVerifiedReader(ctx, dst, io.LimitReader(reader, size+1), size, digest)
}

// CopyObject implements Copier for the in-memory store.
func (s *MemoryStore) CopyObject(ctx context.Context, src, dst string, _ int64) error {
	object, err := s.lookup(ctx, src)
	if err != nil {
		return err
	}
	return s.store(ctx, dst, append([]byte(nil), object.data...), object.digest)
}
