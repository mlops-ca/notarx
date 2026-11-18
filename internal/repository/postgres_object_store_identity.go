package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ObjectStoreIdentity is the row of object_store_identity: the driver and the
// root ID of the single-node object store the database is bound to.
type ObjectStoreIdentity struct {
	Driver string
	RootID string
}

// ClaimObjectStoreIdentity records identity when no row exists and returns the
// row that is stored afterwards, which is identity itself on first boot and the
// earlier owner's otherwise. The caller compares the two: any difference means
// a second node, a restored volume or a wrong mount. Concurrent first boots
// converge on the first writer.
func (s *PostgresStore) ClaimObjectStoreIdentity(ctx context.Context, identity ObjectStoreIdentity) (ObjectStoreIdentity, error) {
	if identity.Driver == "" || identity.RootID == "" {
		return ObjectStoreIdentity{}, errors.New("object store identity requires a driver and a root ID")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO object_store_identity (singleton, driver, root_id) VALUES (true, $1, $2) ON CONFLICT (singleton) DO NOTHING`,
		identity.Driver, identity.RootID); err != nil {
		return ObjectStoreIdentity{}, err
	}
	var stored ObjectStoreIdentity
	err := s.db.QueryRowContext(ctx, `SELECT driver, root_id FROM object_store_identity WHERE singleton`).Scan(&stored.Driver, &stored.RootID)
	return stored, err
}

// ErrObjectStoreRootLockLost is returned by Alive when the session no longer
// holds the lock, which is definitive: it never comes back on its own.
var ErrObjectStoreRootLockLost = errors.New("object store root lock is no longer held")

// ObjectStoreRootLock is a session-level advisory lock on one object store
// root ID. It lives on a dedicated connection for as long as the gateway runs,
// so two processes cannot share one root.
type ObjectStoreRootLock struct {
	conn   *sql.Conn
	rootID string
}

const objectStoreRootLockKey = `'notarx-object-store-root:' || $1::text`

// LockObjectStoreRoot tries to take the root's lock without waiting. It
// returns acquired=false when another session holds it.
func (s *PostgresStore) LockObjectStoreRoot(ctx context.Context, rootID string) (lock *ObjectStoreRootLock, acquired bool, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended(`+objectStoreRootLockKey+`, 0))`, rootID).Scan(&acquired); err != nil || !acquired {
		_ = conn.Close()
		return nil, false, err
	}
	return &ObjectStoreRootLock{conn: conn, rootID: rootID}, true, nil
}

// Alive reports whether the connection still holds the lock. A dropped
// connection releases its session locks, so a failure here means the root is
// no longer protected.
func (l *ObjectStoreRootLock) Alive(ctx context.Context) error {
	var held bool
	err := l.conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_locks
		WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND objsubid = 1
		  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended(`+objectStoreRootLockKey+`, 0))`, l.rootID).Scan(&held)
	if err != nil {
		return err
	}
	if !held {
		return ErrObjectStoreRootLockLost
	}
	return nil
}

// Release drops the lock and returns the connection to the pool.
func (l *ObjectStoreRootLock) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtextextended(`+objectStoreRootLockKey+`, 0))`, l.rootID)
	_ = l.conn.Close()
}
