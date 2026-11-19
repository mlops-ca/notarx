package tokenissuer

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// ErrUniqueState reports a second active or next key for one algorithm, the rule the
// partial unique indexes enforce in PostgreSQL.
var ErrUniqueState = errors.New("tokenissuer: at most one active and one next key per algorithm")

// ErrKeyNotFound reports an unknown kid.
var ErrKeyNotFound = errors.New("tokenissuer: signing key not found")

// MemoryStore is an in-process Store with the same semantics as the PostgreSQL one: a
// single try-lock, transactional writes and the partial unique rule. It backs tests and
// single-process tools.
type MemoryStore struct {
	lock sync.Mutex // the rotation lock
	mu   sync.RWMutex
	rows map[string]KeyRecord
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{rows: map[string]KeyRecord{}} }

// ListKeys implements KeyReader.
func (s *MemoryStore) ListKeys(context.Context) ([]KeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshot(s.rows), nil
}

// ListPublicKeys implements PublicKeyReader.
func (s *MemoryStore) ListPublicKeys(ctx context.Context) ([]KeyRecord, error) {
	records, err := s.ListKeys(ctx)
	for i := range records {
		records[i].PrivateSealed = ""
	}
	return records, err
}

// Put overwrites a row without any check; tests use it to plant tampered rows.
func (s *MemoryStore) Put(record KeyRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[record.KID] = record
}

func snapshot(rows map[string]KeyRecord) []KeyRecord {
	out := make([]KeyRecord, 0, len(rows))
	for _, record := range rows {
		record.PublicJWK = slices.Clone(record.PublicJWK)
		out = append(out, record)
	}
	sortRecords(out)
	return out
}

// WithRotationLock implements Store.
func (s *MemoryStore) WithRotationLock(ctx context.Context, fn func(context.Context, KeyTx) error) (bool, error) {
	if !s.lock.TryLock() {
		return false, nil
	}
	defer s.lock.Unlock()
	s.mu.RLock()
	work := map[string]KeyRecord{}
	for kid, record := range s.rows {
		work[kid] = record
	}
	s.mu.RUnlock()
	if err := fn(ctx, &memoryTx{rows: work}); err != nil {
		return true, err
	}
	s.mu.Lock()
	s.rows = work
	s.mu.Unlock()
	return true, nil
}

type memoryTx struct{ rows map[string]KeyRecord }

func (t *memoryTx) List(context.Context) ([]KeyRecord, error) { return snapshot(t.rows), nil }

func (t *memoryTx) conflicts(record KeyRecord) bool {
	if record.State != StateActive && record.State != StateNext {
		return false
	}
	for kid, other := range t.rows {
		if kid != record.KID && other.Alg == record.Alg && other.State == record.State {
			return true
		}
	}
	return false
}

func (t *memoryTx) Insert(_ context.Context, record KeyRecord) error {
	if _, exists := t.rows[record.KID]; exists || t.conflicts(record) {
		return ErrUniqueState
	}
	t.rows[record.KID] = record
	return nil
}

func (t *memoryTx) Update(_ context.Context, record KeyRecord) error {
	existing, ok := t.rows[record.KID]
	if !ok {
		return ErrKeyNotFound
	}
	if t.conflicts(record) {
		return ErrUniqueState
	}
	existing.State, existing.IntegrityMAC = record.State, record.IntegrityMAC
	existing.PublishedAt, existing.ActivatedAt = record.PublishedAt, record.ActivatedAt
	existing.RetiredAt, existing.ExpiresAt = record.RetiredAt, record.ExpiresAt
	t.rows[record.KID] = existing
	return nil
}

func (t *memoryTx) Delete(_ context.Context, kid string) error {
	if _, ok := t.rows[kid]; !ok {
		return ErrKeyNotFound
	}
	delete(t.rows, kid)
	return nil
}
