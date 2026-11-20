package cache

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
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
	mu          sync.Mutex
	leases      map[string]mysqlLease
	now         func() time.Time
}

type mysqlLease struct {
	key  string
	conn *sql.Conn
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
	return &MySQLCoordinator{db: db, stateDB: stateDB, ownsDB: true, ownsStateDB: true, leases: make(map[string]mysqlLease), now: time.Now}, nil
}

// NewMySQLCoordinatorWithPools builds the coordinator on pools the caller owns.
func NewMySQLCoordinatorWithPools(lockDB, stateDB *sql.DB) (*MySQLCoordinator, error) {
	if lockDB == nil || stateDB == nil {
		return nil, errors.New("MySQL cache coordinator requires lock and state database pools")
	}
	return &MySQLCoordinator{db: lockDB, stateDB: stateDB, leases: make(map[string]mysqlLease), now: time.Now}, nil
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
	c.mu.Lock()
	c.leases[owner] = mysqlLease{key: lockKey, conn: conn}
	c.mu.Unlock()
	return owner, true, nil
}

// Renew pings the dedicated connection: a lost connection lost the lock.
func (c *MySQLCoordinator) Renew(ctx context.Context, _ string, owner string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	lease, exists := c.leases[owner]
	c.mu.Unlock()
	if !exists {
		return false, nil
	}
	if err := lease.conn.PingContext(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (c *MySQLCoordinator) Release(ctx context.Context, _ string, owner string) error {
	c.mu.Lock()
	lease, exists := c.leases[owner]
	delete(c.leases, owner)
	c.mu.Unlock()
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
	c.mu.Lock()
	leases := c.leases
	c.leases = make(map[string]mysqlLease)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var closeErr error
	for _, lease := range leases {
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

// releaseLease unlocks and returns the connection to the pool; a connection that cannot prove it let go
// of its lock is discarded instead, which releases the lock server-side.
func (c *MySQLCoordinator) releaseLease(ctx context.Context, lease mysqlLease) error {
	err := c.locker.UnlockSession(ctx, lease.conn, lease.key)
	if err == nil {
		err = c.locker.ReleaseAll(ctx, lease.conn)
	}
	if err != nil {
		_ = lease.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return joinErrors(err, lease.conn.Close())
}
