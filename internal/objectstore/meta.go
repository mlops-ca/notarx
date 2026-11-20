package objectstore

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// MetaPrefix is the object-store namespace of metadata payloads kept out of the database (ADR 0023
// section 9). Objects under it are content-addressed and immutable: the key is the
// SHA-256 digest of the bytes, so a write can never change what a key means.
const MetaPrefix = "meta/sha256/"

// DefaultMetaCacheBytes bounds the in-process LRU of a MetaStore.
const DefaultMetaCacheBytes = 16 << 20

// IsMetaObjectKey reports whether key is exactly meta/sha256/<64 lowercase hex characters>,
// the only form the namespace accepts.
func IsMetaObjectKey(key string) bool {
	hexPart, ok := strings.CutPrefix(key, MetaPrefix)
	if !ok || len(hexPart) != sha256.Size*2 {
		return false
	}
	for _, c := range hexPart {
		if !isLowerHex(c) {
			return false
		}
	}
	return true
}

func isLowerHex(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

// MetaKey returns the object key of a "sha256:<hex>" digest.
func MetaKey(digest string) (string, error) {
	hexPart, err := parseDigest(digest)
	if err != nil {
		return "", err
	}
	return MetaPrefix + hexPart, nil
}

// MetaStore reads and writes the meta/sha256/<hex> namespace over any Store driver.
// Reads re-verify the digest, so a corrupted or substituted object is rejected, and
// are served through a small in-process LRU bounded by bytes.
type MetaStore struct {
	store Store

	mu       sync.Mutex
	maxBytes int64
	bytes    int64
	order    *list.List // front is the most recently used
	entries  map[string]*list.Element
	// deletes counts Delete calls. A Get that started before one never caches what it read,
	// so a delete cannot be undone by a read that raced it.
	deletes uint64
}

type metaEntry struct {
	digest string
	data   []byte
}

// NewMetaStore wraps store. cacheBytes bounds the LRU; zero or less uses
// DefaultMetaCacheBytes. A payload larger than the bound is never cached.
func NewMetaStore(store Store, cacheBytes int64) *MetaStore {
	if cacheBytes <= 0 {
		cacheBytes = DefaultMetaCacheBytes
	}
	return &MetaStore{store: store, maxBytes: cacheBytes, order: list.New(), entries: map[string]*list.Element{}}
}

// Put stores data and returns its digest ("sha256:<hex>") and object key. Writing the
// same bytes again is a no-op in effect, since the key is derived from the bytes.
func (m *MetaStore) Put(ctx context.Context, data []byte) (digest, key string, err error) {
	sum := sha256.Sum256(data)
	digest = digestPrefix + hex.EncodeToString(sum[:])
	key = MetaPrefix + hex.EncodeToString(sum[:])
	epoch := m.epoch()
	if err := m.store.PutVerifiedReader(ctx, key, bytes.NewReader(data), int64(len(data)), digest); err != nil {
		return "", "", fmt.Errorf("store meta object: %w", err)
	}
	m.remember(digest, data, epoch)
	return digest, key, nil
}

// normalize returns the canonical "sha256:<lowercase hex>" form of digest, so one object has
// one cache key however the caller spelled the digest.
func normalize(digest string) (string, error) {
	hexPart, err := parseDigest(digest)
	if err != nil {
		return "", err
	}
	return digestPrefix + hexPart, nil
}

// Get returns the bytes stored under digest. It returns ErrNotFound for an absent
// object and ErrDigestMismatch when the stored bytes do not hash to digest.
func (m *MetaStore) Get(ctx context.Context, digest string) ([]byte, error) {
	digest, err := normalize(digest)
	if err != nil {
		return nil, err
	}
	if data, ok := m.cached(digest); ok {
		return data, nil
	}
	epoch := m.epoch()
	data, err := m.store.Get(ctx, MetaPrefix+strings.TrimPrefix(digest, digestPrefix))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if digestPrefix+hex.EncodeToString(sum[:]) != digest {
		return nil, ErrDigestMismatch
	}
	m.remember(digest, data, epoch)
	return data, nil
}

// Delete removes the object of digest; it is for the sweep that deletes unreferenced
// meta objects after their grace period.
func (m *MetaStore) Delete(ctx context.Context, digest string) error {
	digest, err := normalize(digest)
	if err != nil {
		return err
	}
	// Bumping the epoch first stops every read that is already in flight from caching what it
	// read; forgetting after the delete drops an entry a read cached before the bump.
	m.mu.Lock()
	m.deletes++
	m.mu.Unlock()
	if err := m.store.Delete(ctx, MetaPrefix+strings.TrimPrefix(digest, digestPrefix)); err != nil {
		return err
	}
	m.forget(digest)
	return nil
}

func (m *MetaStore) epoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deletes
}

func (m *MetaStore) cached(digest string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	element, ok := m.entries[digest]
	if !ok {
		return nil, false
	}
	m.order.MoveToFront(element)
	return bytes.Clone(element.Value.(*metaEntry).data), true
}

func (m *MetaStore) remember(digest string, data []byte, epoch uint64) {
	size := int64(len(data))
	if size > m.maxBytes {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deletes != epoch {
		return
	}
	if element, ok := m.entries[digest]; ok {
		m.order.MoveToFront(element)
		return
	}
	m.entries[digest] = m.order.PushFront(&metaEntry{digest: digest, data: bytes.Clone(data)})
	m.bytes += size
	for m.bytes > m.maxBytes {
		oldest := m.order.Back()
		entry := oldest.Value.(*metaEntry)
		m.order.Remove(oldest)
		delete(m.entries, entry.digest)
		m.bytes -= int64(len(entry.data))
	}
}

func (m *MetaStore) forget(digest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if element, ok := m.entries[digest]; ok {
		m.bytes -= int64(len(element.Value.(*metaEntry).data))
		m.order.Remove(element)
		delete(m.entries, digest)
	}
}
