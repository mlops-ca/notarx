//go:build integration

package retention

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
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
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

// scratchSchema returns a one-connection pool whose search_path is a private schema holding
// empty copies (columns, defaults, CHECK constraints and indexes, no foreign keys) of the tables the
// janitor entries and their terminal predicates read, plus scheduled_tasks for the lease.
// The fixtures therefore never touch rows other tests rely on.
func scratchSchema(t *testing.T) *sql.DB {
	t.Helper()
	db := openDB(t)
	db.SetMaxOpenConns(1)
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	schema := "rj_scratch_" + hex.EncodeToString(token)
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	tables := map[string]bool{"scheduled_tasks": true, "replication_plans": true, "repository_quota_alert_events": true, "nexus_migration_batches": true}
	for _, entry := range DefaultRegistry() {
		if !entry.PartitionedOnPostgres && !entry.Optional {
			tables[entry.Table] = true
		}
	}
	for table := range tables {
		statement := fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES)", schema, table, table)
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), "SET search_path = "+schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func noSleep(context.Context, time.Duration) {}

func newJanitor(t *testing.T, db *sql.DB, owner string, cfg Config, entries ...Entry) *Janitor {
	t.Helper()
	j, err := New(db, cfg, Options{Owner: owner, Registry: entries, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func scratchTable(t *testing.T, db *sql.DB) string {
	t.Helper()
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	name := "rj_" + hex.EncodeToString(token)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("CREATE TABLE %s (id bigserial PRIMARY KEY, at timestamptz NOT NULL, state text NOT NULL)", name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+name) })
	return name
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestJanitorDeletesOnlyTerminalExpiredRowsInThrottledBatches(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	insert := func(state string, age time.Duration, n int) {
		if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) SELECT now() - $1 * interval '1 second', $2 FROM generate_series(1, $3)", table), int64(age.Seconds()), state, n); err != nil {
			t.Fatal(err)
		}
	}
	insert("done", 48*time.Hour, 25)
	insert("done", time.Hour, 3)
	insert("running", 48*time.Hour, 4)
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "state = 'done'", KeyColumns: []string{"id"}, Batch: 10, Standard: 24 * time.Hour}
	var sleeps []time.Duration
	j, err := New(db, DefaultConfig(), Options{Owner: "a", Registry: []Entry{entry}, Sleep: func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) }})
	if err != nil {
		t.Fatal(err)
	}
	report, err := j.RunOnce(t.Context())
	if err != nil || !report.Leader {
		t.Fatalf("RunOnce = %+v, %v", report, err)
	}
	if report.Deleted[table] != 25 {
		t.Fatalf("deleted = %d, want 25 (10+10+5 in three batches)", report.Deleted[table])
	}
	if got := count(t, db, "SELECT count(*) FROM "+table+" WHERE state='done'"); got != 3 {
		t.Fatalf("fresh terminal rows = %d, want 3", got)
	}
	if got := count(t, db, "SELECT count(*) FROM "+table+" WHERE state='running'"); got != 4 {
		t.Fatalf("non-terminal rows = %d, want 4 (never deleted)", got)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v, want one after each full batch", sleeps)
	}
	for _, d := range sleeps {
		if d < 100*time.Millisecond {
			t.Errorf("sleep %v is below the 100ms floor", d)
		}
	}
	if stats := j.Stats(); len(stats) != 1 || stats[0].Deleted != 25 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestJanitorSkipLockedLeavesLockedRows(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) SELECT now() - interval '3 days', 'done' FROM generate_series(1, 6)", table)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), fmt.Sprintf("SELECT id FROM %s ORDER BY id LIMIT 2 FOR UPDATE", table)); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Batch: 100, Standard: 24 * time.Hour}
	started := time.Now()
	report, err := newJanitor(t, db, "a", DefaultConfig(), entry).RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("the janitor waited on locked rows instead of skipping them")
	}
	if report.Deleted[table] != 4 {
		t.Fatalf("deleted = %d, want the 4 unlocked rows", report.Deleted[table])
	}
}

