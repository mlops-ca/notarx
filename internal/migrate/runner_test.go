package migrate

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testFile(name, body string) File {
	return File{Name: name, Checksum: checksum([]byte(body)), Body: body}
}

// mysqlScript answers the runner's own queries; ledger is the answer to the ledger row lookup.
func mysqlScript(ledger fakeResult, overrides map[string]func() (fakeResult, error)) func(string, []driver.NamedValue) (fakeResult, error) {
	return func(query string, _ []driver.NamedValue) (fakeResult, error) {
		for fragment, answer := range overrides {
			if strings.Contains(query, fragment) {
				return answer()
			}
		}
		switch {
		case strings.HasPrefix(query, "SELECT DATABASE()"):
			return one("d", "notarx"), nil
		case strings.HasPrefix(query, "SELECT GET_LOCK"):
			return one("l", int64(1)), nil
		case strings.Contains(query, "information_schema.tables"):
			return one("c", int64(0)), nil
		case strings.HasPrefix(query, "SELECT checksum, state"):
			return ledger, nil
		}
		return none(), nil
	}
}

func ledgerRow(checksum, state string, done int64) fakeResult {
	return one("checksum,state,statements_done", checksum, state, done)
}

func TestMySQLAppliesAFileStatementByStatement(t *testing.T) {
	server := &fakeServer{handler: mysqlScript(none(), nil)}
	file := testFile("000001_baseline.sql", "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);")
	var out bytes.Buffer
	if err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Applying migration 000001_baseline.sql.\n" {
		t.Fatalf("output = %q", out.String())
	}
	if got := server.logged("INSERT INTO notarx_schema_migrations"); len(got) != 1 || !strings.Contains(got[0], "'applying', 0") {
		t.Fatalf("the ledger row is not written in the applying state first: %v", got)
	}
	for _, want := range []string{"CREATE TABLE a", "statements_done = ? WHERE filename = ? | 1,000001_baseline.sql", "CREATE TABLE b", "statements_done = ? WHERE filename = ? | 2,000001_baseline.sql", "state = 'applied', statements_done = ?"} {
		if len(server.logged(want)) != 1 {
			t.Fatalf("missing %q in %v", want, server.log)
		}
	}
	if len(server.logged("GET_LOCK", "notarx-migrate:notarx")) != 1 || len(server.logged("RELEASE_LOCK", "notarx-migrate:notarx")) != 1 {
		t.Fatalf("the lock is not taken and released once: %v", server.log)
	}
	order := strings.Join(server.log, "\n")
	if strings.Index(order, "GET_LOCK") > strings.Index(order, "CREATE TABLE IF NOT EXISTS notarx_schema_migrations") {
		t.Fatal("the ledger is created before the lock is taken")
	}
}

func TestMySQLSkipsAppliedAndRefusesChangedFiles(t *testing.T) {
	file := testFile("000001_baseline.sql", "CREATE TABLE a (id INT);")
	server := &fakeServer{handler: mysqlScript(ledgerRow(file.Checksum, StateApplied, 1), nil)}
	var out bytes.Buffer
	if err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Skipping applied migration 000001_baseline.sql.\n" || len(server.logged("CREATE TABLE a")) != 0 {
		t.Fatalf("an applied migration ran again: %q %v", out.String(), server.log)
	}
	for _, state := range []string{StateApplied, StateApplying} {
		changed := &fakeServer{handler: mysqlScript(ledgerRow(strings.Repeat("0", 64), state, 1), nil)}
		err := Run(context.Background(), changed.open(t), DialectMySQL, []File{file}, Options{Repair: true})
		if !errors.Is(err, ErrChecksumChanged) {
			t.Fatalf("state %s: err = %v", state, err)
		}
	}
}

