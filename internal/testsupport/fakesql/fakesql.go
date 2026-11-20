// Package fakesql is a scripted database/sql driver for unit tests: it records every statement
// and answers queries from a Handler, so SQL-issuing code runs without a database.
package fakesql

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

// Result is what a scripted query returns.
type Result struct {
	Columns []string
	Rows    [][]driver.Value
}

// Server is a scripted database/sql driver that records every statement.
type Server struct {
	mu      sync.Mutex
	Log     []string
	Handler func(query string, args []driver.NamedValue) (Result, error)
}

// Open returns a *sql.DB on the server, closed when the test ends.
func (s *Server) Open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(fakeConnector{s})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (s *Server) record(query string, args []driver.NamedValue) {
	values := make([]string, len(args))
	for i, a := range args {
		values[i] = fmt.Sprint(a.Value)
	}
	entry := strings.Join(strings.Fields(query), " ")
	if len(values) > 0 {
		entry += " | " + strings.Join(values, ",")
	}
	s.mu.Lock()
	s.Log = append(s.Log, entry)
	s.mu.Unlock()
}

// Logged returns the recorded statements that contain every fragment.
func (s *Server) Logged(fragments ...string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, entry := range s.Log {
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

type fakeConnector struct{ server *Server }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{c.server}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not supported") }

type fakeConn struct{ server *Server }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	c.server.record("BEGIN", nil)
	return fakeTx{c.server}, nil
}

type fakeTx struct{ server *Server }

func (t fakeTx) Commit() error   { t.server.record("COMMIT", nil); return nil }
func (t fakeTx) Rollback() error { t.server.record("ROLLBACK", nil); return nil }

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.server.record(query, args)
	result, err := c.server.Handler(query, args)
	if err != nil {
		return nil, err
	}
	return &fakeRows{result: result}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.server.record(query, args)
	if _, err := c.server.Handler(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type fakeRows struct {
	result Result
	next   int
}

func (r *fakeRows) Columns() []string { return r.result.Columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.result.Rows) {
		return io.EOF
	}
	copy(dest, r.result.Rows[r.next])
	r.next++
	return nil
}

// One is a one-row result; columns is a comma-separated list.
func One(columns string, values ...driver.Value) Result {
	return Result{Columns: strings.Split(columns, ","), Rows: [][]driver.Value{values}}
}

// None is an empty result.
func None() Result { return Result{Columns: []string{"x"}} }