func TestJanitorOffOverrideAndPartitionedSkip(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) VALUES (now() - interval '9 days', 'done')", table)); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: day}
	cfg := DefaultConfig()
	cfg.Overrides[table] = Override{Off: true}
	report, err := newJanitor(t, db, "a", cfg, entry).RunOnce(t.Context())
	if err != nil || report.Skipped[table] != "off" || count(t, db, "SELECT count(*) FROM "+table) != 1 {
		t.Fatalf("off override: report = %+v, err = %v", report, err)
	}
	partitioned := entry
	partitioned.PartitionedOnPostgres = true
	report, err = newJanitor(t, db, "a", DefaultConfig(), partitioned).RunOnce(t.Context())
	if err != nil || report.Skipped[table] != "partitioned" || count(t, db, "SELECT count(*) FROM "+table) != 1 {
		t.Fatalf("partitioned entry: report = %+v, err = %v", report, err)
	}
	optional := Entry{Table: "rj_missing_table", TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Optional: true}
	report, err = newJanitor(t, db, "a", DefaultConfig(), optional).RunOnce(t.Context())
	if err != nil || report.Skipped["rj_missing_table"] != "table missing" {
		t.Fatalf("optional entry: report = %+v, err = %v", report, err)
	}
	cfg.Overrides[table] = Override{Keep: time.Hour}
	if _, err := newJanitor(t, db, "a", cfg, entry).RunOnce(t.Context()); err != nil || count(t, db, "SELECT count(*) FROM "+table) != 0 {
		t.Fatalf("a keep override should apply: %v", err)
	}
}

func TestJanitorRunBudgetStopsAndDrainContinues(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) SELECT now() - interval '3 days', 'done' FROM generate_series(1, 30)", table)); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Batch: 5, Standard: day}
	cfg := DefaultConfig()
	cfg.RunBudget = time.Millisecond
	clock := time.Now()
	j, err := New(db, cfg, Options{Owner: "a", Registry: []Entry{entry}, Now: func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := j.RunOnce(t.Context()); err != nil || report.Deleted[table] != 5 {
		t.Fatalf("a spent budget should stop after one batch: %+v %v", report, err)
	}
	entry.Drain = true
	j, err = New(db, cfg, Options{Owner: "a", Registry: []Entry{entry}, Now: func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := j.RunOnce(t.Context()); err != nil || report.Deleted[table] != 25 {
		t.Fatalf("a draining entry must run until empty: %+v %v", report, err)
	}
}

func TestOneRunnerAtATimeAndLeaseExpiry(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) SELECT now() - interval '3 days', 'done' FROM generate_series(1, 4)", table)); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: day}
	holder := newJanitor(t, db, "node-a", DefaultConfig(), entry)
	if held, err := holder.acquire(t.Context()); err != nil || !held {
		t.Fatalf("first acquire = %v, %v", held, err)
	}
	t.Cleanup(func() {
		_ = holder.leaser().Release(context.Background(), leaseTask, "node-a", false)
	})
	var wg sync.WaitGroup
	reports := make([]Report, 3)
	for i := range reports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], _ = newJanitor(t, db, fmt.Sprintf("node-%d", i), DefaultConfig(), entry).RunOnce(t.Context())
		}()
	}
	wg.Wait()
	for i, report := range reports {
		if report.Leader {
			t.Errorf("runner %d ran while node-a held the lease", i)
		}
	}
	if got := count(t, db, "SELECT count(*) FROM "+table); got != 4 {
		t.Fatalf("rows = %d, nothing may be deleted by a runner without the lease", got)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE scheduled_tasks SET lease_until = now() - interval '1 second' WHERE id=$1::uuid", leaseTask.ID); err != nil {
		t.Fatal(err)
	}
	report, err := newJanitor(t, db, "node-b", DefaultConfig(), entry).RunOnce(t.Context())
	if err != nil || !report.Leader || report.Deleted[table] != 4 {
		t.Fatalf("an expired lease should be taken over: %+v %v", report, err)
	}
	var visible int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM scheduled_tasks WHERE kind='retention-janitor' AND enabled").Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("the lease row must never be enabled: %d %v", visible, err)
	}
}

func TestLagIsReportedForOverdueRows(t *testing.T) {
	db := openDB(t)
	table := scratchTable(t, db)
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("INSERT INTO %s (at, state) VALUES (now() - interval '10 days', 'locked')", table)); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Table: table, TimeColumn: "at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 7 * day}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), fmt.Sprintf("SELECT id FROM %s FOR UPDATE", table)); err != nil {
		t.Fatal(err)
	}
	j := newJanitor(t, db, "a", DefaultConfig(), entry)
	if _, err := j.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	stats := j.Stats()
	if got := stats[0].LagSeconds; got < (3*day).Seconds()-60 || got > (3*day).Seconds()+60 {
		t.Fatalf("lag = %vs, want about 3 days", got)
	}
}

// columnInfo describes a column for the generic row builder.
type columnInfo struct{ name, udt string }