func TestMySQLRefusesAHalfAppliedMigrationWithoutRepair(t *testing.T) {
	file := testFile("000002_more.sql", "ALTER TABLE a ADD COLUMN x INT;\nALTER TABLE a ADD COLUMN y INT;")
	server := &fakeServer{handler: mysqlScript(ledgerRow(file.Checksum, StateApplying, 1), nil)}
	err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{})
	if !errors.Is(err, ErrRepairRequired) || !strings.Contains(err.Error(), "stopped after statement 1 of 2") {
		t.Fatalf("err = %v", err)
	}
	if len(server.logged("ALTER TABLE")) != 0 {
		t.Fatal("a half-applied migration ran without --repair")
	}
}

func TestMySQLRepairShowsTheOffsetThenResumes(t *testing.T) {
	file := testFile("000002_more.sql", "ALTER TABLE a ADD COLUMN x INT;\nALTER TABLE a ADD COLUMN y INT;\nALTER TABLE a ADD COLUMN z INT;")
	ledger := ledgerRow(file.Checksum, StateApplying, 1)

	show := &fakeServer{handler: mysqlScript(ledger, nil)}
	var out bytes.Buffer
	err := Run(context.Background(), show.open(t), DialectMySQL, []File{file}, Options{Repair: true, Out: &out})
	if !errors.Is(err, ErrRepairRequired) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"stopped after statement 1 of 3", "Last recorded statement (1): ALTER TABLE a ADD COLUMN x INT", "Next statement (2): ALTER TABLE a ADD COLUMN y INT", "N = 1 (it did not) or 2 (it did)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	if len(show.logged("ALTER TABLE")) != 0 {
		t.Fatal("showing the offset changed the database")
	}

	for completed, wantRun := range map[int][]string{1: {"ADD COLUMN y", "ADD COLUMN z"}, 2: {"ADD COLUMN z"}} {
		resume := &fakeServer{handler: mysqlScript(ledger, nil)}
		n := completed
		out.Reset()
		if err := Run(context.Background(), resume.open(t), DialectMySQL, []File{file}, Options{Repair: true, Completed: &n, Out: &out}); err != nil {
			t.Fatal(err)
		}
		if len(resume.logged("SET statements_done = ? WHERE filename = ? | "+strconv.Itoa(completed)+",000002_more.sql")) == 0 {
			t.Fatalf("the confirmed count is not stored: %v", resume.log)
		}
		for _, want := range wantRun {
			if len(resume.logged(want)) != 1 {
				t.Fatalf("completed %d: %q did not run once: %v", completed, want, resume.log)
			}
		}
		if completed == 2 && len(resume.logged("ADD COLUMN y")) != 0 {
			t.Fatal("a confirmed statement ran again")
		}
		if !strings.Contains(out.String(), "Resuming migration 000002_more.sql after statement") || len(resume.logged("state = 'applied'")) != 1 {
			t.Fatalf("out = %q log = %v", out.String(), resume.log)
		}
	}
}

func TestMySQLRepairRejectsAnImplausibleCount(t *testing.T) {
	file := testFile("000002_more.sql", "ALTER TABLE a ADD COLUMN x INT;\nALTER TABLE a ADD COLUMN y INT;")
	for _, completed := range []int{0, 3} {
		server := &fakeServer{handler: mysqlScript(ledgerRow(file.Checksum, StateApplying, 1), nil)}
		n := completed
		err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{Repair: true, Completed: &n})
		if err == nil || !strings.Contains(err.Error(), "must be 1 or 2") {
			t.Fatalf("completed %d: err = %v", completed, err)
		}
	}
	// A counter past the end of the file means the file is not the one that was applying.
	server := &fakeServer{handler: mysqlScript(ledgerRow(file.Checksum, StateApplying, 9), nil)}
	if err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{Repair: true}); err == nil || !strings.Contains(err.Error(), "records 9 statements done") {
		t.Fatalf("err = %v", err)
	}
	// With every statement confirmed the file is only marked applied.
	finished := &fakeServer{handler: mysqlScript(ledgerRow(file.Checksum, StateApplying, 1), nil)}
	two := 2
	if err := Run(context.Background(), finished.open(t), DialectMySQL, []File{file}, Options{Repair: true, Completed: &two}); err != nil || len(finished.logged("ALTER TABLE")) != 0 || len(finished.logged("state = 'applied'")) != 1 {
		t.Fatalf("err = %v log = %v", err, finished.log)
	}
}

