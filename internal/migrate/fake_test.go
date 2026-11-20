package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeResult is what a scripted query returns.
type fakeResult struct {
	columns []string
	rows    [][]driver.Value
}

// fakeServer is a scripted database/sql driver that records every statement.
type fakeServer struct {
	mu      sync.Mutex
	log     []string
	handler func(query string, args []driver.NamedValue) (fakeResult, error)
}

func (s *fakeServer) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(fakeConnector{s})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (s *fakeServer) record(query string, args []driver.NamedValue) {
	values := make([]string, len(args))
	for i, a := range args {
		values[i] = fmt.Sprint(a.Value)
	}
	entry := strings.Join(strings.Fields(query), " ")
	if len(values) > 0 {
		entry += " | " + strings.Join(values, ",")
	}
	s.mu.Lock()
	s.log = append(s.log, entry)
	s.mu.Unlock()
}

// logged returns the recorded statements that contain every fragment.
func (s *fakeServer) logged(fragments ...string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, entry := range s.log {
		match := true
		for _, f := range fragments {
			if !strings.Contains(entry, f) {
				match = false
			}
		}
		if match {
			out = append(out, entry)
		}
	}
	return out
}

type fakeConnector struct{ server *fakeServer }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{c.server}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not supported") }

type fakeConn struct{ server *fakeServer }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	c.server.record("BEGIN", nil)
	return fakeTx{c.server}, nil
}

type fakeTx struct{ server *fakeServer }

func (t fakeTx) Commit() error   { t.server.record("COMMIT", nil); return nil }
func (t fakeTx) Rollback() error { t.server.record("ROLLBACK", nil); return nil }

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.server.record(query, args)
	result, err := c.server.handler(query, args)
	if err != nil {
		return nil, err
	}
	return &fakeRows{result: result}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.server.record(query, args)
	if _, err := c.server.handler(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type fakeRows struct {
	result fakeResult
	next   int
}

func (r *fakeRows) Columns() []string { return r.result.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.result.rows) {
		return io.EOF
	}
	copy(dest, r.result.rows[r.next])
	r.next++
	return nil
}

func one(columns string, values ...driver.Value) fakeResult {
	return fakeResult{columns: strings.Split(columns, ","), rows: [][]driver.Value{values}}
}

func none() fakeResult { return fakeResult{columns: []string{"x"}} }
