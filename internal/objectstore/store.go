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
	"strings"
	"sync"
)

// ErrNotFound is returned when a requested object is absent.
var ErrNotFound = errors.New("object not found")

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

type MemoryStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[string][]byte)}
}

func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *MemoryStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), value...)
	return nil
}

func (s *MemoryStore) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s.mu.RLock()
	value, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, 0, ErrNotFound
	}
	// Put always allocates a fresh backing slice. A reader can safely retain
	// this immutable snapshot across replacement/deletion without copying the
	// entire object, including when serving small ranges of a large object.
	return io.NopCloser(bytes.NewReader(value)), int64(len(value)), nil
}

func (s *MemoryStore) Stat(ctx context.Context, key string) (Info, error) {
	value, err := s.Get(ctx, key)
	if err != nil {
		return Info{}, err
	}
	sum := sha256.Sum256(value)
	return Info{Size: int64(len(value)), Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func (s *MemoryStore) OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s.mu.RLock()
	value, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, 0, ErrNotFound
	}
	size := int64(len(value))
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		return nil, 0, errors.New("object range is out of bounds")
	}
	return io.NopCloser(bytes.NewReader(value[offset : offset+length])), size, nil
}

func (s *MemoryStore) PutReader(ctx context.Context, key string, value io.Reader, _ int64) error {
	data, err := io.ReadAll(value)
	if err != nil {
		return err
	}
	return s.Put(ctx, key, data)
}

func (s *MemoryStore) PutVerifiedReader(ctx context.Context, key string, value io.Reader, size int64, _ string) error {
	return s.PutReader(ctx, key, value, size)
}

func (*MemoryStore) SetVerifiedDigest(context.Context, string, string) error { return nil }

func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *MemoryStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0)
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
