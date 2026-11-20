package tokenissuer

import "context"

// KeyTx is one transaction that holds the issuer rotation lock. List re-reads every row
// with FOR UPDATE semantics.
type KeyTx interface {
	List(ctx context.Context) ([]KeyRecord, error)
	Insert(ctx context.Context, record KeyRecord) error
	// Update rewrites state, timestamps and integrity_mac of the row with record.KID.
	Update(ctx context.Context, record KeyRecord) error
	Delete(ctx context.Context, kid string) error
	// Audit writes the audit event of the change in the same transaction, so a change whose audit cannot be written
	// does not happen (ADR 0023 section 1).
	Audit(ctx context.Context, event AuditEvent) error
}

// AuditEvent is the audit record of an operator-driven key change. It never carries key material.
type AuditEvent struct {
	Actor     string
	Operation string
	Evidence  map[string]string
}

// PublicKeyReader lists key rows without their sealed private halves, for the anonymous
// JWKS path that must never load private material.
type PublicKeyReader interface {
	// ListPublicKeys returns every row with PrivateSealed left empty.
	ListPublicKeys(ctx context.Context) ([]KeyRecord, error)
}

// Store persists signing keys. The PostgreSQL implementation lives in internal/repository.
type Store interface {
	KeyReader
	PublicKeyReader
	// WithRotationLock runs fn in one transaction while holding the advisory lock
	// pg_try_advisory_xact_lock(hashtextextended('notarx:issuer-rotation', 0)). It returns
	// acquired=false without running fn when another node holds the lock. fn's error rolls the
	// transaction back.
	WithRotationLock(ctx context.Context, fn func(ctx context.Context, tx KeyTx) error) (acquired bool, err error)
}
