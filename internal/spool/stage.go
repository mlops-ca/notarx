package spool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// ErrObjectTooLarge reports an object above the configured size limit.
var ErrObjectTooLarge = errors.New("object exceeds size limit")

// StreamCopyBufferSize is the copy buffer used for large object streams.
const StreamCopyBufferSize = 128 << 10

// Staged is a fully written temporary file with its sha256 digest. The caller
// owns Cleanup.
type Staged struct {
	Digest string
	Size   int64
	Path   string
}

// Stage copies reader into a bounded-buffer temporary file while computing its
// digest. pattern names the temporary file. A stream longer than maxBytes
// fails with ErrObjectTooLarge and leaves no file behind.
func Stage(reader io.Reader, maxBytes int64, pattern string) (Staged, error) {
	if maxBytes < 0 {
		return Staged{}, ErrObjectTooLarge
	}
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return Staged{}, err
	}
	fail := func(err error) (Staged, error) {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		return Staged{}, err
	}
	limit := maxBytes
	if maxBytes < int64(^uint64(0)>>1) {
		limit++
	}
	hash := sha256.New()
	written, err := io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(reader, limit), make([]byte, StreamCopyBufferSize))
	if err != nil {
		return fail(err)
	}
	if written > maxBytes {
		return fail(ErrObjectTooLarge)
	}
	if err := file.Close(); err != nil {
		return fail(err)
	}
	return Staged{Digest: hex.EncodeToString(hash.Sum(nil)), Size: written, Path: file.Name()}, nil
}

// Open opens the whole staged file.
func (s Staged) Open() (io.ReadCloser, int64, error) {
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, 0, err
	}
	return file, s.Size, nil
}

// OpenRange opens length bytes at offset of the staged file.
func (s Staged) OpenRange(_ context.Context, offset, length int64) (io.ReadCloser, int64, error) {
	if offset < 0 || length < 0 || offset > s.Size || length > s.Size-offset {
		return nil, 0, errors.New("staged range is out of bounds")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, 0, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return struct {
		io.Reader
		io.Closer
	}{Reader: io.LimitReader(file, length), Closer: file}, s.Size, nil
}

// Cleanup removes the temporary file.
func (s Staged) Cleanup() {
	if s.Path != "" {
		_ = os.Remove(s.Path)
	}
}
