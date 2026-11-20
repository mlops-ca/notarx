package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// PayloadStore is the object store namespace that holds payloads kept out of the database rows
// (ADR 0023 section 9): content-addressed, so writing the same bytes twice stores one object.
// objectstore.MetaStore implements it.
type PayloadStore interface {
	// Put stores data and returns its "sha256:<hex>" digest (of data) and object key.
	Put(ctx context.Context, data []byte) (digest, key string, err error)
	// Get returns the bytes stored under digest, verified against it.
	Get(ctx context.Context, digest string) ([]byte, error)
}

// ErrPayloadStoreUnavailable reports a row whose payload lives in the object store while the
// store has no payload store configured.
var ErrPayloadStoreUnavailable = errors.New("payload store is not configured")

// ErrPayloadMismatch reports an externalised payload whose size differs from the row's body_size.
var ErrPayloadMismatch = errors.New("externalised payload does not match its row")

type payloadSettings struct {
	store     PayloadStore
	inlineMax int
}

// MaxInlineBytes is the largest inline payload a row accepts whatever NOTARX_INLINE_MAX_BYTES says: the inline
// column of a cached Helm source document is CHECK-limited to 1 MiB (migration 000001), the smallest limit of any
// payload kind, so a larger setting would let a payload stay inline that the database then rejects.
const MaxInlineBytes = 1 << 20

// SetPayloadStore moves every payload larger than inlineMaxBytes (at most MaxInlineBytes) out of the database
// rows into store. A nil store keeps every payload inline; rows that already reference an object still need a
// store to be read.
func (s *PostgresStore) SetPayloadStore(store PayloadStore, inlineMaxBytes int) {
	if store == nil {
		s.payloads.Store(nil)
		return
	}
	s.payloads.Store(&payloadSettings{store: store, inlineMax: min(inlineMaxBytes, MaxInlineBytes)})
}

// payloadRef is the nullable body_digest, body_size and body_key triple of a row. The zero
// value is an inline payload.
type payloadRef struct {
	digest sql.NullString
	size   sql.NullInt64
	key    sql.NullString
}

// external reports whether the row keeps its payload in the object store.
func (r payloadRef) external() bool { return r.digest.Valid }

// columns returns the three column arguments in the order body_digest, body_size, body_key.
func (r payloadRef) columns() (any, any, any) {
	if !r.digest.Valid {
		return nil, nil, nil
	}
	return r.digest.String, r.size.Int64, r.key.String
}

// externalize returns what to store in the inline column and the reference columns. A payload
// at or below the threshold, or any payload when no payload store is configured, stays inline.
// The inline value of an externalised payload is empty. The object is written before the row,
// so a rolled-back transaction leaves an unreferenced object for the sweep, never a dangling row.
func (s *PostgresStore) externalize(ctx context.Context, data []byte) ([]byte, payloadRef, error) {
	return s.externalizeIn(ctx, s.db, data)
}

// externalizeIn is externalize for a caller that already holds a transaction: the pin of the object is written
// through q, the connection the caller holds, because a second connection per publish would let concurrent
// publishes exhaust the pool while each waits for the other connection.
func (s *PostgresStore) externalizeIn(ctx context.Context, q sqlRunner, data []byte) ([]byte, payloadRef, error) {
	settings := s.payloads.Load()
	if settings == nil || len(data) <= settings.inlineMax {
		return data, payloadRef{}, nil
	}
	// The pin is committed before the put: the sweep of unreferenced objects (meta_sweep.go) treats an object
	// pinned within its grace period as in use and waits for the pin while it deletes one.
	if err := s.pinMetaObject(ctx, q, data); err != nil {
		return nil, payloadRef{}, err
	}
	digest, key, err := settings.store.Put(ctx, data)
	if err != nil {
		return nil, payloadRef{}, fmt.Errorf("externalise payload: %w", err)
	}
	return []byte{}, payloadRef{
		digest: sql.NullString{String: digest, Valid: true},
		size:   sql.NullInt64{Int64: int64(len(data)), Valid: true},
		key:    sql.NullString{String: key, Valid: true},
	}, nil
}

// externalizeJSON is externalize for a jsonb column: it returns the text to bind to the column,
// which is placeholder (a JSON value that satisfies the column's constraints) when the payload
// went to the object store.
func (s *PostgresStore) externalizeJSON(ctx context.Context, data []byte, placeholder string) (string, payloadRef, error) {
	inline, ref, err := s.externalize(ctx, data)
	if err != nil {
		return "", payloadRef{}, err
	}
	if ref.external() {
		return placeholder, ref, nil
	}
	return string(inline), ref, nil
}

// matches reports whether data is the payload the reference names: the digest is the sha256 of the bytes
// (PayloadStore.Put), so no object read is needed.
func (r payloadRef) matches(data []byte) bool {
	if !r.external() || r.size.Int64 != int64(len(data)) {
		return false
	}
	sum := sha256.Sum256(data)
	return r.digest.String == "sha256:"+hex.EncodeToString(sum[:])
}

// payloadsEqual reports whether two payloads are equal without reading the object store, for the comparison a
// locking transaction must make between a row whose payload is externalised and another payload. A payload scanned
// without resolving holds no bytes (its reference names them): two references are equal when their digests are, and
// a reference equals bytes that hash to its digest.
func payloadsEqual(a []byte, aRef payloadRef, b []byte, bRef payloadRef) bool {
	switch {
	case aRef.external() && bRef.external():
		return aRef.digest.String == bRef.digest.String && aRef.size.Int64 == bRef.size.Int64
	case aRef.external():
		return aRef.matches(b)
	case bRef.external():
		return bRef.matches(a)
	}
	return bytes.Equal(a, b)
}

