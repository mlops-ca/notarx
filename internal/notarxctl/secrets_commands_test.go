package notarxctl

import (
	"strings"
	"testing"
)

func TestSecretsResealRequiresConfirmation(t *testing.T) {
	t.Setenv(secretsDatabaseEnv, "postgres://user:hunter2@127.0.0.1:1/db")
	code, _, stderr := runCLI(t, "secrets", "reseal")
	if code != ExitUsage || !strings.Contains(stderr, "needs --yes") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestSecretsResealRefusesTheMySQLDriver(t *testing.T) {
	t.Setenv(secretsDatabaseEnv, "mysql://user:hunter2@127.0.0.1:1/db")
	t.Setenv("NOTARX_DATABASE_DRIVER", "mysql")
	code, stdout, stderr := runCLI(t, "secrets", "reseal", "--yes")
	if code != ExitUsage || !strings.Contains(stderr, "supports PostgreSQL only") || strings.Contains(stdout+stderr, "hunter2") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestSecretsResealRequiresDatabaseURL(t *testing.T) {
	t.Setenv(secretsDatabaseEnv, "")
	code, _, stderr := runCLI(t, "secrets", "reseal", "--yes")
	if code != ExitUsage || !strings.Contains(stderr, secretsDatabaseEnv) {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestSecretsResealRequiresEncryptionKey(t *testing.T) {
	t.Setenv(secretsDatabaseEnv, "postgres://user:hunter2@127.0.0.1:1/db")
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "")
	code, _, stderr := runCLI(t, "secrets", "reseal", "--dry-run")
	if code != ExitUsage || !strings.Contains(stderr, "NOTARX_SETTINGS_ENCRYPTION_KEY") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestSecretsResealNeverPrintsDatabasePassword(t *testing.T) {
	t.Setenv(secretsDatabaseEnv, "postgres://user:hunter2@127.0.0.1:1/db?connect_timeout=1")
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	code, stdout, stderr := runCLI(t, "secrets", "reseal", "--yes", "--timeout", "5s")
	if code == ExitOK {
		t.Fatalf("an unreachable database must fail; stdout=%s", stdout)
	}
	if strings.Contains(stdout+stderr, "hunter2") {
		t.Fatalf("database password leaked: %s %s", stdout, stderr)
	}
}
