// Package sqllint enforces the typed-key rules of ADR 0023 section 4.
//
// Four rules are checked over the Go source below internal/:
//
//   - SQL string literals in non-test files must not compare a column cast to
//     text with a parameter (`col::text = $1`, `$1::text = col`, or `id::text` after
//     WHERE). Such comparisons defeat the btree on uuid columns. Existing
//     violations are recorded per file in an allowlist that may only shrink.
//   - A statement whose select list holds `id::text` must not order by a bare `id`. PostgreSQL
//     resolves a bare name in ORDER BY to an output column first, so the statement sorts the
//     text instead of reading the uuid key in order and every page of a keyset listing adds a
//     Sort node. Qualify the column (`ORDER BY <table>.id`). A column list kept in a package
//     level string constant is expanded before the check. There is no allowlist.
//   - `uuid.New()` and `uuid.NewRandom()` (random UUIDv4), also under an import alias,
//     are forbidden outside internal/ids so every new identifier is a UUIDv7
//     created by ids.New. `uuid.Must(uuid.NewRandom())` is caught by the same rule.
//   - `uuid.NewString()` (also a random UUIDv4) in non-test files is counted per file
//     in a second allowlist that may only shrink (tools/sqllint/allowlist-uuid.txt):
//     a new call in a file without an allowance fails. Files convert to ids.New() in
//     waves; calls that are not row identifiers (leases, trace ids, temp names)
//     may stay and keep their count.
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

const uuidImportPath = "github.com/google/uuid"

var (
	castComparison = regexp.MustCompile(`::text\s*=\s*\$\d|\$\d+::text\s*=`)
	bareIDCast     = regexp.MustCompile(`\bid::text`)
	whereKeyword   = regexp.MustCompile(`(?i)\bwhere\b`)
	// endOfWhere ends the filter region: id::text after these is a select-list item.
	endOfWhere = regexp.MustCompile(`(?i)\b(returning|order\s+by|group\s+by|limit|union)\b`)
)

// Result is the outcome of scanning a source tree.
type Result struct {
	// Casts maps a slash-separated, root-relative file path to its number of
	// text-cast comparisons in non-test SQL literals.
	Casts map[string]int
	// BareIDOrder lists file:line positions of statements that select id::text and order by a bare id.
	BareIDOrder []string
	// UUIDNew lists file:line positions of uuid.New() calls outside internal/ids.
	UUIDNew []string
	// NewStrings maps a file path to its number of uuid.NewString() calls in
	// non-test files outside internal/ids.
	NewStrings map[string]int
}

