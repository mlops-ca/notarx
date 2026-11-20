//go:build integration

package mysqlbaseline

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/migrate"
	"github.com/mlops-ca/notarx/internal/schemaparity"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

func TestMain(m *testing.M) {
	if err := testsupport.ValidateIsolatedEnvironment(os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

var pool = database.PoolConfig{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute}

// freshBaseline applies the checked-in baseline to an empty database of its own and returns it.
func freshBaseline(t *testing.T) *sql.DB {
	t.Helper()
	driver, adminURL := testsupport.Database(t)
	if driver != testsupport.DriverMySQL {
		t.Skip("the MySQL baseline suites run with TEST_DATABASE_DRIVER=mysql")
	}
	admin, err := database.OpenMySQL(adminURL, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("notarx_baseline_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + name) })
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	db, err := database.OpenMySQL(parsed.String(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files, err := migrate.LoadDir(root("migrations", "mysql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(context.Background(), db, migrate.DialectMySQL, files, migrate.Options{}); err != nil {
		t.Fatalf("the baseline does not apply to an empty mysql:8.4: %v", err)
	}
	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestBaselineAppliesAndSeeds covers the first acceptance criterion of slice 3c: the baseline applies to an
// empty mysql:8.4, seeds the 4096 lock buckets and the singleton rows, and every table of the catalogue exists.
func TestBaselineAppliesAndSeeds(t *testing.T) {
	db := freshBaseline(t)
	if n := count(t, db, "SELECT COUNT(*) FROM notarx_lock_buckets"); n != LockBuckets {
		t.Fatalf("%d lock buckets; want %d", n, LockBuckets)
	}
	if n := count(t, db, "SELECT COUNT(DISTINCT bucket) FROM notarx_lock_buckets WHERE bucket BETWEEN 0 AND 4095"); n != LockBuckets {
		t.Fatalf("%d distinct buckets in 0..4095", n)
	}
	catalogue, err := schemaparity.Load(root("migrations", "catalogue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range catalogue.Seeds {
		if n := count(t, db, "SELECT COUNT(*) FROM `"+name+"`"); n != len(rows) {
			t.Errorf("%s holds %d seed rows; the catalogue has %d", name, n, len(rows))
		}
	}
	if id := ""; db.QueryRow("SELECT value FROM settings WHERE `key` = 'deployment_id'").Scan(&id) != nil || len(id) != 36 {
		t.Errorf("the deployment id seed is %q", id)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'"); n != len(catalogue.Tables)+2 {
		t.Errorf("%d tables; the catalogue has %d, plus the lock buckets and the migration ledger", n, len(catalogue.Tables))
	}
}

// TestBinaryCollationKeepsCaseApart is the bin-collation contract of ADR 0022: Admin and admin are two values,
// no column uses an ai_ci collation, and the explicit case-insensitive identity (users.name_lower) still refuses
// two user names that differ only in case, as the lower(name) index does on PostgreSQL.
func TestBinaryCollationKeepsCaseApart(t *testing.T) {
	db := freshBaseline(t)
	for _, key := range []string{"Admin", "admin"} {
		if _, err := db.Exec("INSERT INTO settings (`key`, value) VALUES (?, 'x')", key); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM settings WHERE `key` IN ('Admin', 'admin')"); n != 2 {
		t.Fatalf("Admin and admin are %d rows; want two", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM settings WHERE `key` = 'admin'"); n != 1 {
		t.Fatalf("a lookup of admin matched %d rows; want one", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND collation_name LIKE '%\\_ai\\_ci'"); n != 0 {
		t.Fatalf("%d columns use an ai_ci collation", n)
	}
	insert := "INSERT INTO users (id, name, role, secret_hash) VALUES (?, ?, 'member', 'x')"
	if _, err := db.Exec(insert, "00000000-0000-7000-8000-000000000001", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "00000000-0000-7000-8000-000000000002", "alice"); err == nil || !strings.Contains(err.Error(), "Duplicate entry") {
		t.Fatalf("a user name that differs only in case was accepted: %v", err)
	}
}
