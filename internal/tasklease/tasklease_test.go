package tasklease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a database/sql driver that logs each statement and answers with scripted results, so the statements of
// both dialects are exercised without a server.
type recorder struct {
	mu       sync.Mutex
	log      []string
	affected []int64 // rows affected per Exec, in order
	holder   string  // the lease_owner a SELECT returns
	noRows   bool    // a RETURNING query that matched nothing
	returned string
}

type recorderConn struct{ r *recorder }

func (c recorderConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c recorderConn) Close() error                        { return nil }
func (c recorderConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c recorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.log = append(c.r.log, query)
	var n int64
	if len(c.r.affected) > 0 {
		n, c.r.affected = c.r.affected[0], c.r.affected[1:]
	}
	return driver.RowsAffected(n), nil
}

func (c recorderConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.log = append(c.r.log, query)
	value := c.r.holder
	if strings.Contains(query, "RETURNING") {
		value = c.r.returned
		if c.r.noRows {
			return &recorderRows{}, nil
		}
	}
	return &recorderRows{values: []string{value}}, nil
}

type recorderRows struct {
	values []string
	i      int
}

func (r *recorderRows) Columns() []string { return []string{"lease_owner"} }
func (r *recorderRows) Close() error      { return nil }
func (r *recorderRows) Next(dest []driver.Value) error {
	if r.i >= len(r.values) {
		return sql.ErrNoRows
	}
	dest[0] = r.values[r.i]
	r.i++
	return nil
}

type recorderDriver struct{ r *recorder }

func (d recorderDriver) Open(string) (driver.Conn, error) { return recorderConn(d), nil }

var driverSeq int

func open(t *testing.T, r *recorder) *sql.DB {
	t.Helper()
	driverSeq++
	name := "tasklease-recorder-" + string(rune('a'+driverSeq))
	sql.Register(name, recorderDriver{r})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var task = Task{ID: "00000000-0000-7000-8000-0000000000a9", Name: "internal:test", Kind: "retention-janitor", Description: "test"}

func TestUnsupportedDialect(t *testing.T) {
	leaser := Leaser{DB: open(t, &recorder{}), Dialect: "oracle"}
	if _, err := leaser.Acquire(t.Context(), task, "a", time.Minute, 0); err == nil {
		t.Fatal("acquire accepted an unknown dialect")
	}
	if err := leaser.Release(t.Context(), task, "a", true); err == nil {
		t.Fatal("release accepted an unknown dialect")
	}
}

func TestAcquirePostgres(t *testing.T) {
	cases := []struct {
		name     string
		rec      *recorder
		held     bool
		statemts int
	}{
		{"update wins", &recorder{returned: "a"}, true, 1},
		{"held by another node and row exists", &recorder{noRows: true, affected: []int64{0}}, false, 2},
		{"first use creates the row", &recorder{noRows: true, affected: []int64{1}}, true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			held, err := Leaser{DB: open(t, rec), Dialect: Postgres}.Acquire(t.Context(), task, "a", time.Minute, time.Hour)
			if err != nil || held != tc.held || len(rec.log) != tc.statemts {
				t.Fatalf("held=%v err=%v statements=%d", held, err, len(rec.log))
			}
			if !strings.Contains(rec.log[0], "last_run_at") {
				t.Fatalf("the minimum interval is not checked: %s", rec.log[0])
			}
		})
	}
}

func TestAcquireMySQL(t *testing.T) {
	cases := []struct {
		name     string
		rec      *recorder
		held     bool
		statemts int
	}{
		{"update wins", &recorder{affected: []int64{1}}, true, 1},
		{"first use creates the row", &recorder{affected: []int64{0, 1}}, true, 2},
		{"held by another node", &recorder{affected: []int64{0, 0}, holder: "b"}, false, 3},
		{"renewal that rewrote identical values", &recorder{affected: []int64{0, 0}, holder: "a"}, true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			held, err := Leaser{DB: open(t, rec), Dialect: MySQL}.Acquire(t.Context(), task, "a", time.Minute, time.Hour)
			if err != nil || held != tc.held || len(rec.log) != tc.statemts {
				t.Fatalf("held=%v err=%v statements=%d", held, err, len(rec.log))
			}
			if !strings.Contains(rec.log[0], "last_run_at") || strings.Contains(rec.log[0], "$") {
				t.Fatalf("not a MySQL statement that checks the minimum interval: %s", rec.log[0])
			}
		})
	}
}

func TestAcquireIgnoresLastRunWithoutMinimumInterval(t *testing.T) {
	for _, dialect := range []Dialect{Postgres, MySQL} {
		rec := &recorder{affected: []int64{1}, returned: "a"}
		if _, err := (Leaser{DB: open(t, rec), Dialect: dialect}).Acquire(t.Context(), task, "a", time.Minute, 0); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.log[0], "<=0 OR last_run_at IS NULL") {
			t.Fatalf("%s: a zero minimum interval must not depend on last_run_at: %s", dialect, rec.log[0])
		}
	}
}

func TestIntervalDefault(t *testing.T) {
	if (Task{}).interval() != 900 || (Task{IntervalSeconds: 21600}).interval() != 21600 {
		t.Fatal("interval defaults to 900 and honours the task")
	}
}

func TestRelease(t *testing.T) {
	for _, dialect := range []Dialect{Postgres, MySQL} {
		rec := &recorder{}
		if err := (Leaser{DB: open(t, rec), Dialect: dialect}).Release(t.Context(), task, "a", true); err != nil {
			t.Fatal(err)
		}
		if len(rec.log) != 1 || !strings.Contains(rec.log[0], "last_run_at") {
			t.Fatalf("%s release: %v", dialect, rec.log)
		}
	}
}