// stagedPayload is a payload externalised before a transaction opens, so the object store write never runs while
// the transaction holds locks and a pool connection. The zero value stages nothing.
type stagedPayload struct {
	data, inline []byte
	ref          payloadRef
	staged       bool
}

// stagePayload externalises data now (see externalize).
func (s *PostgresStore) stagePayload(ctx context.Context, data []byte) (stagedPayload, error) {
	inline, ref, err := s.externalize(ctx, data)
	if err != nil {
		return stagedPayload{}, err
	}
	return stagedPayload{data: data, inline: inline, ref: ref, staged: true}, nil
}

type stagedPayloadContextKey struct{}

// PayloadStager is implemented by a store that can externalise a payload ahead of its commit (PostgresStore).
type PayloadStager interface {
	// StagePayload externalises data now and returns a context that carries the result, so the commit made with
	// that context (or one derived from it, such as the one a publication advisory lock returns) finds the object
	// already stored and never calls the object store itself.
	StagePayload(ctx context.Context, data []byte) (context.Context, error)
}

// StagePayload stages data on store when it supports staging and returns ctx unchanged otherwise. A publication
// handler calls it before it takes the publication advisory lock, so the lock and its dedicated connection never
// wait on the object store. The staged object is unreferenced until the commit uses it; a commit that is refused
// leaves it for the sweep of unreferenced objects.
func StagePayload(ctx context.Context, store any, data []byte) (context.Context, error) {
	if stager, ok := store.(PayloadStager); ok {
		return stager.StagePayload(ctx, data)
	}
	return ctx, nil
}

// StagePayload implements PayloadStager.
func (s *PostgresStore) StagePayload(ctx context.Context, data []byte) (context.Context, error) {
	staged, err := s.stagePayload(ctx, data)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, stagedPayloadContextKey{}, staged), nil
}

// stagedFor returns the payload staged on ctx by StagePayload when it holds data, and stages data now otherwise.
func (s *PostgresStore) stagedFor(ctx context.Context, data []byte) (stagedPayload, error) {
	if staged, ok := ctx.Value(stagedPayloadContextKey{}).(stagedPayload); ok && staged.staged && bytes.Equal(staged.data, data) {
		return staged, nil
	}
	return s.stagePayload(ctx, data)
}

// externalizeIn returns the staged result when data is the staged payload. When the row changed after the payload
// was staged it externalises data now through q, the transaction's connection (see PostgresStore.externalizeIn),
// the behaviour before staging; the object staged for the old bytes is left for the sweep.
func (p stagedPayload) externalizeIn(ctx context.Context, s *PostgresStore, q sqlRunner, data []byte) ([]byte, payloadRef, error) {
	if p.staged && bytes.Equal(p.data, data) {
		return p.inline, p.ref, nil
	}
	return s.externalizeIn(ctx, q, data)
}

// payloadPrefetch holds payloads read before a transaction opens, by digest, for a transaction that needs the bytes
// of a row it locks.
type payloadPrefetch map[string][]byte

// add keeps data under the digest of ref.
func (p payloadPrefetch) add(ref payloadRef, data []byte) {
	if ref.external() {
		p[ref.digest.String] = data
	}
}

// internalizeWith is internalize that uses a prefetched payload when one matches the reference. A row that changed
// after the prefetch reads its object, the behaviour before prefetching.
func (s *PostgresStore) internalizeWith(ctx context.Context, inline []byte, ref payloadRef, pre payloadPrefetch) ([]byte, error) {
	if data, ok := pre[ref.digest.String]; ok && ref.external() && int64(len(data)) == ref.size.Int64 {
		return data, nil
	}
	return s.internalize(ctx, inline, ref)
}

// internalize returns the payload of a row: inline when ref is empty, otherwise the verified
// object.
func (s *PostgresStore) internalize(ctx context.Context, inline []byte, ref payloadRef) ([]byte, error) {
	if !ref.external() {
		return inline, nil
	}
	settings := s.payloads.Load()
	if settings == nil {
		return nil, ErrPayloadStoreUnavailable
	}
	data, err := settings.store.Get(ctx, ref.digest.String)
	if err != nil {
		return nil, fmt.Errorf("read externalised payload %s: %w", ref.digest.String, err)
	}
	if int64(len(data)) != ref.size.Int64 {
		return nil, ErrPayloadMismatch
	}
	return data, nil
}

// payloadFetchConcurrency bounds the object reads of one list.
const payloadFetchConcurrency = 8

// payloadSlot is one payload a list read still has to fetch: the reference a row carried and where its bytes go.
type payloadSlot struct {
	ref payloadRef
	set func([]byte)
}

// resolvePayloads fetches the externalised payloads of slots. A list read scans its rows first and calls this
// after the rows are closed, so no database cursor or connection waits on the object store, and the reads of
// one list run concurrently, payloadFetchConcurrency at a time. Slots whose payload is inline are skipped.
func (s *PostgresStore) resolvePayloads(ctx context.Context, slots []payloadSlot) error {
	var group errgroup.Group
	group.SetLimit(payloadFetchConcurrency)
	for _, slot := range slots {
		if !slot.ref.external() {
			continue
		}
		group.Go(func() error {
			data, err := s.internalize(ctx, nil, slot.ref)
			if err != nil {
				return err
			}
			slot.set(data)
			return nil
		})
	}
	return group.Wait()
}
