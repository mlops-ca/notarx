//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/dialect/dialecttest"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// TestPostgresDialectContract runs the shared dialect contract (internal/dialect/dialecttest), the
// same suite internal/dialect/mysql runs against MySQL.
func TestPostgresDialectContract(t *testing.T) {
	driver, url := testsupport.Database(t)
	if driver != testsupport.DriverPostgres {
		t.Skip("the PostgreSQL dialect suites run with TEST_DATABASE_DRIVER=postgres")
	}
	db, err := database.OpenPostgres(url, database.PoolConfig{MaxOpenConns: 40, MaxIdleConns: 10, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dialecttest.Run(t, dialecttest.Harness{
		Set:    New(),
		DB:     db,
		Rebind: dialecttest.PostgresRebind,
		Schema: []string{
			"CREATE TABLE dialect_contract_counter (id INT NOT NULL PRIMARY KEY, n INT NOT NULL)",
			"CREATE TABLE dialect_contract_queue (id BIGINT NOT NULL PRIMARY KEY, state VARCHAR(16) NOT NULL, lease_until TIMESTAMPTZ NULL)",
			"CREATE TABLE dialect_contract_parent (id INT NOT NULL PRIMARY KEY)",
			"CREATE TABLE dialect_contract_child (id INT NOT NULL PRIMARY KEY, parent_id INT NOT NULL REFERENCES dialect_contract_parent (id))",
		},
		ConnectionID: func(ctx context.Context, conn *sql.Conn) (int64, error) {
			var id int64
			err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&id)
			return id, err
		},
		Terminate: func(ctx context.Context, db *sql.DB, id int64) error {
			var ok bool
			if err := db.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", id).Scan(&ok); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond) // the backend needs a moment to release its locks
			return nil
		},
		LockTimeout: func(ctx context.Context, conn *sql.Conn, on bool) error {
			statement := "RESET lock_timeout"
			if on {
				statement = "SET lock_timeout = '1s'"
			}
			_, err := conn.ExecContext(ctx, statement)
			return err
		},
		Notify: func(ctx context.Context, channel string) error {
			_, err := db.ExecContext(ctx, "SELECT pg_notify($1, '')", channel)
			return err
		},
		LatencyBound: 2 * time.Second,
	})
}
