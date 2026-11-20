package migrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func runCLI(t *testing.T, values map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunCLIWithEnv(context.Background(), args, env(values), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001_baseline.sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCLIUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":        {"--bogus"},
		"positional argument": {"extra"},
		"completed needs fix": {"--completed", "1"},
		"non-positive wait":   {"--lock-wait", "0s"},
		"lint unknown flag":   {"lint", "--bogus"},
		"lint positional":     {"lint", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := runCLI(t, nil, args...); code != 2 {
				t.Fatalf("exit code %d; want 2", code)
			}
		})
	}
	if code, _, stderr := runCLI(t, nil, "-h"); code != 0 || !strings.Contains(stderr, "usage: notarx migrate") {
		t.Fatalf("help: %d %q", code, stderr)
	}
	if code, _, _ := runCLI(t, nil, "lint", "-h"); code != 0 {
		t.Fatalf("lint help: %d", code)
	}
}

func TestCLIConfigurationErrors(t *testing.T) {
	dir := migrationsDir(t)
	cases := map[string]struct {
		env  map[string]string
		args []string
		code int
		text string
	}{
		"bad driver":      {map[string]string{"NOTARX_DATABASE_DRIVER": "oracle"}, nil, 2, "must be postgres or mysql"},
		"no url":          {nil, []string{"--dir", dir}, 1, "NOTARX_DATABASE_URL is not set"},
		"no migrations":   {map[string]string{"NOTARX_DATABASE_URL": "postgres://u:secret-pass@127.0.0.1:1/d"}, []string{"--dir", filepath.Join(dir, "missing")}, 1, "read migrations"},
		"bad mysql url":   {map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_DATABASE_URL": "u:secret-pass@tcp(h)/d"}, []string{"--dir", dir}, 1, "mysql:// form"},
		"unreachable pg":  {map[string]string{"NOTARX_DATABASE_URL": "postgres://u:secret-pass@127.0.0.1:1/d?sslmode=disable&connect_timeout=1"}, []string{"--dir", dir}, 1, "cannot reach the database"},
		"unreachable my":  {map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_DATABASE_URL": "mysql://u:secret-pass@127.0.0.1:1/d"}, []string{"--dir", dir}, 1, "cannot reach the database"},
		"migrate url use": {map[string]string{"NOTARX_DATABASE_DRIVER": "mysql", "NOTARX_DATABASE_URL": "mysql://u:p@127.0.0.1:1/d", "NOTARX_DATABASE_MIGRATE_URL": "u:secret-pass@tcp(h)/d"}, []string{"--dir", dir}, 1, "mysql:// form"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tc.env, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.text) {
				t.Fatalf("exit %d stderr %q; want %d containing %q", code, stderr, tc.code, tc.text)
			}
			if strings.Contains(stderr, "secret-pass") {
				t.Fatalf("the password leaked: %q", stderr)
			}
		})
	}
}

func TestMigrationDirDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := migrationDir("x", "postgres", env(nil)); got != "x" {
		t.Fatal(got)
	}
	if got := migrationDir("", "postgres", env(map[string]string{"NOTARX_MIGRATIONS_DIR": "/share/migrations"})); got != "/share/migrations" {
		t.Fatal(got)
	}
	if got := migrationDir("", "postgres", env(nil)); got != "migrations" {
		t.Fatal(got)
	}
	if err := os.MkdirAll(filepath.Join("migrations", "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := migrationDir("", "postgres", env(nil)); got != filepath.Join("migrations", "postgres") {
		t.Fatal(got)
	}
	if got := migrationDir("", "mysql", env(nil)); got != "migrations" {
		t.Fatal(got)
	}
}

func TestCLILint(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("000001_baseline.sql", "CREATE TABLE t (id INT);")
	write("000002_ok.sql", "UPDATE t SET id = 1 WHERE id = 0;")
	if code, stdout, _ := runCLI(t, nil, "lint", "--dir", dir); code != 0 || !strings.Contains(stdout, "re-runnable") {
		t.Fatalf("clean directory: %d %q", code, stdout)
	}
	write("000003_bad.sql", "INSERT INTO t (id) VALUES (3);")
	code, _, stderr := runCLI(t, nil, "lint", "--dir", dir)
	if code != 1 || !strings.Contains(stderr, "000003_bad.sql: statement 1") || !strings.Contains(stderr, "1 migration finding(s)") {
		t.Fatalf("non-idempotent fixture: %d %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, nil, "lint", "--dir", filepath.Join(dir, "missing")); code != 1 || !strings.Contains(stderr, "read migrations") {
		t.Fatalf("missing directory: %d %q", code, stderr)
	}
}
