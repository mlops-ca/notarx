package repository

import (
	"os"
	"testing"

	"github.com/mlops-ca/notarx/tools/sqllint"
)

// TestSQLTypedKeyLint runs the ADR 0023 section 4 ratchet in make test: no new
// col::text = $n comparison, counts in tools/sqllint/allowlist.txt only shrink,
// and uuid.New() stays inside internal/ids.
func TestSQLTypedKeyLint(t *testing.T) {
	result, err := sqllint.Scan("../..")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open("../../tools/sqllint/allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	allow, err := sqllint.ParseAllowlist(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range sqllint.Check(result, allow) {
		t.Error(problem)
	}
}