func TestMySQLStatementFailureLeavesTheLedgerApplying(t *testing.T) {
	file := testFile("000002_more.sql", "SELECT 1;\nALTER TABLE broken ADD COLUMN x INT;\nSELECT 3;")
	server := &fakeServer{handler: mysqlScript(none(), map[string]func() (fakeResult, error){
		"ALTER TABLE broken": func() (fakeResult, error) { return fakeResult{}, errors.New("table broken does not exist") },
	})}
	err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{})
	if err == nil || !strings.Contains(err.Error(), "statement 2 of 3 failed") {
		t.Fatalf("err = %v", err)
	}
	if len(server.logged("state = 'applied'")) != 0 || len(server.logged("statements_done = ? WHERE filename = ? | 1,")) != 1 || len(server.logged("statements_done = ? WHERE filename = ? | 2,")) != 0 {
		t.Fatalf("the ledger does not stop at statement 1: %v", server.log)
	}
	if len(server.logged("RELEASE_LOCK")) != 1 {
		t.Fatal("the lock is not released after a failure")
	}
}

func TestMySQLRefusesForeignSchemasAndLockProblems(t *testing.T) {
	file := testFile("000001_baseline.sql", "SELECT 1;")
	cases := map[string]struct {
		override map[string]func() (fakeResult, error)
		want     error
		text     string
	}{
		"foreign tables": {map[string]func() (fakeResult, error){"information_schema.tables": func() (fakeResult, error) { return one("c", int64(3)), nil }}, ErrForeignSchema, "holds 3 table"},
		"lock held":      {map[string]func() (fakeResult, error){"GET_LOCK": func() (fakeResult, error) { return one("l", int64(0)), nil }}, ErrLockHeld, "notarx-migrate:notarx"},
		"lock error":     {map[string]func() (fakeResult, error){"GET_LOCK": func() (fakeResult, error) { return one("l", nil), nil }}, nil, "server reported an error"},
		"lock query":     {map[string]func() (fakeResult, error){"GET_LOCK": func() (fakeResult, error) { return fakeResult{}, errors.New("boom") }}, nil, "take the migration lock"},
		"no database":    {map[string]func() (fakeResult, error){"SELECT DATABASE()": func() (fakeResult, error) { return one("d", nil), nil }}, nil, "names no database"},
		"ledger create":  {map[string]func() (fakeResult, error){"CREATE TABLE IF NOT EXISTS": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "create the migration ledger"},
		"ledger read":    {map[string]func() (fakeResult, error){"SELECT checksum, state": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "read the ledger row"},
		"record row":     {map[string]func() (fakeResult, error){"INSERT INTO notarx": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "record migration"},
		"foreign query":  {map[string]func() (fakeResult, error){"information_schema.tables": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "check for foreign tables"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := &fakeServer{handler: mysqlScript(none(), tc.override)}
			err := Run(context.Background(), server.open(t), DialectMySQL, []File{file}, Options{LockWait: time.Second})
			if err == nil || !strings.Contains(err.Error(), tc.text) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v", err)
			}
			if tc.want == ErrLockHeld && len(server.logged("GET_LOCK", "| notarx-migrate:notarx,1")) != 1 {
				t.Fatalf("the lock wait is not passed in seconds: %v", server.log)
			}
		})
	}
	unsplittable := testFile("000002_bad.sql", "DELIMITER //\nSELECT 1//")
	server := &fakeServer{handler: mysqlScript(none(), nil)}
	if err := Run(context.Background(), server.open(t), DialectMySQL, []File{unsplittable}, Options{}); err == nil || !strings.Contains(err.Error(), "DELIMITER") {
		t.Fatalf("err = %v", err)
	}
}

func TestLockNameStaysWithinTheServerLimit(t *testing.T) {
	if got := lockName("notarx"); got != "notarx-migrate:notarx" {
		t.Fatal(got)
	}
	long := strings.Repeat("d", 64)
	got := lockName(long)
	if len(got) > maxLockName || got == "notarx-migrate:"+long || got != lockName(long) || got == lockName(long[:63]+"e") {
		t.Fatalf("lock name %q", got)
	}
}

// postgresScript answers the PostgreSQL runner's own queries.
func postgresScript(trigram string, recorded map[string]string, overrides map[string]func() (fakeResult, error)) func(string, []driver.NamedValue) (fakeResult, error) {
	return func(query string, args []driver.NamedValue) (fakeResult, error) {
		for fragment, answer := range overrides {
			if strings.Contains(query, fragment) {
				return answer()
			}
		}
		switch {
		case strings.HasPrefix(query, "SELECT current_user"):
			return one("u,d", "notarx", "notarx_test"), nil
		case strings.HasPrefix(query, "SELECT CASE"):
			return one("s", trigram), nil
		case strings.HasPrefix(query, "SELECT count(*)"):
			return one("c", int64(0)), nil
		case strings.HasPrefix(query, "SELECT checksum FROM"):
			if sum, ok := recorded[args[0].Value.(string)]; ok {
				return one("checksum", sum), nil
			}
			return none(), nil
		}
		return none(), nil
	}
}

func TestPostgresAppliesEachFileInOneTransaction(t *testing.T) {
	first := testFile("000001_baseline.sql", "CREATE TABLE a (id int);\nCREATE TABLE b (id int);")
	second := testFile("000002_more.sql", "ALTER TABLE a ADD COLUMN x int;")
	server := &fakeServer{handler: postgresScript("installed", map[string]string{second.Name: second.Checksum}, nil)}
	var out bytes.Buffer
	if err := Run(context.Background(), server.open(t), DialectPostgres, []File{first, second}, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Applying migration 000001_baseline.sql.\nSkipping applied migration 000002_more.sql.\n" {
		t.Fatalf("output = %q", out.String())
	}
	got := strings.Join(server.log, "\n")
	body := strings.Index(got, "CREATE TABLE a (id int); CREATE TABLE b (id int);")
	insert := strings.Index(got, "INSERT INTO notarx_schema_migrations (filename, checksum) VALUES ($1, $2) | 000001_baseline.sql,"+first.Checksum)
	if body < 0 || insert < body || strings.Index(got, "BEGIN") > body || strings.Index(got, "COMMIT") < insert {
		t.Fatalf("file and ledger row are not one transaction: %v", server.log)
	}
	if len(server.logged("ALTER TABLE")) != 0 {
		t.Fatal("an applied migration ran again")
	}
}

func TestPostgresRefusals(t *testing.T) {
	file := testFile("000001_baseline.sql", "SELECT 1;")
	cases := map[string]struct {
		trigram  string
		recorded map[string]string
		override map[string]func() (fakeResult, error)
		want     error
		text     string
	}{
		"changed":     {"installed", map[string]string{file.Name: "abc"}, nil, ErrChecksumChanged, "recorded abc"},
		"foreign":     {"installed", nil, map[string]func() (fakeResult, error){"SELECT count(*)": func() (fakeResult, error) { return one("c", int64(2)), nil }}, ErrForeignSchema, "holds 2 table"},
		"unavailable": {"unavailable", nil, nil, nil, "not available on this PostgreSQL server"},
		"denied":      {"denied", nil, nil, nil, "role notarx may not create the pg_trgm extension"},
		"session":     {"installed", nil, map[string]func() (fakeResult, error){"SELECT current_user": func() (fakeResult, error) { return fakeResult{}, errors.New("down") }}, nil, "read the database session"},
		"trigram":     {"installed", nil, map[string]func() (fakeResult, error){"SELECT CASE": func() (fakeResult, error) { return fakeResult{}, errors.New("down") }}, nil, "check the pg_trgm extension"},
		"ledger":      {"installed", nil, map[string]func() (fakeResult, error){"CREATE TABLE IF NOT EXISTS": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "create the migration ledger"},
		"foreign sql": {"installed", nil, map[string]func() (fakeResult, error){"SELECT count(*)": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "check for foreign tables"},
		"lookup":      {"installed", nil, map[string]func() (fakeResult, error){"SELECT checksum FROM": func() (fakeResult, error) { return fakeResult{}, errors.New("denied") }}, nil, "read the ledger row"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := &fakeServer{handler: postgresScript(tc.trigram, tc.recorded, tc.override)}
			err := Run(context.Background(), server.open(t), DialectPostgres, []File{file}, Options{})
			if err == nil || !strings.Contains(err.Error(), tc.text) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPostgresRollsBackAFailingFile(t *testing.T) {
	file := testFile("000001_baseline.sql", "CREATE TABLE a (id int);")
	for name, fail := range map[string]string{"body": "CREATE TABLE a", "ledger": "INSERT INTO notarx"} {
		t.Run(name, func(t *testing.T) {
			server := &fakeServer{handler: postgresScript("creatable", nil, map[string]func() (fakeResult, error){fail: func() (fakeResult, error) { return fakeResult{}, errors.New("boom") }})}
			err := Run(context.Background(), server.open(t), DialectPostgres, []File{file}, Options{})
			if err == nil || len(server.logged("ROLLBACK")) != 1 || len(server.logged("COMMIT")) != 0 {
				t.Fatalf("err = %v log = %v", err, server.log)
			}
		})
	}
}

func TestRunRejectsUnsupportedRequests(t *testing.T) {
	server := &fakeServer{handler: postgresScript("installed", nil, nil)}
	db := server.open(t)
	if err := Run(context.Background(), db, "oracle", nil, Options{}); err == nil {
		t.Fatal("an unknown dialect was accepted")
	}
	if err := Run(context.Background(), db, DialectPostgres, nil, Options{Repair: true}); err == nil || !strings.Contains(err.Error(), "MySQL only") {
		t.Fatalf("err = %v", err)
	}
}

// TestPostgresSQLMatchesRunMigrationsScript keeps the Go runner's PostgreSQL
// statements in step with the script that stays the deployed runner.
func TestPostgresSQLMatchesRunMigrationsScript(t *testing.T) {
	data, err := os.ReadFile("../../scripts/run-migrations.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	heredoc := regexp.MustCompile("(?s)<<'SQL'\\n(.*?)\\nSQL\\n")
	blocks := heredoc.FindAllStringSubmatch(script, -1)
	find := func(prefix string) string {
		for _, block := range blocks {
			if strings.HasPrefix(block[1], prefix) {
				return strings.TrimSuffix(strings.TrimSpace(block[1]), ";")
			}
		}
		t.Fatalf("run-migrations.sh has no SQL block starting %q", prefix)
		return ""
	}
	for name, pair := range map[string][2]string{
		"trigram": {find("SELECT CASE"), trigramStateSQL},
		"ledger":  {find("CREATE TABLE IF NOT EXISTS notarx_schema_migrations"), postgresLedgerSQL},
		"foreign": {find("SELECT count(*)"), postgresForeignTablesSQL},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s SQL differs between scripts/run-migrations.sh and internal/migrate\nscript: %s\ngo:     %s", name, pair[0], pair[1])
		}
	}
	if !strings.Contains(script, "sed '/-- +goose Down/,$d'") || gooseDown != "-- +goose Down" {
		t.Error("the Down-section rule of run-migrations.sh changed")
	}
}
