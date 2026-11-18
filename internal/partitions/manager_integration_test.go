//go:build integration

package partitions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("owned PostgreSQL required")
	}
	if err := testsupport.ValidateIsolatedDatabaseURL(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenPostgres(dsn, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// scratch creates a partitioned table with the audit table's key shape and a
// DEFAULT partition, and drops it (with every partition) afterwards.
func scratch(t *testing.T, db *sql.DB, retention time.Duration) Spec {
	t.Helper()
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Table: "pt_" + hex.EncodeToString(token), Column: "occurred_at", Granularity: Month, Retention: retention}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s (id bigserial, occurred_at timestamptz NOT NULL DEFAULT now(), payload text NOT NULL DEFAULT '', PRIMARY KEY (occurred_at, id)) PARTITION BY RANGE (occurred_at)`, spec.Table),
		fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s DEFAULT`, spec.DefaultName(), spec.Table),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS `+spec.Table)
	})
	return spec
}

func newManager(t *testing.T, db *sql.DB, owner string, now time.Time, spec ...Spec) *Manager {
	t.Helper()
	m, err := New(db, spec, Options{Owner: owner, Now: func() time.Time { return now }, LockTimeout: 200 * time.Millisecond, Attempts: 2, Backoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func partitionNames(t *testing.T, m *Manager, spec Spec) []string {
	t.Helper()
	children, err := m.children(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range children {
		names = append(names, c.name)
	}
	return names
}

func TestManagerCreatesFuturePartitionsIdempotently(t *testing.T) {
	db := openDB(t)
	spec := scratch(t, db, 0)
	now := time.Date(2031, 3, 15, 10, 0, 0, 0, time.UTC)
	m := newManager(t, db, "node-a", now, spec)
	first, err := m.Maintain(t.Context())
	if err != nil || !first.Leader || len(first.Created) != 4 {
		t.Fatalf("first run = %+v, %v", first, err)
	}
	want := []string{spec.Name(time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC)), spec.Name(time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)), spec.Name(time.Date(2031, 5, 1, 0, 0, 0, 0, time.UTC)), spec.Name(time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC))}
	if got := partitionNames(t, m, spec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("partitions = %v, want %v", got, want)
	}
	second, err := m.Maintain(t.Context())
	if err != nil || len(second.Created) != 0 || len(second.Dropped) != 0 {
		t.Fatalf("second run must be a no-op: %+v, %v", second, err)
	}
	stats := m.Stats()
	if len(stats) != 1 || stats[0].DefaultRows != 0 || stats[0].Partitions != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	// A row in the current month routes to its partition, not the default one.
	if _, err = db.ExecContext(t.Context(), `INSERT INTO `+spec.Table+`(occurred_at) VALUES ($1)`, now); err != nil {
		t.Fatal(err)
	}
	var inPartition int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+want[0]).Scan(&inPartition); err != nil || inPartition != 1 {
		t.Fatalf("row in partition = %d, %v", inPartition, err)
	}
}

func TestManagerDropsOnlyExpiredPartitionsOfBothRetentions(t *testing.T) {
	db := openDB(t)
	// 90 days stands for resolver_audit_log and 730 days for security_audit_log.
	short := scratch(t, db, 90*24*time.Hour)
	long := scratch(t, db, 730*24*time.Hour)
	now := time.Date(2031, 6, 15, 0, 0, 0, 0, time.UTC)
	// Thirteen monthly partitions: 2030-06 to 2031-06.
	for _, spec := range []Spec{short, long} {
		for i := 0; i < 13; i++ {
			start := time.Date(2030, 6+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
			end := spec.Granularity.Next(start)
			statement := fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`, spec.Name(start), spec.Table, boundLiteral(start), boundLiteral(end))
			if _, err := db.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	m := newManager(t, db, "node-a", now, short, long)
	report, err := m.Maintain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Cutoff for 90 days is 2031-03-17: partitions ending on or before it go, that
	// is 2030-06 to 2031-02 (nine). The 730-day table drops nothing.
	if len(report.Dropped) != 9 {
		t.Fatalf("dropped = %v", report.Dropped)
	}
	for _, name := range report.Dropped {
		if !strings.HasPrefix(name, short.Table+"_p") {
			t.Fatalf("dropped %s from the wrong table", name)
		}
	}
	remaining := partitionNames(t, m, short)
	if remaining[0] != short.Name(time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("March partition (ends after the cutoff) must be kept: %v", remaining)
	}
	if got := len(partitionNames(t, m, long)); got != 13+3 {
		// 13 existing plus the manager's own future periods (2031-07 to 2031-09).
		t.Fatalf("730-day table has %d partitions, want 16", got)
	}
}

