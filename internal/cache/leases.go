package cache

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
)

// sessionLease is a lock held on a dedicated connection until it is released: the lock belongs to the
// database session, so a lost connection loses the lock and a held connection keeps it.
type sessionLease struct {
	key  string
	conn *sql.Conn
}

// leaseTable holds the leases of one coordinator by owner; both coordinators (PostgreSQL advisory
// locks, MySQL GET_LOCK) keep their bookkeeping here and differ only in the lock calls.
type leaseTable struct {
	mu     sync.Mutex
	leases map[string]sessionLease
}

func newLeaseTable() *leaseTable { return &leaseTable{leases: map[string]sessionLease{}} }

func (t *leaseTable) add(owner string, lease sessionLease) {
	t.mu.Lock()
	t.leases[owner] = lease
	t.mu.Unlock()
}

// take removes and returns the lease of owner.
func (t *leaseTable) take(owner string) (sessionLease, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, exists := t.leases[owner]
	delete(t.leases, owner)
	return lease, exists
}

// drain removes and returns every lease.
func (t *leaseTable) drain() []sessionLease {
	t.mu.Lock()
	defer t.mu.Unlock()
	leases := make([]sessionLease, 0, len(t.leases))
	for _, lease := range t.leases {
		leases = append(leases, lease)
	}
	t.leases = map[string]sessionLease{}
	return leases
}

// renew pings the dedicated connection of owner: a lost connection lost the lock.
func (t *leaseTable) renew(ctx context.Context, owner string) (bool, error) {
	t.mu.Lock()
	lease, exists := t.leases[owner]
	t.mu.Unlock()
	if !exists {
		return false, nil
	}
	if err := lease.conn.PingContext(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// closeLease returns the connection of a lease to the pool after its unlock. A connection whose unlock
// failed cannot prove it let go of its lock, so it is discarded instead, which releases the lock
// server-side; that already closed it, so the closing error is not an error.
func closeLease(conn *sql.Conn, unlockErr error) error {
	if unlockErr != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	closeErr := conn.Close()
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return joinErrors(unlockErr, closeErr)
}
