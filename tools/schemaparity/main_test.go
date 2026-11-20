package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func exec(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(key string) string { return env[key] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{"no command": nil, "unknown command": {"bogus"}, "unknown flag": {"check", "-bogus"}, "positional": {"check", "extra"}} {
		if code, _, _ := exec(t, nil, args...); code != 2 {
			t.Errorf("%s: exit code %d; want 2", name, code)
		}
	}
	if code, _, stderr := exec(t, nil, "check", "-h"); code != 0 || !strings.Contains(stderr, "-mysql-url") {
		t.Errorf("help: %d %q", code, stderr)
	}
}

func TestMissingAndUnreachableDatabases(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"no postgres url":        {[]string{"generate", "-out", "unused.yaml"}, nil, "no postgres URL"},
		"unreachable postgres":   {[]string{"generate", "-out", "unused.yaml"}, map[string]string{"TEST_DATABASE_URL": "postgres://u:secret-pass@127.0.0.1:1/d?sslmode=disable&connect_timeout=1"}, "cannot reach the postgres database"},
		"missing catalogue":      {[]string{"check", "-catalogue", "missing.yaml"}, nil, "missing.yaml"},
		"check reaches postgres": {[]string{"check", "-catalogue", "../../migrations/catalogue.yaml", "-postgres-url", "postgres://u:secret-pass@127.0.0.1:1/d?sslmode=disable&connect_timeout=1"}, nil, "cannot reach the postgres database"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := exec(t, tc.env, tc.args...)
			if code != 1 || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "secret-pass") {
				t.Fatalf("exit %d, stderr %q; want 1 containing %q and no password", code, stderr, tc.want)
			}
		})
	}
	if _, err := open(context.Background(), "mysql", "u:p@tcp(h)/d"); err == nil || strings.Contains(err.Error(), "u:p") {
		t.Fatalf("err = %v", err)
	}
}
