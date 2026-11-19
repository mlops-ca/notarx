package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	for _, key := range append([]string{
		"NOTARX_DATABASE_DRIVER", "NOTARX_DATABASE_TLS_CA_FILE", "NOTARX_DATABASE_MAX_CONNECTIONS",
		"NOTARX_DATABASE_POLL_INTERVAL", "NOTARX_DATABASE_MIGRATE_URL", "NOTARX_EXPERIMENTAL_MYSQL",
	}, perPoolConnectionKeys...) {
		t.Setenv(key, "")
	}
}

func TestDatabaseDefaultsKeepPostgreSQLBehaviour(t *testing.T) {
	setValidNativeEnv(t)
	clearDatabaseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseDriver != "postgres" || cfg.DatabasePollInterval != 500*time.Millisecond || cfg.DatabaseExperimentalMySQL {
		t.Fatalf("defaults: %q %v %v", cfg.DatabaseDriver, cfg.DatabasePollInterval, cfg.DatabaseExperimentalMySQL)
	}
	if cfg.DatabasePool.MaxOpenConns != 32 || cfg.DatabaseCoordinatorPool.MaxOpenConns != 8 || cfg.DatabaseArtifactLockPool.MaxOpenConns != 4 || cfg.DatabaseNotificationPool.MaxOpenConns != 2 {
		t.Fatalf("pools: %#v", cfg)
	}
	if cfg.DatabaseConnectionBudget() != 46 {
		t.Fatalf("budget = %d", cfg.DatabaseConnectionBudget())
	}
}

func TestDatabaseConfigurationErrors(t *testing.T) {
	const mysqlURL = "mysql://notarx:secret-pw@db.internal:3306/notarx?tls=verify-full"
	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badCA, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unknown driver", map[string]string{"NOTARX_DATABASE_DRIVER": "oracle"}, "NOTARX_DATABASE_DRIVER must be one of postgres, mysql"},
		{"mysql without gate", map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_DATABASE_URL": mysqlURL}, "NOTARX_EXPERIMENTAL_MYSQL=true"},
		{"gate not boolean", map[string]string{"NOTARX_EXPERIMENTAL_MYSQL": "maybe"}, "NOTARX_EXPERIMENTAL_MYSQL must be true or false"},
		{"mysql driver DSN", map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_EXPERIMENTAL_MYSQL": "true", "NOTARX_DATABASE_URL": "notarx:secret-pw@tcp(db:3306)/notarx"}, "driver DSN form is not accepted"},
		{"mysql postgres URL", map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_EXPERIMENTAL_MYSQL": "true"}, "mysql:// form"},
		{"mysql bad CA", map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_EXPERIMENTAL_MYSQL": "true", "NOTARX_DATABASE_URL": mysqlURL, "NOTARX_DATABASE_TLS_CA_FILE": badCA}, "no PEM certificate"},
		{"mysql bad migrate URL", map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_EXPERIMENTAL_MYSQL": "true", "NOTARX_DATABASE_URL": mysqlURL, "NOTARX_DATABASE_MIGRATE_URL": "root:secret-pw@tcp(db)/notarx"}, "NOTARX_DATABASE_MIGRATE_URL"},
		{"postgres with CA file", map[string]string{"NOTARX_DATABASE_TLS_CA_FILE": badCA}, "apply to NOTARX_DATABASE_DRIVER=mysql only"},
		{"postgres with migrate URL", map[string]string{"NOTARX_DATABASE_MIGRATE_URL": mysqlURL}, "apply to NOTARX_DATABASE_DRIVER=mysql only"},
		{"postgres bad URL", map[string]string{"NOTARX_DATABASE_URL": "not a url"}, "NOTARX_DATABASE_URL is not a valid URL"},
		{"max connections not a number", map[string]string{"NOTARX_DATABASE_MAX_CONNECTIONS": "many"}, "NOTARX_DATABASE_MAX_CONNECTIONS must be a positive integer"},
		{"max connections too small", map[string]string{"NOTARX_DATABASE_MAX_CONNECTIONS": "3"}, "at least 4"},
		{"max connections with per pool key", map[string]string{"NOTARX_DATABASE_MAX_CONNECTIONS": "20", "NOTARX_DATABASE_MAX_OPEN_CONNS": "10"}, "cannot be combined with NOTARX_DATABASE_MAX_OPEN_CONNS"},
		{"poll interval invalid", map[string]string{"NOTARX_DATABASE_POLL_INTERVAL": "soon"}, "NOTARX_DATABASE_POLL_INTERVAL must be a positive duration"},
		{"poll interval too small", map[string]string{"NOTARX_DATABASE_POLL_INTERVAL": "1ms"}, "NOTARX_DATABASE_POLL_INTERVAL must be between"},
		{"poll interval too large", map[string]string{"NOTARX_DATABASE_POLL_INTERVAL": "1m"}, "NOTARX_DATABASE_POLL_INTERVAL must be between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidNativeEnv(t)
			clearDatabaseEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-pw") {
				t.Fatalf("error leaks the password: %v", err)
			}
		})
	}
}

func TestDatabaseMySQLConfiguration(t *testing.T) {
	setValidNativeEnv(t)
	clearDatabaseEnv(t)
	t.Setenv("NOTARX_DATABASE_DRIVER", "MySQL")
	t.Setenv("NOTARX_EXPERIMENTAL_MYSQL", "true")
	t.Setenv("NOTARX_DATABASE_URL", "mysql://notarx:pw@db.internal/notarx")
	t.Setenv("NOTARX_DATABASE_MIGRATE_URL", "mysql://ddl:pw@db.internal/notarx?tls=required")
	t.Setenv("NOTARX_DATABASE_POLL_INTERVAL", "250ms")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseDriver != "mysql" || !cfg.DatabaseExperimentalMySQL || cfg.DatabasePollInterval != 250*time.Millisecond {
		t.Fatalf("mysql configuration: %#v", cfg)
	}
	if cfg.DatabaseConnectionBudget() != 44 || cfg.DatabaseNotificationPool.MaxOpenConns != 0 {
		t.Fatalf("MySQL budget %d notification %d", cfg.DatabaseConnectionBudget(), cfg.DatabaseNotificationPool.MaxOpenConns)
	}

	t.Setenv("NOTARX_DATABASE_MAX_CONNECTIONS", "22")
	cfg, err = Load()
	if err != nil || cfg.DatabaseConnectionBudget() != 22 || cfg.DatabasePool.MaxOpenConns != 16 {
		t.Fatalf("split budget: %v %#v", err, cfg.DatabasePool)
	}

	t.Setenv("NOTARX_DATABASE_MAX_CONNECTIONS", "46")
	t.Setenv("NOTARX_DATABASE_DRIVER", "postgres")
	t.Setenv("NOTARX_DATABASE_URL", "postgres://notarx:pw@db:5432/notarx")
	t.Setenv("NOTARX_DATABASE_MIGRATE_URL", "")
	cfg, err = Load()
	if err != nil || cfg.DatabaseNotificationPool.MaxOpenConns != 2 || cfg.DatabaseConnectionBudget() != 46 {
		t.Fatalf("postgres split: %v %#v", err, cfg.DatabaseNotificationPool)
	}
}
