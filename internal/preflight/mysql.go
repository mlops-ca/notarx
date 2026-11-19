package preflight

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/database"
)

// MinMySQLWaitTimeout is the shortest acceptable session wait_timeout. The
// dedicated lock and lease connections are idle between pings (the lock lease
// is 35 s), so a server that drops idle sessions sooner than this would drop
// GET_LOCK holders (ADR 0022).
const MinMySQLWaitTimeout = 2 * time.Minute

// MySQLFacts are the server values the start-up check reads.
type MySQLFacts struct {
	Version             string
	LowerCaseTableNames int
	MaxConnections      int
	WaitTimeoutSeconds  int
}

func isMySQL(cfg config.Config) bool { return cfg.DatabaseDriver == database.DriverMySQL }

// probeMySQL reads the facts over one short-lived connection.
func probeMySQL(ctx context.Context, cfg config.Config) (MySQLFacts, error) {
	pool := database.PoolConfig{MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, TLSCAFile: cfg.DatabaseTLSCAFile}
	db, err := database.OpenMySQL(cfg.DatabaseURL, pool)
	if err != nil {
		return MySQLFacts{}, err
	}
	defer func() { _ = db.Close() }()
	return queryMySQLFacts(ctx, db)
}

func queryMySQLFacts(ctx context.Context, db *sql.DB) (MySQLFacts, error) {
	var facts MySQLFacts
	err := db.QueryRowContext(ctx, "SELECT VERSION(), @@lower_case_table_names, @@max_connections, @@session.wait_timeout").
		Scan(&facts.Version, &facts.LowerCaseTableNames, &facts.MaxConnections, &facts.WaitTimeoutSeconds)
	return facts, err
}

// supportedMySQLVersion accepts the 8.4 LTS series (ADR 0022). 8.0 is past end
// of life and 9.x innovation releases are accepted only after the full suite
// has passed on them.
func supportedMySQLVersion(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	return errMajor == nil && errMinor == nil && major == 8 && minor == 4
}

// mysqlResult judges the probe. Failures name the offending value but never the URL.
func mysqlResult(cfg config.Config, facts MySQLFacts, err error) Result {
	budget := cfg.DatabaseConnectionBudget()
	details := map[string]any{"pool_budget": budget}
	if err != nil {
		details["error"] = database.RedactError(err, cfg.DatabaseURL)
		return Result{Name: "mysql", Status: StatusFail, Summary: "dependency check failed", Details: details}
	}
	details["version"] = facts.Version
	details["lower_case_table_names"] = facts.LowerCaseTableNames
	details["max_connections"] = facts.MaxConnections
	details["wait_timeout_s"] = facts.WaitTimeoutSeconds
	fail := func(format string, args ...any) Result {
		return Result{Name: "mysql", Status: StatusFail, Summary: fmt.Sprintf(format, args...), Details: details}
	}
	switch {
	case !supportedMySQLVersion(facts.Version):
		return fail("MySQL server version %q is not supported; 8.4 LTS is required", facts.Version)
	case facts.MaxConnections < budget:
		return fail("MySQL max_connections %d is below the pool budget %d; lower NOTARX_DATABASE_MAX_CONNECTIONS or raise the server value", facts.MaxConnections, budget)
	case time.Duration(facts.WaitTimeoutSeconds)*time.Second < MinMySQLWaitTimeout:
		return fail("MySQL wait_timeout %ds is below the %ds the lock and lease connections need", facts.WaitTimeoutSeconds, int(MinMySQLWaitTimeout/time.Second))
	}
	return Result{Name: "mysql", Status: StatusPass, Summary: "MySQL is reachable and within its limits", Details: details}
}
