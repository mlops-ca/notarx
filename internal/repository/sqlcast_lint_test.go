package repository

import (
	"os"
	"testing"

	"github.com/mlops-ca/notarx/tools/sqllint"
)

// TestSQLTypedKeyLint runs the ADR 0023 section 4 ratchet in make test: no
// col::text = $n comparison (no allowlist),
// uuid.New() stays inside internal/ids, and uuid.NewString() counts in
// tools/sqllint/allowlist-uuid.txt only shrink.
func TestSQLTypedKeyLint(t *testing.T) {
	result, err := sqllint.Scan("../..")
	if err != nil {
		t.Fatal(err)
	}
	uuidFile, err := os.Open("../../tools/sqllint/allowlist-uuid.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = uuidFile.Close() }()
	uuidAllow, err := sqllint.ParseAllowlist(uuidFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range sqllint.Check(result) {
		t.Error(problem)
	}
	for _, problem := range sqllint.CheckNewStrings(result, uuidAllow) {
		t.Error(problem)
	}
}
