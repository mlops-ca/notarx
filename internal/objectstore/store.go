// Package objectstore defines the byte-store port shared by protocol caches
// and native artifact lifecycles.
package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound is returned when a requested object is absent.
var ErrNotFound = errors.New("object not found")

// ErrSizeMismatch is returned when a reader does not yield exactly the
// declared number of bytes.
var ErrSizeMismatch = errors.New("object size does not match the declared size")

// ErrDigestMismatch is returned when the bytes of a verified write do not hash
// to the supplied SHA-256 digest. Nothing is stored in that case.
var ErrDigestMismatch = errors.New("object digest does not match the supplied digest")

// Digests are written "sha256:<64 lowercase hex characters>".
const digestPrefix = "sha256:"

// Store is deliberately small so publication ordering can be tested without
// a running object-store service.
type Store interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
	Stat(context.Context, string) (Info, error)
	Open(context.Context, string) (io.ReadCloser, int64, error)
	OpenRange(context.Context, string, int64, int64) (io.ReadCloser, int64, error)
	PutReader(context.Context, string, io.Reader, int64) error
	PutVerifiedReader(context.Context, string, io.Reader, int64, string) error
	SetVerifiedDigest(context.Context, string, string) error
	Delete(context.Context, string) error
	List(context.Context, string) ([]string, error)
}

type Info struct {
	Size   int64
	Digest string
}

// parseDigest returns the lowercase hex of a "sha256:<hex>" digest.
func parseDigest(digest string) (string, error) {
	hexPart, ok := strings.CutPrefix(digest, digestPrefix)
	if !ok || len(hexPart) != sha256.Size*2 {
		return "", errors.New("object digest must be sha256:<64 hex characters>")
	}
	decoded, err := hex.DecodeString(hexPart)
	if err != nil {
		return "", errors.New("object digest must be sha256:<64 hex characters>")
	}
	return hex.EncodeToString(decoded), nil
}

// validDigestSyntax accepts "<algorithm>:<hex>" for SetVerifiedDigest: a sha256
// digest must be exactly 64 hex characters; other algorithms (an OCI manifest
// may be sha512) need only be well-formed, since only sha256 can be re-verified.
func validDigestSyntax(digest string) error {
	algorithm, hexPart, ok := strings.Cut(digest, ":")
	if !ok || algorithm == "" || len(hexPart) < 32 || len(hexPart)%2 != 0 {
		return errors.New("object digest must be <algorithm>:<hex>")
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return errors.New("object digest must be <algorithm>:<hex>")
	}
	if algorithm == "sha256" && len(hexPart) != sha256.Size*2 {
		return errors.New("object digest must be sha256:<64 hex characters>")
	}
	return nil
}

// MemoryStore is the in-process Store used by tests. It follows the same
// contract as the other drivers (see storetest.RunContract): verified writes
// check size and digest, listings are lexicographic, and every call honours
// context cancellation.
type MemoryStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

type memoryObject struct {
	data   []byte
	digest string // empty until a verified write or SetVerifiedDigest
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[string]memoryObject)}
}

func (s *MemoryStore) lookup(ctx context.Context, key string) (memoryObject, error) {
	if err := ctx.Err(); err != nil {
		return memoryObject{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	object, ok := s.objects[key]
	if !ok {
		return memoryObject{}, ErrNotFound
	}
	return object, nil
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), object.data...), nil
}

func (s *MemoryStore) Put(ctx context.Context, key string, value []byte) error {
	return s.store(ctx, key, append([]byte(nil), value...), "")
}

func (s *MemoryStore) store(ctx context.Context, key string, data []byte, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A write always allocates a fresh backing slice. A reader can safely
	// retain this immutable snapshot across replacement or deletion.
	s.objects[key] = memoryObject{data: data, digest: digest}
	return nil
}

func (s *MemoryStore) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	object, err := s.lookup(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(object.data)), int64(len(object.data)), nil
}

// Stat reports the recorded digest. An object written without one reports the
// SHA-256 of its bytes, which an S3 object lacking metadata would not.
func (s *MemoryStore) Stat(ctx context.Context, key string) (Info, error) {
	object, err := s.lookup(ctx, key)
	if err != nil {
		return Info{}, err
	}
	digest := object.digest
	if digest == "" {
		sum := sha256.Sum256(object.data)
		digest = digestPrefix + hex.EncodeToString(sum[:])
	}
	return Info{Size: int64(len(object.data)), Digest: digest}, nil
}

func (s *MemoryStore) OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	object, err := s.lookup(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	size := int64(len(object.data))
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		return nil, 0, errors.New("object range is out of bounds")
	}
	return io.NopCloser(bytes.NewReader(object.data[offset : offset+length])), size, nil
}

func readExactly(value io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("object size is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(value, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, ErrSizeMismatch
	}
	return data, nil
}

func (s *MemoryStore) PutReader(ctx context.Context, key string, value io.Reader, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := readExactly(value, size)
	if err != nil {
		return err
	}
	return s.store(ctx, key, data, "")
}

func (s *MemoryStore) PutVerifiedReader(ctx context.Context, key string, value io.Reader, size int64, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want, err := parseDigest(digest)
	if err != nil {
		return err
	}
	data, err := readExactly(value, size)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
		return ErrDigestMismatch
	}
	return s.store(ctx, key, data, digestPrefix+want)
}

func (s *MemoryStore) SetVerifiedDigest(ctx context.Context, key, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validDigestSyntax(digest); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return ErrNotFound
	}
	object.digest = digest
	s.objects[key] = object
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *MemoryStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0)
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
