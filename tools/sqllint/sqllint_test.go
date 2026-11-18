package sqllint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountCasts(t *testing.T) {
	cases := []struct {
		sql  string
		want int
	}{
		{"SELECT 1", 0},
		{"SELECT repository_id::text FROM t WHERE repository_id=$1::uuid", 0},
		{"SELECT 1 FROM t WHERE repository_id::text=$1", 1},
		{"SELECT 1 FROM t WHERE repository_id::text = $1 AND other_id::text= $2", 2},
		{"SELECT 1 FROM t WHERE $1::text = repository_id::text", 1},
		{"SELECT 1 FROM t WHERE id::text = $1", 1},
		{"SELECT 1 FROM t WHERE x::text <> $1", 0},
		{"SELECT id::text FROM t", 0},
		{"SELECT id::text FROM t WHERE state='a'", 0},
		{"SELECT 1 FROM t WHERE lower(id::text) LIKE $1", 1},
		{"SELECT 1 FROM t WHERE parent_id::text = $1", 1},
		{"UPDATE t SET a=1 WHERE id=$1 RETURNING id::text,x", 0},
		{"SELECT 1 FROM t WHERE id::text = $1 ORDER BY id::text", 1},
		{"SELECT 1 FROM t WHERE state='a' ORDER BY id::text LIMIT 1", 0},
	}
	for _, c := range cases {
		if got := CountCasts(c.sql); got != c.want {
			t.Errorf("CountCasts(%q) = %d, want %d", c.sql, got, c.want)
		}
	}
}

func TestScanFindsCastsAndUUIDNew(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/a/a.go", "package a\nimport \"github.com/google/uuid\"\nvar q = `SELECT 1 FROM t WHERE x::text=$1`\nvar u = uuid.New()\nvar ok = uuid.NewString()\n")
	write("internal/a/a_test.go", "package a\nimport \"github.com/google/uuid\"\nvar q = `SELECT 1 FROM t WHERE x::text=$1`\nvar u = uuid.New()\n")
	write("internal/ids/ids.go", "package ids\nimport \"github.com/google/uuid\"\nvar u = uuid.New()\n")
	write("internal/c/c.go", "package c\nimport guuid \"github.com/google/uuid\"\nvar a = guuid.New()\nvar b, _ = guuid.NewRandom()\nvar ok = guuid.NewString()\n")
	// Not the uuid package: a local identifier named uuid must not trip the rule.
	write("internal/d/d.go", "package d\nvar uuid = struct{ New func() int }{}\nvar x = uuid.New()\n")
	result, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Casts["internal/a/a.go"] != 1 || len(result.Casts) != 1 {
		t.Fatalf("casts = %v", result.Casts)
	}
	want := []string{"internal/a/a.go:4:9", "internal/a/a_test.go:4:9", "internal/c/c.go:3:9", "internal/c/c.go:4:12"}
	if strings.Join(result.UUIDNew, ",") != strings.Join(want, ",") {
		t.Fatalf("uuid.New positions = %v, want %v", result.UUIDNew, want)
	}
	if _, err := Scan(filepath.Join(root, "missing")); err == nil {
		t.Fatal("Scan of a missing root must fail")
	}
	write("internal/b/b.go", "package b\nfunc (")
	if _, err := Scan(root); err == nil {
		t.Fatal("Scan must fail on unparsable Go")
	}
}

func TestAllowlistRoundTripAndErrors(t *testing.T) {
	allow, err := ParseAllowlist(strings.NewReader("# c\n\ninternal/a.go 3\ninternal/b.go 1\n"))
	if err != nil || allow["internal/a.go"] != 3 || allow["internal/b.go"] != 1 {
		t.Fatalf("allow = %v, err = %v", allow, err)
	}
	again, err := ParseAllowlist(strings.NewReader(FormatAllowlist(allow)))
	if err != nil || len(again) != 2 || again["internal/a.go"] != 3 {
		t.Fatalf("round trip = %v, err = %v", again, err)
	}
	for _, bad := range []string{"onlypath\n", "a.go x\n", "a.go 0\n", "a.go 1\na.go 2\n"} {
		if _, err := ParseAllowlist(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) should fail", bad)
		}
	}
}

func TestCheckRatchet(t *testing.T) {
	result := Result{Casts: map[string]int{"a.go": 2, "b.go": 1, "c.go": 4}, UUIDNew: []string{"internal/x/x.go:1:1"}}
	allow := map[string]int{"a.go": 2, "b.go": 3, "c.go": 1, "gone.go": 2}
	problems := strings.Join(Check(result, allow), "\n")
	for _, want := range []string{"are forbidden outside internal/ids", "c.go: 4 text-cast", "b.go: allowlist permits 3 but only 1", "gone.go: allowlist entry has no remaining"} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
	if strings.Contains(problems, "a.go:") {
		t.Errorf("a.go is at its allowance and must not be reported:\n%s", problems)
	}
	if got := Check(Result{Casts: map[string]int{"a.go": 1}}, map[string]int{"a.go": 1}); len(got) != 0 {
		t.Errorf("clean state reported %v", got)
	}
	if got := Check(Result{Casts: map[string]int{"new.go": 1}}, map[string]int{}); len(got) != 1 {
		t.Errorf("a new violating file must be reported, got %v", got)
	}
}

func TestLowered(t *testing.T) {
	next, err := Lowered(Result{Casts: map[string]int{"a.go": 1}}, map[string]int{"a.go": 3, "b.go": 2})
	if err != nil || len(next) != 1 || next["a.go"] != 1 {
		t.Fatalf("next = %v, err = %v", next, err)
	}
	if _, err := Lowered(Result{Casts: map[string]int{"a.go": 4}}, map[string]int{"a.go": 3}); err == nil {
		t.Fatal("raising a count must fail")
	}
	if _, err := Lowered(Result{Casts: map[string]int{"new.go": 1}}, map[string]int{}); err == nil {
		t.Fatal("adding a file must fail")
	}
}
