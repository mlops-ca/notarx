package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// PayloadStore is the object store namespace that holds payloads kept out of the database rows
// (ADR 0023 section 9): content-addressed, so writing the same bytes twice stores one object.
// objectstore.MetaStore implements it.
type PayloadStore interface {
	// Put stores data and returns its "sha256:<hex>" digest and object key.
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
	settings := s.payloads.Load()
	if settings == nil || len(data) <= settings.inlineMax {
		return data, payloadRef{}, nil
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
