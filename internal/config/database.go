package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
)

const (
	defaultDatabasePollInterval = 500 * time.Millisecond
	minDatabasePollInterval     = 50 * time.Millisecond
	// maxDatabasePollInterval is the idle back-off ceiling of the MySQL poller
	// (ADR 0022), so a longer base cadence would contradict it.
	maxDatabasePollInterval = 5 * time.Second
)

// perPoolConnectionKeys are the individual pool-size keys that NOTARX_DATABASE_MAX_CONNECTIONS replaces.
var perPoolConnectionKeys = []string{
	"NOTARX_DATABASE_MAX_OPEN_CONNS", "NOTARX_DATABASE_MAX_IDLE_CONNS",
	"NOTARX_DATABASE_COORDINATOR_MAX_OPEN_CONNS", "NOTARX_DATABASE_COORDINATOR_MAX_IDLE_CONNS",
	"NOTARX_DATABASE_ARTIFACT_LOCK_MAX_OPEN_CONNS", "NOTARX_DATABASE_ARTIFACT_LOCK_MAX_IDLE_CONNS",
}

// configureDatabase reads the NOTARX_DATABASE_* driver keys (ADR 0022) and
// validates them against each other. Messages never contain the URLs.
func configureDatabase(cfg *Config) error {
	driver, err := enumEnv("NOTARX_DATABASE_DRIVER", database.DriverPostgres, database.DriverPostgres, database.DriverMySQL)
	if err != nil {
		return err
	}
	cfg.DatabaseDriver = driver
	cfg.DatabaseTLSCAFile = strings.TrimSpace(os.Getenv("NOTARX_DATABASE_TLS_CA_FILE"))
	cfg.DatabaseMigrateURL = strings.TrimSpace(os.Getenv("NOTARX_DATABASE_MIGRATE_URL"))
	if cfg.DatabaseExperimentalMySQL, err = boolEnv("NOTARX_EXPERIMENTAL_MYSQL", false); err != nil {
		return err
	}
	if cfg.DatabasePollInterval, err = durationEnv("NOTARX_DATABASE_POLL_INTERVAL", defaultDatabasePollInterval); err != nil {
		return err
	}
	if cfg.DatabasePollInterval < minDatabasePollInterval || cfg.DatabasePollInterval > maxDatabasePollInterval {
		return fmt.Errorf("NOTARX_DATABASE_POLL_INTERVAL must be between %s and %s", minDatabasePollInterval, maxDatabasePollInterval)
	}

	switch driver {
	case database.DriverPostgres:
		if _, err := url.ParseRequestURI(cfg.DatabaseURL); err != nil {
			return errors.New("NOTARX_DATABASE_URL is not a valid URL")
		}
		if cfg.DatabaseTLSCAFile != "" || cfg.DatabaseMigrateURL != "" {
			return errors.New("NOTARX_DATABASE_TLS_CA_FILE and NOTARX_DATABASE_MIGRATE_URL apply to NOTARX_DATABASE_DRIVER=mysql only")
		}
	case database.DriverMySQL:
		if !cfg.DatabaseExperimentalMySQL {
			return errors.New("NOTARX_DATABASE_DRIVER=mysql is experimental and not yet supported: set NOTARX_EXPERIMENTAL_MYSQL=true to start it anyway (the gateway refuses to start until every store port has a MySQL implementation)")
		}
		if _, _, err := database.ParseMySQLURL(cfg.DatabaseURL, cfg.DatabaseTLSCAFile); err != nil {
			return fmt.Errorf("NOTARX_DATABASE_URL: %w", err)
		}
		if cfg.DatabaseMigrateURL != "" {
			if _, _, err := database.ParseMySQLURL(cfg.DatabaseMigrateURL, cfg.DatabaseTLSCAFile); err != nil {
				return fmt.Errorf("NOTARX_DATABASE_MIGRATE_URL: %w", err)
			}
		}
	}

	pools := database.DefaultPoolSet(driver)
	if raw := strings.TrimSpace(os.Getenv("NOTARX_DATABASE_MAX_CONNECTIONS")); raw != "" {
		total, err := positiveIntEnv("NOTARX_DATABASE_MAX_CONNECTIONS", 0, false)
		if err != nil {
			return err
		}
		for _, key := range perPoolConnectionKeys {
			if strings.TrimSpace(os.Getenv(key)) != "" {
				return fmt.Errorf("NOTARX_DATABASE_MAX_CONNECTIONS cannot be combined with %s", key)
			}
		}
		if pools, err = database.SplitPoolBudget(driver, total); err != nil {
			return fmt.Errorf("NOTARX_DATABASE_MAX_CONNECTIONS: %w", err)
		}
	}
	cfg.DatabasePool, cfg.DatabaseCoordinatorPool = pools.Main, pools.Coordinator
	cfg.DatabaseArtifactLockPool, cfg.DatabaseNotificationPool = pools.ArtifactLock, pools.Notification
	cfg.DatabasePool.TLSCAFile = cfg.DatabaseTLSCAFile
	cfg.DatabaseCoordinatorPool.TLSCAFile = cfg.DatabaseTLSCAFile
	cfg.DatabaseArtifactLockPool.TLSCAFile = cfg.DatabaseTLSCAFile
	cfg.DatabaseNotificationPool.TLSCAFile = cfg.DatabaseTLSCAFile
	return nil
}

// DatabaseConnectionBudget is the number of connections the configured pools may open.
func (c Config) DatabaseConnectionBudget() int {
	return c.DatabasePool.MaxOpenConns + c.DatabaseCoordinatorPool.MaxOpenConns + c.DatabaseArtifactLockPool.MaxOpenConns + c.DatabaseNotificationPool.MaxOpenConns
}