// CountCasts returns the number of text-cast comparisons in one SQL string.
func CountCasts(sql string) int {
	type span struct{ start, end int }
	var spans []span
	for _, m := range castComparison.FindAllStringIndex(sql, -1) {
		spans = append(spans, span{m[0], m[1]})
	}
	if where := whereKeyword.FindStringIndex(sql); where != nil {
		region := sql[where[1]:]
		if end := endOfWhere.FindStringIndex(region); end != nil {
			region = region[:end[0]]
		}
		for _, m := range bareIDCast.FindAllStringIndex(region, -1) {
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

// Scan walks root/internal and applies the rules.
func Scan(root string) (Result, error) {
	result := Result{Casts: map[string]int{}, NewStrings: map[string]int{}}
	consts, err := packageStringConsts(root)
	if err != nil {
		return Result{}, err
	}
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
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
		dirConsts := consts[filepath.ToSlash(filepath.Dir(rel))]
		isTest := strings.HasSuffix(rel, "_test.go")
		inIDs := strings.HasPrefix(rel, IDsPackage+"/")
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return err
		}
		// The local names under which github.com/google/uuid is imported.
		uuidNames := map[string]bool{}
		for _, spec := range file.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil && path == uuidImportPath {
				name := "uuid"
				if spec.Name != nil {
					name = spec.Name.Name
				}
				uuidNames[name] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BinaryExpr:
				// A statement built from literals and string constants is checked as a whole.
				if isTest || n.Op != token.ADD {
					return true
				}
				if value, ok := evalStringExpr(n, dirConsts, 0); ok {
					if c := CountCasts(value); c > 0 {
						result.Casts[rel] += c
					}
					if BareIDOrder(value) {
						result.BareIDOrder = append(result.BareIDOrder, set.Position(n.Pos()).String())
					}
					return false
				}
			case *ast.BasicLit:
				if isTest || n.Kind != token.STRING {
					return true
				}
				if value, err := strconv.Unquote(n.Value); err == nil {
					if c := CountCasts(value); c > 0 {
						result.Casts[rel] += c
					}
					if BareIDOrder(value) {
						result.BareIDOrder = append(result.BareIDOrder, set.Position(n.Pos()).String())
					}
				}
			case *ast.CallExpr:
				if inIDs {
					return true
				}
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
					pkg, isPkg := sel.X.(*ast.Ident)
					switch {
					case !isPkg || !uuidNames[pkg.Name]:
					case sel.Sel.Name == "New" || sel.Sel.Name == "NewRandom":
						result.UUIDNew = append(result.UUIDNew, set.Position(n.Pos()).String())
					case sel.Sel.Name == "NewString" && !isTest:
						result.NewStrings[rel]++
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
	for i, position := range result.BareIDOrder {
		if rel, err := filepath.Rel(root, position); err == nil {
			result.BareIDOrder[i] = filepath.ToSlash(rel)
		}
	}
	sort.Strings(result.UUIDNew)
	sort.Strings(result.BareIDOrder)
	return result, nil
}

var (
	selectList    = regexp.MustCompile(`(?is)\bselect\b(.*?)\bfrom\b`)
	orderByClause = regexp.MustCompile(`(?is)\border\s+by\b(.*?)(\blimit\b|\bfor\s+(update|share)\b|\boffset\b|\)|$)`)
	bareIDTerm    = regexp.MustCompile(`(?i)(^|,)\s*id(\s+(asc|desc))?\s*(,|$)`)
)

// BareIDOrder reports whether a statement selects id::text and orders by an unqualified id. PostgreSQL
// resolves that name to the selected text column, not to the uuid column.
func BareIDOrder(sql string) bool {
	list := selectList.FindStringSubmatch(sql)
	if list == nil || !bareIDCast.MatchString(list[1]) {
		return false
	}
	for _, clause := range orderByClause.FindAllStringSubmatch(sql, -1) {
		if bareIDTerm.MatchString(clause[1]) {
			return true
		}
	}
	return false
}

// packageStringConsts maps each package directory below root/internal to its string constants, by name, so a
// statement built from a column-list constant can be checked as one string.
func packageStringConsts(root string) (map[string]map[string]ast.Expr, error) {
	out := map[string]map[string]ast.Expr{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, name := range value.Names {
					if out[dir] == nil {
						out[dir] = map[string]ast.Expr{}
					}
					out[dir][name.Name] = value.Values[i]
				}
			}
		}
		return nil
	})
	return out, err
}

// evalStringExpr folds a concatenation of string literals and package string constants. It reports false for
// anything else (a call, a variable, a constant of another package), so such a statement is only checked piece
// by piece.
func evalStringExpr(expr ast.Expr, consts map[string]ast.Expr, depth int) (string, bool) {
	if depth > 16 {
		return "", false
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.ParenExpr:
		return evalStringExpr(e.X, consts, depth+1)
	case *ast.Ident:
		value, ok := consts[e.Name]
		if !ok {
			return "", false
		}
		return evalStringExpr(value, consts, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := evalStringExpr(e.X, consts, depth+1)
		if !ok {
			return "", false
		}
		right, ok := evalStringExpr(e.Y, consts, depth+1)
		return left + right, ok
	}
	return "", false
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

// Check returns one message per violation of the text-cast and uuid.New rules; the
// text-cast rule has no allowlist (ADR 0023 slice 4c).
func Check(result Result) []string {
	var problems []string
	for _, position := range result.UUIDNew {
		problems = append(problems, position+": uuid.New() and uuid.NewRandom() are forbidden outside "+IDsPackage+"; use ids.New() (ADR 0023 section 4)")
	}
	for _, position := range result.BareIDOrder {
		problems = append(problems, position+": the statement selects id::text and orders by a bare id, which sorts the text; write ORDER BY <table>.id (ADR 0023 section 4)")
	}
	for _, file := range sortedKeys(result.Casts) {
		problems = append(problems, fmt.Sprintf("%s: %d text-cast comparison(s); bind uuid parameters as uuid.UUID or $n::uuid instead of col::text = $n", file, result.Casts[file]))
	}
	return problems
}

// CheckNewStrings compares the uuid.NewString() counts with their allowlist and
// returns one message per problem.
func CheckNewStrings(result Result, allow map[string]int) []string {
	var problems []string
	for _, file := range sortedKeys(result.NewStrings) {
		actual, allowed := result.NewStrings[file], allow[file]
		switch {
		case actual > allowed:
			problems = append(problems, fmt.Sprintf("%s: %d uuid.NewString() call(s), allowlist permits %d; create identifiers with ids.New() (ADR 0023 section 4)", file, actual, allowed))
		case actual < allowed:
			problems = append(problems, fmt.Sprintf("%s: uuid allowlist permits %d but only %d remain; lower it with go run ./tools/sqllint/cmd -update", file, allowed, actual))
		}
	}
	for _, file := range sortedKeys(allow) {
		if _, ok := result.NewStrings[file]; !ok {
			problems = append(problems, fmt.Sprintf("%s: uuid allowlist entry has no remaining calls; remove it with go run ./tools/sqllint/cmd -update", file))
		}
	}
	return problems
}

// LoweredNewStrings is Lowered for the uuid.NewString() counts.
func LoweredNewStrings(result Result, allow map[string]int) (map[string]int, error) {
	return lowerCounts(result.NewStrings, allow, "uuid.NewString() call(s)")
}

// FormatNewStringAllowlist renders the uuid.NewString() counts in the allowlist file format.
func FormatNewStringAllowlist(counts map[string]int) string {
	var b strings.Builder
	b.WriteString("# Ratchet for ADR 0023 section 4: uuid.NewString() (random UUIDv4) in non-test files.\n")
	b.WriteString("# Convert row identifiers to ids.New(); counts may only decrease, remove a file at zero.\n")
	b.WriteString("# Calls that are not row identifiers (lease tokens, trace ids, temporary names) may stay.\n")
	b.WriteString("# Regenerate lower counts with: go run ./tools/sqllint/cmd -update\n")
	for _, file := range sortedKeys(counts) {
		fmt.Fprintf(&b, "%s %d\n", file, counts[file])
	}
	return b.String()
}

// lowerCounts keeps each file's current count and refuses to raise any of them.
func lowerCounts(counts, allow map[string]int, what string) (map[string]int, error) {
	next := map[string]int{}
	for file, actual := range counts {
		if actual > allow[file] {
			return nil, fmt.Errorf("%s has %d %s, allowlist permits %d: fix the code instead of raising the allowlist", file, actual, what, allow[file])
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