func notNullColumns(t *testing.T, db *sql.DB, table string) []columnInfo {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT column_name, udt_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND is_nullable='NO' AND column_default IS NULL AND is_generated='NEVER' ORDER BY ordinal_position", table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.udt); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func dummy(c columnInfo) string {
	switch c.udt {
	case "uuid":
		return "gen_random_uuid()"
	case "text", "varchar":
		return "md5(random()::text)"
	case "int2", "int4", "int8":
		return "0"
	case "bool":
		return "false"
	case "jsonb", "json":
		return "'{}'::" + c.udt
	case "timestamptz", "timestamp":
		return "now()"
	case "bytea":
		return "'\\x00'::bytea"
	}
	return "NULL"
}

// fixtures give, per registry label, the column values that make a row terminal and
// the values that make an otherwise expired row non-terminal.
type fixture struct {
	terminal, live map[string]string
	// setup inserts the nexus migration batches the entries point at.
	setup bool
}

const (
	terminalBatch = "'00000000-0000-7000-8000-00000000f001'"
	liveBatch     = "'00000000-0000-7000-8000-00000000f002'"
)

func fixtures() map[string]fixture {
	return map[string]fixture{
		"repository_request_rollup":          {},
		"webhook_deliveries_succeeded":       {terminal: map[string]string{"state": "'succeeded'", "delivered_at": "now()"}, live: map[string]string{"state": "'pending'", "delivered_at": "NULL", "next_attempt_at": "now()"}},
		"webhook_deliveries_dead":            {terminal: map[string]string{"state": "'dead'"}, live: map[string]string{"state": "'retrying'", "next_attempt_at": "now()"}},
		"lifecycle_jobs_done":                {terminal: map[string]string{"state": "'completed'", "kind": "'scan'", "idempotency_key": "md5(random()::text)"}, live: map[string]string{"state": "'running'"}},
		"lifecycle_jobs_failed":              {terminal: map[string]string{"state": "'failed'", "kind": "'scan'", "idempotency_key": "md5(random()::text)"}, live: map[string]string{"state": "'retrying'"}},
		"replication_plans":                  {terminal: map[string]string{"state": "'completed'", "format": "'raw'"}, live: map[string]string{"state": "'running'"}},
		"cache_tasks":                        {live: map[string]string{"completed_at": "NULL"}},
		"audit_cleanup_jobs":                 {terminal: map[string]string{"state": "'completed'", "batch_size": "1"}, live: map[string]string{"state": "'running'"}},
		"scheduled_task_runs":                {terminal: map[string]string{"state": "'submitted'", "trigger": "'manual'"}},
		"user_sessions":                      {terminal: map[string]string{"kind": "'oidc'", "created_at": "now() - interval '100 years'"}},
		"runtime_node_sessions":              {live: map[string]string{"stopped_at": "NULL"}},
		"repository_quota_alert_events":      {terminal: map[string]string{"notification_code": "'queued'", "scenario": "'warning'", "sequence": "1"}},
		"email_test_deliveries":              {terminal: map[string]string{"state": "'accepted'", "scenario": "'warning'", "locale": "'en-CA'"}, live: map[string]string{"state": "'pending'"}},
		"nexus_migration_entries":            {terminal: map[string]string{"format": "'raw'", "state": "'pending'", "ordinal": "(random()*1000000000)::int", "batch_id": terminalBatch}, live: map[string]string{"batch_id": liveBatch}, setup: true},
		"hosted_repository_idempotency":      {},
		"hosted_group_idempotency":           {},
		"native_apt_publication_idempotency": {},
		"native_maven_commit_idempotency":    {},
		"native_maven_publish_idempotency":   {},
		"workload_jti_seen":                  {},
		"saml_assertion_ids":                 {},
		"saml_request_state":                 {},
	}
}

