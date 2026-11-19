package main

import (
	"fmt"
	"strings"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/dialect"
	dialectpostgres "github.com/mlops-ca/notarx/internal/dialect/postgres"
	"github.com/mlops-ca/notarx/internal/repository"
)

// selectDatabaseBackend picks the dialect set once, from NOTARX_DATABASE_DRIVER
// (ADR 0022). MySQL is refused, listing the store ports that have no MySQL
// implementation yet, so a partial backend never serves traffic.
func selectDatabaseBackend(cfg config.Config) (dialect.Set, error) {
	switch cfg.DatabaseDriver {
	case "", database.DriverPostgres:
		return dialectpostgres.New(), nil
	case database.DriverMySQL:
		if missing := repository.MissingMySQLStorePorts(); len(missing) > 0 {
			return dialect.Set{}, fmt.Errorf("the MySQL backend is incomplete and cannot serve traffic; no MySQL implementation for %d store ports: %s", len(missing), strings.Join(missing, ", "))
		}
		return dialect.Set{}, fmt.Errorf("the MySQL dialect primitives are not implemented yet")
	default:
		return dialect.Set{}, fmt.Errorf("unsupported database driver %q", cfg.DatabaseDriver)
	}
}
