package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	dialectmysql "github.com/mlops-ca/notarx/internal/dialect/mysql"
)

// MySQLCoordinator is the MySQL twin of PostgresCoordinator (ADR 0022). A cache publication holds a
// GET_LOCK session lock on a dedicated connection, so a lost connection releases it like a PostgreSQL
// advisory lock does; before a connection goes back to the pool its locks are released explicitly,
// because COMMIT does not release them. Circuit state lives in cache_circuit_breakers.
type MySQLCoordinator struct {
	db          *sql.DB
	stateDB     *sql.DB
	ownsDB      bool
	ownsStateDB bool
	locker      dialectmysql.Locker
	leases      *leaseTable
	now         func() time.Time
}

// NewMySQLCoordinator opens the lock and state pools from a mysql:// URL.
func NewMySQLCoordinator(databaseURL string) (*MySQLCoordinator, error) {
	db, err := database.OpenMySQL(databaseURL, database.DefaultCoordinatorPoolConfig())
	if err != nil {
		return nil, fmt.Errorf("open MySQL cache coordinator: %w", err)
	}
	stateDB, err := database.OpenMySQL(databaseURL, database.DefaultPoolConfig())
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open MySQL cache coordinator state: %w", err)
	}
	return &MySQLCoordinator{db: db, stateDB: stateDB, ownsDB: true, ownsStateDB: true, leases: newLeaseTable(), now: time.Now}, nil
}

// NewMySQLCoordinatorWithPools builds the coordinator on pools the caller owns.
func NewMySQLCoordinatorWithPools(lockDB, stateDB *sql.DB) (*MySQLCoordinator, error) {
	if lockDB == nil || stateDB == nil {
		return nil, errors.New("MySQL cache coordinator requires lock and state database pools")
	}
	return &MySQLCoordinator{db: lockDB, stateDB: stateDB, leases: newLeaseTable(), now: time.Now}, nil
}

func (c *MySQLCoordinator) Acquire(ctx context.Context, key string, _ time.Duration) (string, bool, error) {
	owner, err := newOwner()
	if err != nil {
		return "", false, err
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return "", false, err
	}
	lockKey := "notarx:cache:" + key
	locked, err := c.locker.LockSession(ctx, conn, lockKey, 0)
	if err != nil || !locked {
		_ = conn.Close()
		return owner, false, err
	}
	c.leases.add(owner, sessionLease{key: lockKey, conn: conn})
	return owner, true, nil
}

// Renew pings the dedicated connection: a lost connection lost the lock.
func (c *MySQLCoordinator) Renew(ctx context.Context, _ string, owner string, _ time.Duration) (bool, error) {
	return c.leases.renew(ctx, owner)
}

func (c *MySQLCoordinator) Release(ctx context.Context, _ string, owner string) error {
	lease, exists := c.leases.take(owner)
	if !exists {
		return nil
	}
	return c.releaseLease(ctx, lease)
}

// CircuitOpen reports whether an unexpired breaker exists for key.
func (c *MySQLCoordinator) CircuitOpen(ctx context.Context, key string) (bool, error) {
	var open bool
	err := c.stateDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM cache_circuit_breakers WHERE `key` = ? AND expires_at > ?)", key, c.now().UTC()).Scan(&open)
	return open, err
}

// OpenCircuit opens or extends the breaker. The time comes from the Go clock: NOW(6) is the statement
// start on MySQL, and the stores do not rely on it across statements (ADR 0022).
func (c *MySQLCoordinator) OpenCircuit(ctx context.Context, key string, ttl time.Duration) error {
	now := c.now().UTC()
	_, err := c.stateDB.ExecContext(ctx, "INSERT INTO cache_circuit_breakers (`key`, expires_at, updated_at) VALUES (?, ?, ?) AS new ON DUPLICATE KEY UPDATE expires_at = new.expires_at, updated_at = new.updated_at", key, now.Add(ttl), now)
	return err
}

func (c *MySQLCoordinator) CloseCircuit(ctx context.Context, key string) error {
	_, err := c.stateDB.ExecContext(ctx, "DELETE FROM cache_circuit_breakers WHERE `key` = ?", key)
	return err
}

func (c *MySQLCoordinator) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var closeErr error
	for _, lease := range c.leases.drain() {
		closeErr = joinErrors(closeErr, c.releaseLease(ctx, lease))
	}
	if c.ownsDB {
		closeErr = joinErrors(closeErr, c.db.Close())
	}
	if c.ownsStateDB {
		closeErr = joinErrors(closeErr, c.stateDB.Close())
	}
	return closeErr
}

// releaseLease unlocks, drops every session lock (COMMIT does not release them) and returns the connection to the pool.
func (c *MySQLCoordinator) releaseLease(ctx context.Context, lease sessionLease) error {
	err := c.locker.UnlockSession(ctx, lease.conn, lease.key)
	if err == nil {
		err = c.locker.ReleaseAll(ctx, lease.conn)
	}
	return closeLease(lease.conn, err)
}