// TestEveryRegistryEntryDeletesOnlyTerminalExpiredRows inserts, per entry, one
// expired terminal row, one fresh terminal row and (where the predicate can differ)
// one expired non-terminal row, runs the janitor and expects only the first to go.
// The rows go into a private schema of table copies (scratchSchema).
func TestCheckpointsGoWithCompletedPlansAndDeliveriesStayWhileAnEventPointsAtThem(t *testing.T) {
	db := scratchSchema(t)
	ctx := t.Context()
	var completed, running, delivery string
	if err := db.QueryRowContext(ctx, "SELECT gen_random_uuid()::text, gen_random_uuid()::text, gen_random_uuid()::text").Scan(&completed, &running, &delivery); err != nil {
		t.Fatal(err)
	}
	for plan, state := range map[string]string{completed: "completed", running: "running"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO replication_plans (id,source_repository_id,target_repository_id,format,idempotency_key,state,completed_at) VALUES ($1::uuid,gen_random_uuid(),gen_random_uuid(),'raw',md5(random()::text),$2,now())", plan, state); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO replication_checkpoints (plan_id,object_key,digest,size,byte_offset,state,attempts,last_error,verified_at,updated_at,source_object_key) VALUES ($1::uuid,'k','sha256:'||repeat('a',64),1,0,'pending',0,'',NULL,now() - interval '2 hours','s')", plan); err != nil {
			t.Fatal(err)
		}
	}
	checkpoints, ok := map[string]Entry{}, false
	for _, entry := range DefaultRegistry() {
		checkpoints[entry.Name()] = entry
	}
	if _, ok = checkpoints["replication_checkpoints"]; !ok {
		t.Fatal("registry lacks replication_checkpoints")
	}
	if _, err := newJanitor(t, db, "cp", DefaultConfig(), checkpoints["replication_checkpoints"]).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, "SELECT count(*) FROM replication_checkpoints WHERE plan_id=$1::uuid", completed); got != 0 {
		t.Errorf("checkpoints of a completed plan = %d, want 0", got)
	}
	if got := count(t, db, "SELECT count(*) FROM replication_checkpoints WHERE plan_id=$1::uuid", running); got != 1 {
		t.Errorf("checkpoints of a running plan = %d, want 1", got)
	}
}

// TestSAMLStateRowsAreDeletedPastExpiryPlusSkewInThrottledBatches runs the registry entries of the
// two SAML tables (ADR 0018 section 3, ADR 0023 section 2) against copies of the real tables and
// indexes: rows past expires_at plus the default clock skew go in batches of two, rows inside the
// skew grace and unexpired rows survive.
func TestSAMLStateRowsAreDeletedPastExpiryPlusSkewInThrottledBatches(t *testing.T) {
	db := scratchSchema(t)
	skew := time.Minute
	for _, entry := range DefaultRegistry() {
		if entry.Table != "saml_assertion_ids" && entry.Table != "saml_request_state" {
			continue
		}
		t.Run(entry.Table, func(t *testing.T) {
			for _, keep := range []time.Duration{entry.Standard, entry.FreeTier} {
				if keep != skew {
					t.Fatalf("keep time %s differs from the default SAML clock skew %s", keep, skew)
				}
			}
			insert := func(age time.Duration, n int) {
				for range n {
					insertRow(t, db, entry.Table, "expires_at", fmt.Sprintf("now() - interval '%d seconds'", int64(age.Seconds())), nil)
				}
			}
			insert(skew+time.Minute, 5) // past expiry plus skew: deleted
			insert(skew/2, 2)           // expired but inside the skew grace: kept
			insert(-10*time.Minute, 2)  // not yet expired: kept
			entry.Batch = 2
			var sleeps int
			j, err := New(db, DefaultConfig(), Options{Owner: "saml", Registry: []Entry{entry}, Sleep: func(context.Context, time.Duration) { sleeps++ }})
			if err != nil {
				t.Fatal(err)
			}
			report, err := j.RunOnce(t.Context())
			if err != nil || report.Deleted[entry.Table] != 5 {
				t.Fatalf("RunOnce = %+v, %v; want 5 deleted", report, err)
			}
			if sleeps != 2 {
				t.Fatalf("sleeps = %d, want one after each full batch of 2, 2 and a final partial batch of 1", sleeps)
			}
			if got := count(t, db, "SELECT count(*) FROM "+entry.Table); got != 4 {
				t.Fatalf("rows left = %d, want the 4 inside the grace or unexpired", got)
			}
		})
	}
}

