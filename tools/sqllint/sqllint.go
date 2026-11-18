// Package sqllint enforces the typed-key rules of ADR 0023 section 4.
//
// Two rules are checked over the Go source below internal/:
//
//   - SQL string literals in non-test files must not compare a column cast to
//     text with a parameter (`col::text = $1`, `$1::text = col`, or `id::text` after
//     WHERE). Such comparisons defeat the btree on uuid columns. Existing
//     violations are recorded per file in an allowlist that may only shrink.
//   - `uuid.New()` is forbidden outside internal/ids so every new identifier is a
//     UUIDv7 created by ids.New.
package sqllint

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// IDsPackage is the only package allowed to call uuid.New directly.
const IDsPackage = "internal/ids"

var (
	castComparison = regexp.MustCompile(`::text\s*=\s*\$\d|\$\d+::text\s*=`)
	bareIDCast     = regexp.MustCompile(`\bid::text`)
	whereKeyword   = regexp.MustCompile(`(?i)\bwhere\b`)
)

// Result is the outcome of scanning a source tree.
type Result struct {
	// Casts maps a slash-separated, root-relative file path to its number of
	// text-cast comparisons in non-test SQL literals.
	Casts map[string]int
	// UUIDNew lists file:line positions of uuid.New() calls outside internal/ids.
	UUIDNew []string
}

// CountCasts returns the number of text-cast comparisons in one SQL string.
func CountCasts(sql string) int {
	type span struct{ start, end int }
	var spans []span
	for _, m := range castComparison.FindAllStringIndex(sql, -1) {
		spans = append(spans, span{m[0], m[1]})
	}
	if where := whereKeyword.FindStringIndex(sql); where != nil {
		for _, m := range bareIDCast.FindAllStringIndex(sql[where[1]:], -1) {
			spans = append(spans, span{where[1] + m[0], where[1] + m[1]})
		}
	}
	if len(spans) == 0 {
		return 0
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	count, end := 0, -1
	for _, s := range spans {
		// "id::text = $1" matches both rules; it is one violation.
		if s.start >= end {
			count++
		}
		if s.end > end {
			end = s.end
		}
	}
	return count
}

// Scan walks root/internal and applies both rules.
func Scan(root string) (Result, error) {
	result := Result{Casts: map[string]int{}}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		isTest := strings.HasSuffix(rel, "_test.go")
		inIDs := strings.HasPrefix(rel, IDsPackage+"/")
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BasicLit:
				if isTest || n.Kind != token.STRING {
					return true
				}
				if value, err := strconv.Unquote(n.Value); err == nil {
					if c := CountCasts(value); c > 0 {
						result.Casts[rel] += c
					}
				}
			case *ast.CallExpr:
				if inIDs {
					return true
				}
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "uuid" {
						result.UUIDNew = append(result.UUIDNew, set.Position(n.Pos()).String())
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	for i, position := range result.UUIDNew {
		if rel, err := filepath.Rel(root, position); err == nil {
			result.UUIDNew[i] = filepath.ToSlash(rel)
		}
	}
	sort.Strings(result.UUIDNew)
	return result, nil
}

// ParseAllowlist reads lines of "<path> <count>"; blank lines and lines
// starting with # are ignored.
func ParseAllowlist(r io.Reader) (map[string]int, error) {
	allow := map[string]int{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("allowlist line %d: want \"<path> <count>\", got %q", line, text)
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil || count <= 0 {
			return nil, fmt.Errorf("allowlist line %d: count must be a positive integer, got %q", line, fields[1])
		}
		if _, dup := allow[fields[0]]; dup {
			return nil, fmt.Errorf("allowlist line %d: duplicate entry for %s", line, fields[0])
		}
		allow[fields[0]] = count
	}
	return allow, scanner.Err()
}

// FormatAllowlist renders counts in the allowlist file format.
func FormatAllowlist(counts map[string]int) string {
	var b strings.Builder
	b.WriteString("# Ratchet for ADR 0023 section 4: SQL literals that compare a text-cast column with a\n")
	b.WriteString("# parameter. Counts may only decrease; remove a file when it reaches zero.\n")
	b.WriteString("# Regenerate lower counts with: go run ./tools/sqllint/cmd -update\n")
	for _, file := range sortedKeys(counts) {
		fmt.Fprintf(&b, "%s %d\n", file, counts[file])
	}
	return b.String()
}

// Check compares a scan with the allowlist and returns one message per problem.
func Check(result Result, allow map[string]int) []string {
	var problems []string
	for _, position := range result.UUIDNew {
		problems = append(problems, position+": uuid.New() is forbidden outside "+IDsPackage+"; use ids.New() (ADR 0023 section 4)")
	}
	for _, file := range sortedKeys(result.Casts) {
		actual, allowed := result.Casts[file], allow[file]
		switch {
		case actual > allowed:
			problems = append(problems, fmt.Sprintf("%s: %d text-cast comparison(s), allowlist permits %d; bind uuid parameters as uuid.UUID or $n::uuid instead of col::text = $n", file, actual, allowed))
		case actual < allowed:
			problems = append(problems, fmt.Sprintf("%s: allowlist permits %d but only %d remain; lower it with go run ./tools/sqllint/cmd -update", file, allowed, actual))
		}
	}
	for _, file := range sortedKeys(allow) {
		if _, ok := result.Casts[file]; !ok {
			problems = append(problems, fmt.Sprintf("%s: allowlist entry has no remaining violations; remove it with go run ./tools/sqllint/cmd -update", file))
		}
	}
	return problems
}

// Lowered returns the allowlist after a ratchet step: each file keeps its current
// count and files without violations are dropped. It reports an error when any
// count grew or a new file appeared, because the ratchet never goes up.
func Lowered(result Result, allow map[string]int) (map[string]int, error) {
	next := map[string]int{}
	for file, actual := range result.Casts {
		if actual > allow[file] {
			return nil, fmt.Errorf("%s has %d violations, allowlist permits %d: fix the code instead of raising the allowlist", file, actual, allow[file])
		}
		next[file] = actual
	}
	return next, nil
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