func TestManagerMovesDefaultPartitionRowsIntoNewPartition(t *testing.T) {
	db := openDB(t)
	spec := scratch(t, db, 0)
	now := time.Date(2032, 1, 10, 0, 0, 0, 0, time.UTC)
	insert := `INSERT INTO ` + spec.Table + `(occurred_at,payload) VALUES ($1,$2)`
	for i, at := range []time.Time{now.Add(time.Hour), now.Add(48 * time.Hour), time.Date(2032, 2, 3, 0, 0, 0, 0, time.UTC), time.Date(2031, 12, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := db.ExecContext(t.Context(), insert, at, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var inDefault int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+spec.DefaultName()).Scan(&inDefault); err != nil || inDefault != 4 {
		t.Fatalf("default rows before = %d, %v", inDefault, err)
	}
	m := newManager(t, db, "node-a", now, spec)
	report, err := m.Maintain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Moved[spec.Name(time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC))] != 2 || report.Moved[spec.Name(time.Date(2032, 2, 1, 0, 0, 0, 0, time.UTC))] != 1 {
		t.Fatalf("moved = %v", report.Moved)
	}
	var total, remaining int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+spec.Table).Scan(&total); err != nil || total != 4 {
		t.Fatalf("rows lost: total = %d, %v", total, err)
	}
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+spec.DefaultName()).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("only the row before the first period stays in the default partition, got %d, %v", remaining, err)
	}
	if stats := m.Stats(); stats[0].DefaultRows != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestManagerLeaseAllowsOneRunnerAndSurvivesTwoNodes(t *testing.T) {
	db := openDB(t)
	spec := scratch(t, db, 0)
	now := time.Date(2033, 5, 5, 0, 0, 0, 0, time.UTC)
	a := newManager(t, db, "node-a", now, spec)
	b := newManager(t, db, "node-b", now, spec)

	// While another node holds an unexpired lease, this node does nothing.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until) VALUES ($1::uuid,$2,'','partition-manager',21600,false,now(),'node-held',now()+interval '1 hour') ON CONFLICT (id) DO UPDATE SET lease_owner='node-held',lease_until=now()+interval '1 hour'`, leaseTaskID, leaseTaskName); err != nil {
		t.Fatal(err)
	}
	report, err := a.Maintain(t.Context())
	if err != nil || report.Leader || len(report.Created) != 0 {
		t.Fatalf("a must not run while node-held has the lease: %+v, %v", report, err)
	}
	if got := partitionNames(t, a, spec); len(got) != 0 {
		t.Fatalf("partitions created without the lease: %v", got)
	}
	// An expired lease is taken over.
	if _, err = db.ExecContext(t.Context(), `UPDATE scheduled_tasks SET lease_until=now()-interval '1 second' WHERE id=$1::uuid`, leaseTaskID); err != nil {
		t.Fatal(err)
	}

	// Two nodes racing: both finish without error and the table ends up complete.
	var wg sync.WaitGroup
	results := make([]Report, 2)
	errs := make([]error, 2)
	for i, m := range []*Manager{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = m.Maintain(t.Context())
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("node %d: %v", i, errs[i])
		}
	}
	if !results[0].Leader && !results[1].Leader {
		t.Fatal("neither node ran")
	}
	if got := partitionNames(t, a, spec); len(got) != 4 {
		t.Fatalf("partitions = %v", got)
	}
	var owner string
	var until sql.NullTime
	if err = db.QueryRowContext(t.Context(), `SELECT lease_owner,lease_until FROM scheduled_tasks WHERE id=$1::uuid`, leaseTaskID).Scan(&owner, &until); err != nil || owner != "" || until.Valid {
		t.Fatalf("lease must be released after a run: %q %v %v", owner, until, err)
	}
}

func TestManagerDropBacksOffOnLockTimeoutThenSucceeds(t *testing.T) {
	db := openDB(t)
	spec := scratch(t, db, 24*time.Hour)
	old := time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC)
	name := spec.Name(old)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`, name, spec.Table, boundLiteral(old), boundLiteral(spec.Granularity.Next(old)))); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2034, 6, 1, 0, 0, 0, 0, time.UTC)
	m := newManager(t, db, "node-a", now, spec)

	holder, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = holder.ExecContext(t.Context(), `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err = holder.ExecContext(t.Context(), `LOCK TABLE `+name+` IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	report, err := m.Maintain(t.Context())
	if err == nil || !strings.Contains(err.Error(), "drop "+name) || len(report.Dropped) != 0 {
		t.Fatalf("a held lock must fail the drop within lock_timeout: %+v, %v", report, err)
	}
	if _, err = holder.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	_ = holder.Close()
	report, err = m.Maintain(t.Context())
	if err != nil || len(report.Dropped) != 1 || report.Dropped[0] != name {
		t.Fatalf("drop after the lock is released: %+v, %v", report, err)
	}
}