func TestEveryRegistryEntryDeletesOnlyTerminalExpiredRows(t *testing.T) {
	single := scratchSchema(t)
	cfg := DefaultConfig()
	covered := map[string]bool{}
	for _, entry := range DefaultRegistry() {
		fix, known := fixtures()[entry.Name()]
		if !known {
			if !entry.PartitionedOnPostgres && !entry.Optional && entry.Table != "replication_checkpoints" {
				t.Errorf("no fixture for %s", entry.Name())
			}
			continue
		}
		covered[entry.Name()] = true
		t.Run(entry.Name(), func(t *testing.T) {
			keep, _ := cfg.Keep(entry)
			expired := fmt.Sprintf("now() - interval '%d seconds'", int64(keep.Seconds())+3600)
			insert := func(overrides map[string]string, timeValue string) {
				insertRow(t, single, entry.Table, entry.TimeColumn, timeValue, overrides)
			}
			if _, err := newJanitor(t, single, "fixture", cfg, entry).RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if fix.setup {
				_, _ = single.ExecContext(t.Context(), "DELETE FROM nexus_migration_batches WHERE id IN ("+terminalBatch+","+liveBatch+")")
				insertRow(t, single, "nexus_migration_batches", "created_at", "now()", map[string]string{"id": terminalBatch, "state": "'completed'", "actor_kind": "''"})
				insertRow(t, single, "nexus_migration_batches", "created_at", "now()", map[string]string{"id": liveBatch, "state": "'running'", "actor_kind": "''"})
			}
			before := count(t, single, "SELECT count(*) FROM "+entry.Table)
			insert(fix.terminal, expired)
			insert(fix.terminal, "now()")
			want := before + 1
			if fix.live != nil {
				live := map[string]string{}
				for k, v := range fix.terminal {
					live[k] = v
				}
				for k, v := range fix.live {
					live[k] = v
				}
				if v, ok := live[entry.TimeColumn]; !ok || v != "NULL" {
					live[entry.TimeColumn] = expired
				}
				insert(live, expired)
				want++
			}
			j := newJanitor(t, single, "fixture", cfg, entry)
			if _, err := j.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := count(t, single, "SELECT count(*) FROM "+entry.Table); got != want {
				t.Fatalf("rows after = %d, want %d", got, want)
			}
		})
	}
	if len(covered) == 0 {
		t.Fatal("no entries were exercised")
	}
}

// insertRow inserts one row with dummy values for every NOT NULL column without a
// default, the given overrides, and the time column set to timeValue.
func insertRow(t *testing.T, db *sql.DB, table, timeColumn, timeValue string, overrides map[string]string) {
	t.Helper()
	values := map[string]string{timeColumn: timeValue}
	order := []string{timeColumn}
	for _, c := range notNullColumns(t, db, table) {
		if c.name == timeColumn {
			continue
		}
		values[c.name] = dummy(c)
		order = append(order, c.name)
	}
	for name, value := range overrides {
		if _, ok := values[name]; !ok {
			order = append(order, name)
		}
		values[name] = value
	}
	columns, literals := make([]string, 0, len(order)), make([]string, 0, len(order))
	for _, name := range order {
		columns, literals = append(columns, name), append(literals, values[name])
	}
	statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ","), strings.Join(literals, ","))
	if _, err := db.ExecContext(t.Context(), statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// TestBatchSelectionsAreServedByAnIndex plans the inner SELECT of every entry's batch
// delete with sequential scans discouraged: the oldest-first scan must be able to use an
// index, whichever the planner prefers for the table size. The entries that have a
// dedicated *_retention_idx must scan that index.
func TestBatchSelectionsAreServedByAnIndex(t *testing.T) {
	db := openDB(t)
	existing := map[string]string{
		"user_sessions": "user_sessions_expiry_idx",
		// The planner may drive this entry from the completed plans or scan the checkpoints
		// by age; either is index-served, so only "some index" is asserted.
		"replication_checkpoints":            "",
		"hosted_repository_idempotency":      "hosted_repository_idempotency_expires_at_idx",
		"native_apt_publication_idempotency": "native_apt_publication_idempotency_expiry_idx",
		"native_maven_publish_idempotency":   "native_maven_publish_idempotency_expires_at_idx",
		"workload_jti_seen":                  "workload_jti_seen_expires_at_idx",
		"saml_assertion_ids":                 "saml_assertion_ids_expires_at_idx",
		"saml_request_state":                 "saml_request_state_expires_at_idx",
	}
	for _, entry := range DefaultRegistry() {
		if entry.PartitionedOnPostgres || entry.Optional {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			want, ok := existing[entry.Table]
			if !ok {
				want = entry.Table + "_retention_idx"
			}
			query := fmt.Sprintf("SELECT ctid FROM %s WHERE (%s) AND %s < $1 ORDER BY %s LIMIT $2", entry.Table, entry.Terminal, entry.TimeColumn, entry.TimeColumn)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			plan, err := explaincheck.Plan(t.Context(), tx, query, time.Now(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if len(plan.Indexes()) == 0 || len(plan.SequentialScans()) > 0 {
					t.Fatalf("plan is not index-served: indexes %v, sequential scans %v", plan.Indexes(), plan.SequentialScans())
				}
				return
			}
			if !plan.UsesIndex(want) {
				t.Fatalf("plan scans %v, want %s", plan.Indexes(), want)
			}
		})
	}
}
