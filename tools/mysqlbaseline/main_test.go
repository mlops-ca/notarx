package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoFile(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func TestGenerateWritesAndChecksTheBaseline(t *testing.T) {
	dir := t.TempDir()
	baseline, doc := filepath.Join(dir, "000001_baseline.sql"), filepath.Join(dir, "decisions.md")
	args := []string{"-catalogue", repoFile("migrations", "catalogue.yaml"), "-decisions", repoFile("migrations", "mysql-decisions.yaml"), "-baseline", baseline, "-doc", doc}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	// What the tool writes is what is checked in, and the check passes.
	checkedIn, err := os.ReadFile(repoFile("migrations", "mysql", "000001_baseline.sql"))
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(baseline)
	if err != nil || !bytes.Equal(checkedIn, written) {
		t.Fatalf("the written baseline differs from the checked-in one: %v", err)
	}
	stdout.Reset()
	if code := run(append(args, "-check"), &stdout, &stderr); code != 0 {
		t.Fatalf("-check failed on fresh files: %s", stderr.String())
	}
	if err := os.WriteFile(baseline, []byte("-- stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run(append(args, "-check"), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "out of date") {
		t.Fatalf("-check accepted a stale file: %d %s", code, stderr.String())
	}
}

func TestRunReportsBadInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for name, args := range map[string][]string{
		"unknown flag":      {"-bogus"},
		"missing catalogue": {"-catalogue", filepath.Join(t.TempDir(), "missing.yaml")},
		"missing decisions": {"-catalogue", repoFile("migrations", "catalogue.yaml"), "-decisions", filepath.Join(t.TempDir(), "missing.yaml")},
	} {
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	// A decisions file with no entries leaves the catalogue without decisions.
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, []byte("version: 1\ndefaultKeyWidth: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"-catalogue", repoFile("migrations", "catalogue.yaml"), "-decisions", empty}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "needs decisions") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	missing := filepath.Join(t.TempDir(), "absent.sql")
	if code := run([]string{"-catalogue", repoFile("migrations", "catalogue.yaml"), "-decisions", repoFile("migrations", "mysql-decisions.yaml"), "-baseline", missing, "-doc", missing, "-check"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-check of a missing file = %d", code)
	}
}
