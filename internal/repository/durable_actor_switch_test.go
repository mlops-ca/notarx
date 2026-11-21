package repository

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/mlops-ca/notarx/internal/durableactor"
)

// The authority switch of every store must handle each durableactor class and no kind spelled as a string, so a new
// kind cannot be added to the job layer and silently skipped by one store.
func TestDurableActorAuthoritySwitchesCoverEveryClass(t *testing.T) {
	want := map[string]durableactor.Class{
		"ClassStatic": durableactor.ClassStatic, "ClassUser": durableactor.ClassUser, "ClassAPIKey": durableactor.ClassAPIKey,
		"ClassServiceAccountCredential": durableactor.ClassServiceAccountCredential, "ClassWorkload": durableactor.ClassWorkload,
	}
	if len(want) != len(durableactor.Classes()) {
		t.Fatalf("the test lists %d classes, durableactor has %d", len(want), len(durableactor.Classes()))
	}
	for file, switches := range map[string]int{
		"durable_actor_authority.go":                             2,
		"rubygems_governance_authority.go":                       1,
		"../rubygemspublication/pgstore/governance_authority.go": 1,
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		ast.Inspect(parsed, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || !switchesOnClassOf(sw) {
				return true
			}
			found++
			var got []string
			for _, stmt := range sw.Body.List {
				for _, e := range stmt.(*ast.CaseClause).List {
					sel, ok := e.(*ast.SelectorExpr)
					if !ok {
						t.Errorf("%s: case %T is not a durableactor class", file, e)
						continue
					}
					got = append(got, sel.Sel.Name)
				}
			}
			slices.Sort(got)
			var all []string
			for name := range want {
				all = append(all, name)
			}
			slices.Sort(all)
			if !slices.Equal(got, all) {
				t.Errorf("%s: authority switch handles %v, want %v", file, got, all)
			}
			return true
		})
		if found != switches {
			t.Errorf("%s: %d switches on durableactor.ClassOf, want %d", file, found, switches)
		}
	}
}

func switchesOnClassOf(sw *ast.SwitchStmt) bool {
	call, ok := sw.Tag.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "ClassOf"
}
