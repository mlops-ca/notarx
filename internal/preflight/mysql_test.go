package preflight

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/database"
)

func mysqlTestConfig() config.Config {
	cfg := testConfig()
	cfg.DatabaseDriver = database.DriverMySQL
	cfg.DatabaseURL = "mysql://notarx:super-secret@db.internal:3306/notarx"
	pools := database.DefaultPoolSet(database.DriverMySQL)
	cfg.DatabasePool, cfg.DatabaseCoordinatorPool, cfg.DatabaseArtifactLockPool = pools.Main, pools.Coordinator, pools.ArtifactLock
	return cfg
}

func goodFacts() MySQLFacts {
	return MySQLFacts{Version: "8.4.3", LowerCaseTableNames: 0, MaxConnections: 151, WaitTimeoutSeconds: 28800}
}

func TestMySQLResultFailures(t *testing.T) {
	cfg := mysqlTestConfig()
	cases := []struct {
		name  string
		edit  func(*MySQLFacts)
		match string
	}{
		{"eight zero", func(f *MySQLFacts) { f.Version = "8.0.36" }, `version "8.0.36" is not supported`},
		{"nine", func(f *MySQLFacts) { f.Version = "9.0.1" }, "8.4 LTS is required"},
		{"garbage version", func(f *MySQLFacts) { f.Version = "mysql" }, "not supported"},
		{"non numeric version", func(f *MySQLFacts) { f.Version = "x.y.z" }, "not supported"},
		{"max connections", func(f *MySQLFacts) { f.MaxConnections = 40 }, "max_connections 40 is below the pool budget 44"},
		{"wait timeout", func(f *MySQLFacts) { f.WaitTimeoutSeconds = 30 }, "wait_timeout 30s is below the 120s"},
	}
	for _, tc := range cases {
		facts := goodFacts()
		tc.edit(&facts)
		result := mysqlResult(cfg, facts, nil)
		if result.Status != StatusFail || !strings.Contains(result.Summary, tc.match) {
			t.Errorf("%s: %#v", tc.name, result)
		}
	}
	if result := mysqlResult(cfg, goodFacts(), nil); result.Status != StatusPass || result.Details["version"] != "8.4.3" {
		t.Fatalf("good facts: %#v", result)
	}
	facts := goodFacts()
	facts.Version = "8.4.0-cloud"
	facts.LowerCaseTableNames = 1
	if result := mysqlResult(cfg, facts, nil); result.Status != StatusPass {
		t.Fatalf("a vendor suffix and any lower_case_table_names are fine: %#v", result)
	}
}

func TestMySQLCheckRunsInsteadOfPostgres(t *testing.T) {
	cfg := mysqlTestConfig()
	runner := NewRunner()
	runner.PostgresPing = func(context.Context, string) error {
		t.Fatal("PostgreSQL was probed on a MySQL deployment")
		return nil
	}
	runner.TrigramState = func(context.Context, string) (string, error) {
		t.Fatal("pg_trgm was probed on a MySQL deployment")
		return "", nil
	}
	runner.MySQLProbe = func(context.Context, config.Config) (MySQLFacts, error) { return goodFacts(), nil }
	report := runner.Run(context.Background(), cfg, []string{"postgres", "pg_trgm", "mysql"})
	if len(report.Results) != 1 || report.Results[0].Name != "mysql" || report.Results[0].Status != StatusPass {
		t.Fatalf("results = %#v", report.Results)
	}

	pg := NewRunner()
	pg.MySQLProbe = func(context.Context, config.Config) (MySQLFacts, error) {
		t.Fatal("MySQL probed on PostgreSQL")
		return MySQLFacts{}, nil
	}
	pg.PostgresPing = func(context.Context, string) error { return nil }
	if report := pg.Run(context.Background(), testConfig(), []string{"postgres", "mysql"}); len(report.Results) != 1 || report.Results[0].Name != "postgres" {
		t.Fatalf("results = %#v", report.Results)
	}
}

func TestMySQLProbeErrorsAreRedacted(t *testing.T) {
	cfg := mysqlTestConfig()
	runner := NewRunner()
	runner.MySQLProbe = func(context.Context, config.Config) (MySQLFacts, error) {
		return MySQLFacts{}, errors.New("dial " + cfg.DatabaseURL + ": access denied for password super-secret")
	}
	report := runner.Run(context.Background(), cfg, []string{"mysql"})
	if len(report.Results) != 1 || report.Results[0].Status != StatusFail || !report.Failed() {
		t.Fatalf("results = %#v", report.Results)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("report leaked the password: %s", encoded)
	}
}

func TestMySQLProbeRejectsAnUnusableURL(t *testing.T) {
	cfg := mysqlTestConfig()
	cfg.DatabaseURL = "notarx:super-secret@tcp(db)/notarx"
	if _, err := probeMySQL(context.Background(), cfg); err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("probe error = %v", err)
	}
}

// factsDriver answers the single facts query.
type factsDriver struct{ values []driver.Value }

func (d factsDriver) Open(string) (driver.Conn, error) { return &factsConn{d}, nil }

type factsConn struct{ d factsDriver }

func (c *factsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *factsConn) Close() error                        { return nil }
func (c *factsConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *factsConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "VERSION()") || !strings.Contains(query, "@@lower_case_table_names") || !strings.Contains(query, "@@max_connections") || !strings.Contains(query, "wait_timeout") {
		return nil, errors.New("unexpected query " + query)
	}
	return &factsRows{values: c.d.values}, nil
}

type factsRows struct {
	values []driver.Value
	done   bool
}

func (r *factsRows) Columns() []string { return []string{"v", "l", "m", "w"} }
func (r *factsRows) Close() error      { return nil }
func (r *factsRows) Next(dest []driver.Value) error {
	if r.done {
		return errors.New("EOF")
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

func TestQueryMySQLFactsReadsTheServerValues(t *testing.T) {
	sql.Register("mysql-facts", factsDriver{values: []driver.Value{"8.4.3", int64(0), int64(151), int64(28800)}})
	db, err := sql.Open("mysql-facts", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	facts, err := queryMySQLFacts(context.Background(), db)
	if err != nil || facts != goodFacts() {
		t.Fatalf("facts = %#v, err = %v", facts, err)
	}
}
