// Package mysqlbaseline generates the MySQL 8.4 baseline migration (ADR 0022, slice 3c)
// from the dialect-neutral schema catalogue (migrations/catalogue.yaml) and the decisions
// file (migrations/mysql-decisions.yaml), and the human-readable decision lists from the
// same inputs. The generator is pure: it reads no database, so make test can prove that the
// checked-in baseline is what the inputs produce, and make schema-parity proves that a
// migrated MySQL matches the catalogue.
package mysqlbaseline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mlops-ca/notarx/internal/schemaparity"
)

// LockBuckets is the number of rows of notarx_lock_buckets (ADR 0022).
const LockBuckets = 4096

// maxKeyBytes is the InnoDB DYNAMIC index key limit.
const maxKeyBytes = 3072

// maxIdentifier is the longest MySQL index or constraint name.
const maxIdentifier = 64

// Output is what the generator produces.
type Output struct {
	SQL string // migrations/mysql/000001_baseline.sql
	Doc string // docs/mysql-schema-decisions.md
}

// Generate builds the baseline and the decision lists. All problems are collected, so
// one run lists every object that still needs a decision.
func Generate(catalogue schemaparity.Catalogue, decisions Decisions) (Output, error) {
	g := &generator{catalogue: catalogue, decisions: decisions}
	g.run()
	if len(g.problems) > 0 {
		slices.Sort(g.problems)
		return Output{}, errors.New(strings.Join(slices.Compact(g.problems), "\n"))
	}
	return Output{SQL: g.sql.String(), Doc: g.doc()}, nil
}

type generator struct {
	catalogue schemaparity.Catalogue
	decisions Decisions
	problems  []string
	sql       strings.Builder
	tables    map[string]*tablePlan
	names     map[string]string // constraint names used so far, to keep them unique per database
	report    report
}

func (g *generator) problemf(format string, args ...any) {
	g.problems = append(g.problems, fmt.Sprintf(format, args...))
}

// tablePlan is everything the generator decides about one table.
type tablePlan struct {
	name      string
	table     schemaparity.Table
	indexed   map[string]bool   // columns that appear in a key or an index
	types     map[string]string // column -> MySQL type
	hashed    map[string]bool   // columns keyed by their SHA-256 column
	generated []string          // generated column definitions, in order
	lines     []string          // column, key, index and check lines in order
	flags     map[string]string // generated flag column -> expression
	autoInc   string
}

type report struct {
	skippedChecks  []string // "table.constraint: reason"
	autoSkipped    []string
	droppedIndexes []string
	indexActions   []string
	hashColumns    []string
	widths         []string
	arrayColumns   []string
	partitioned    []string
	seedTables     []string
}

func (g *generator) run() {
	g.tables = map[string]*tablePlan{}
	g.names = map[string]string{}
	g.validate()
	for _, name := range g.catalogue.TableNames() {
		g.tables[name] = &tablePlan{
			name: name, table: g.catalogue.Tables[name],
			indexed: map[string]bool{}, types: map[string]string{}, hashed: map[string]bool{}, flags: map[string]string{},
		}
	}
	specials := map[string][]specialPlan{}
	for _, name := range g.catalogue.TableNames() {
		specials[name] = g.planSpecials(g.tables[name])
	}
	for _, name := range g.catalogue.TableNames() {
		g.markIndexed(g.tables[name], specials[name])
	}
	for _, name := range g.catalogue.TableNames() {
		g.planTypes(g.tables[name])
	}
	for _, name := range g.catalogue.TableNames() {
		g.buildTable(g.tables[name], specials[name])
	}
	g.emit()
}

// validate rejects decisions that name objects the catalogue does not have, and
// catalogue objects that need a decision and have none.
func (g *generator) validate() {
	cat, dec := g.catalogue, g.decisions
	for key := range dec.Columns {
		g.requireColumn("columns", key)
	}
	for key := range dec.Defaults {
		g.requireColumn("defaults", key)
	}
	for key := range dec.Arrays {
		g.requireColumn("arrays", key)
	}
	specialNames := map[string]bool{}
	checkNames := map[string]bool{}
	for _, name := range cat.TableNames() {
		t := cat.Tables[name]
		for index := range t.Special {
			specialNames[index] = true
		}
		for check := range t.Checks {
			checkNames[check] = true
		}
		for _, column := range slices.Sorted(mapKeys(t.Columns)) {
			if strings.HasPrefix(t.Columns[column].Type, "array<") {
				if _, ok := dec.Arrays[name+"."+column]; !ok {
					g.problemf("%s.%s is a PostgreSQL array: add an entry under arrays: with the reason it is a JSON array", name, column)
				}
			}
		}
	}
	for index := range dec.Indexes {
		if !specialNames[index] {
			g.problemf("indexes: %s is not a special index of the catalogue", index)
		}
	}
	for check := range dec.Checks {
		if !checkNames[check] {
			g.problemf("checks: %s is not a check constraint of the catalogue", check)
		}
	}
	for trigger := range cat.Triggers {
		d, ok := dec.Triggers[trigger]
		switch {
		case !ok:
			g.problemf("trigger %s has no decision: add it under triggers:", trigger)
		case d.Disposition != DispositionDropped && d.Disposition != DispositionPorted && d.Disposition != DispositionKept:
			g.problemf("triggers: %s has the unknown disposition %q", trigger, d.Disposition)
		case strings.TrimSpace(d.Reason) == "":
			g.problemf("triggers: %s needs a reason", trigger)
		}
	}
	for trigger := range dec.Triggers {
		if _, ok := cat.Triggers[trigger]; !ok {
			g.problemf("triggers: %s is not a trigger of the catalogue", trigger)
		}
	}
	for _, view := range cat.Views {
		if d, ok := dec.Views[view]; !ok || strings.TrimSpace(d.Reason) == "" {
			g.problemf("view %s needs an entry with a reason under views:", view)
		}
	}
	for view := range dec.Views {
		if !slices.Contains(cat.Views, view) {
			g.problemf("views: %s is not a view of the catalogue", view)
		}
	}
	for table, columns := range dec.Seeds {
		if _, ok := cat.Tables[table]; !ok {
			g.problemf("seeds: %s is not a table", table)
		}
		for column := range columns {
			if _, ok := cat.Tables[table].Columns[column]; !ok {
				g.problemf("seeds: %s.%s is not a column", table, column)
			}
		}
	}
}

func (g *generator) requireColumn(section, key string) {
	table, column, ok := strings.Cut(key, ".")
	if _, found := g.catalogue.Tables[table].Columns[column]; !ok || !found {
		g.problemf("%s: %s is not a column of the catalogue", section, key)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// --- index specials -------------------------------------------------------------------

// specialPlan is the decision for one special index, with the parts of its definition.
type specialPlan struct {
	name       string
	action     string
	unique     bool
	columns    []keyPart
	predicate  string // PostgreSQL text, empty when the index is not partial
	flag       string // the generated column of ActionFlag
	generated  []GeneratedColumn
	reason     string
	definition string
}

// keyPart is one column of an index with its direction.
type keyPart struct {
	column string
	desc   bool
}

var (
	indexTail  = regexp.MustCompile(`(?: (?:text_pattern_ops|varchar_pattern_ops|gin_trgm_ops|public\.gin_trgm_ops|DESC|ASC|NULLS FIRST|NULLS LAST|COLLATE "[A-Za-z_.0-9-]+"))+$`)
	identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// parseIndexDefinition splits a normalized definition ("[unique ]USING btree (a, b DESC) WHERE (p)").
func parseIndexDefinition(definition string) (method string, items []string, predicate string, unique bool, err error) {
	text := definition
	if rest, ok := strings.CutPrefix(text, "unique "); ok {
		unique, text = true, rest
	}
	rest, ok := strings.CutPrefix(text, "USING ")
	if !ok {
		return "", nil, "", false, fmt.Errorf("unexpected index definition %q", definition)
	}
	method, rest, _ = strings.Cut(rest, " ")
	if !strings.HasPrefix(rest, "(") {
		return "", nil, "", false, fmt.Errorf("unexpected index definition %q", definition)
	}
	end := matchingParen(rest)
	if end < 0 {
		return "", nil, "", false, fmt.Errorf("unbalanced index definition %q", definition)
	}
	items = splitTopLevel(rest[1:end])
	tail := strings.TrimSpace(rest[end+1:])
	if i := strings.Index(tail, "WHERE "); i >= 0 {
		predicate = strings.TrimSpace(tail[i+len("WHERE "):])
	}
	return method, items, predicate, unique, nil
}

// matchingParen returns the index of the parenthesis that closes the one at text[0].
func matchingParen(text string) int {
	depth := 0
	quoted := false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevel(text string) []string {
	var items []string
	depth, start := 0, 0
	quoted := false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			items = append(items, strings.TrimSpace(text[start:i]))
			start = i + 1
		}
	}
	return append(items, strings.TrimSpace(text[start:]))
}

// planSpecials decides every special index of a table.
func (g *generator) planSpecials(t *tablePlan) []specialPlan {
	var plans []specialPlan
	for _, name := range slices.Sorted(mapKeys(t.table.Special)) {
		definition := t.table.Special[name]
		method, items, predicate, unique, err := parseIndexDefinition(definition)
		if err != nil {
			g.problemf("%s: index %s: %v", t.name, name, err)
			continue
		}
		plan := specialPlan{name: name, unique: unique, predicate: predicate, definition: definition}
		expression := false
		for _, item := range items {
			text := strings.TrimSpace(indexTail.ReplaceAllString(item, ""))
			part := keyPart{desc: strings.HasSuffix(item, " DESC")}
			text = strings.Trim(text, "\"")
			if !identifier.MatchString(text) {
				expression = true
				break
			}
			part.column = text
			plan.columns = append(plan.columns, part)
		}
		decision, decided := g.decisions.Indexes[name]
		switch {
		case decided:
			plan.action, plan.reason, plan.flag, plan.generated = decision.Action, decision.Reason, decision.Flag, decision.Generated
			if len(decision.Columns) > 0 {
				plan.columns = nil
				for _, c := range decision.Columns {
					plan.columns = append(plan.columns, keyPart{column: strings.TrimSuffix(c, " desc"), desc: strings.HasSuffix(c, " desc")})
				}
				expression = false
			}
			if strings.TrimSpace(decision.Reason) == "" {
				g.problemf("indexes: %s needs a reason", name)
			}
		case expression:
			g.problemf("index %s of %s is an expression index (%s): add an entry under indexes: (drop, or full with columns:)", name, t.name, definition)
			continue
		case unique && predicate != "":
			plan.action, plan.reason = ActionUniqueFlag, "a unique index over a predicate becomes a generated column that is NULL outside it (ADR 0022)"
		case method == "brin":
			plan.action, plan.reason = ActionDrop, "BRIN has no MySQL form: time-ordered scans use the primary key and the retention indexes"
		case method == "gin":
			plan.action, plan.reason = ActionFull, "no trigram index on MySQL: a plain index serves prefix search and ordered keyset listing (ADR 0022)"
		case unique:
			plan.action, plan.reason = ActionFull, "a unique index with an operator class: MySQL's bin collation orders the same way"
		case predicate != "":
			plan.action, plan.reason = ActionFull, "a partial index becomes a full index: MySQL has no partial index and the table is small or the predicate selective enough"
		default:
			plan.action, plan.reason = ActionFull, "an operator class or collation PostgreSQL needs for prefix search: MySQL's bin collation serves LIKE 'x%' from a plain index"
		}
		if expression && plan.action != ActionDrop {
			g.problemf("index %s of %s is an expression index with no replacement columns", name, t.name)
			continue
		}
		generatedNames := map[string]bool{}
		for _, c := range plan.generated {
			generatedNames[c.Name] = true
			if _, clash := t.table.Columns[c.Name]; clash || c.Type == "" || c.Expression == "" {
				g.problemf("indexes: %s: the generated column %q clashes with a column or lacks a type or expression", name, c.Name)
			}
		}
		for _, part := range plan.columns {
			if _, ok := t.table.Columns[part.column]; !ok && !generatedNames[part.column] {
				g.problemf("index %s of %s names the unknown column %s", name, t.name, part.column)
			}
		}
		switch plan.action {
		case ActionFull, ActionDrop:
		case ActionFlag:
			if plan.flag == "" || predicate == "" {
				g.problemf("indexes: %s: action flag needs flag: and a partial index", name)
			}
		case ActionUniqueFlag:
			if !unique || predicate == "" {
				g.problemf("indexes: %s: action unique-flag needs a partial unique index", name)
			}
		case ActionGenerated:
			if len(plan.generated) == 0 || len(decision.Columns) == 0 {
				g.problemf("indexes: %s: action generated needs generated: and columns:", name)
			}
		default:
			g.problemf("indexes: %s has the unknown action %q", name, plan.action)
		}
		plans = append(plans, plan)
	}
	return plans
}

// markIndexed records the columns that appear in a key or an index, whose text type must be indexable.
func (g *generator) markIndexed(t *tablePlan, specials []specialPlan) {
	mark := func(columns ...string) {
		for _, c := range columns {
			t.indexed[c] = true
		}
	}
	mark(t.table.PrimaryKey...)
	for _, set := range t.table.Unique {
		mark(set...)
	}
	for _, fk := range t.table.ForeignKeys {
		local, remote := parseForeignKey(fk)
		mark(local.columns...)
		parent := g.tables[remote.table]
		if parent != nil {
			parent.indexed[remote.columns[0]] = true
			for _, c := range remote.columns {
				parent.indexed[c] = true
			}
		}
	}
	for _, index := range t.table.Indexes {
		for _, part := range splitIndexColumns(index) {
			mark(part.column)
		}
	}
	for _, s := range specials {
		if s.action == ActionDrop {
			continue
		}
		for _, part := range s.columns {
			if _, real := t.table.Columns[part.column]; real {
				mark(part.column)
			}
		}
	}
}

func splitIndexColumns(index string) []keyPart {
	var parts []keyPart
	for _, item := range strings.Split(index, ",") {
		column, desc := strings.CutSuffix(item, " desc")
		parts = append(parts, keyPart{column: column, desc: desc})
	}
	return parts
}

type foreignSide struct {
	table   string
	columns []string
}

// parseForeignKey reads "a,b -> t(x,y) on delete cascade".
func parseForeignKey(fk string) (local, remote foreignSide) {
	text, _, _ := strings.Cut(fk, " on ")
	left, right, _ := strings.Cut(text, " -> ")
	local.columns = strings.Split(left, ",")
	table, columns, _ := strings.Cut(right, "(")
	remote.table = table
	remote.columns = strings.Split(strings.TrimSuffix(columns, ")"), ",")
	return local, remote
}

func foreignKeyActions(fk string) (onDelete, onUpdate string) {
	onDelete, onUpdate = "NO ACTION", "NO ACTION"
	_, rest, _ := strings.Cut(fk, " on ")
	if rest == "" {
		return
	}
	rest = " on " + rest
	if i := strings.Index(rest, " on delete "); i >= 0 {
		onDelete = strings.ToUpper(strings.SplitN(strings.TrimPrefix(rest[i:], " on delete "), " on ", 2)[0])
	}
	if i := strings.Index(rest, " on update "); i >= 0 {
		onUpdate = strings.ToUpper(strings.SplitN(strings.TrimPrefix(rest[i:], " on update "), " on ", 2)[0])
	}
	return
}

// --- types ----------------------------------------------------------------------------

const (
	uuidType       = "CHAR(36) CHARACTER SET ascii COLLATE ascii_bin"
	defaultDecimal = "DECIMAL(20,10)"
	hashType       = "BINARY(32)"
	defaultBytes   = 64
)

func (g *generator) planTypes(t *tablePlan) {
	for _, name := range slices.Sorted(mapKeys(t.table.Columns)) {
		col := t.table.Columns[name]
		key := t.name + "." + name
		decision := g.decisions.Columns[key]
		if reason, ok := g.decisions.HashNames[name]; ok && col.Type == "text" && t.indexed[name] && !decision.Hash && decision.Width == 0 {
			decision = ColumnDecision{Hash: true, Reason: reason}
		}
		if decision.Hash {
			if col.Type != "text" {
				g.problemf("columns: %s: only a text column can be hashed", key)
			}
			if strings.TrimSpace(decision.Reason) == "" {
				g.problemf("columns: %s needs a reason", key)
			}
			t.hashed[name] = true
			t.types[name] = "TEXT"
			g.report.hashColumns = append(g.report.hashColumns, fmt.Sprintf("| %s | %s |", key, decision.Reason))
			continue
		}
		if decision.Type != "" {
			t.types[name] = decision.Type
			continue
		}
		t.types[name] = g.mysqlType(t, name, col)
	}
}

// width returns the VARCHAR width of an indexed text column.
func (g *generator) width(table, column string) int {
	if d, ok := g.decisions.Columns[table+"."+column]; ok && d.Width > 0 {
		return d.Width
	}
	if w, ok := g.decisions.KeyWidths[column]; ok {
		return w
	}
	return g.decisions.DefaultKeyWidth
}

func (g *generator) mysqlType(t *tablePlan, name string, col schemaparity.Column) string {
	switch {
	case col.Type == "uuid":
		return uuidType
	case col.Type == "text":
		if t.indexed[name] {
			return fmt.Sprintf("VARCHAR(%d)", g.width(t.name, name))
		}
		return "TEXT"
	case col.Type == "bool":
		return "TINYINT(1)"
	case col.Type == "int16":
		return "SMALLINT"
	case col.Type == "int32":
		return "INT"
	case col.Type == "int64":
		return "BIGINT"
	case col.Type == "decimal":
		return defaultDecimal
	case col.Type == "float32":
		return "FLOAT"
	case col.Type == "float64":
		return "DOUBLE"
	case col.Type == "timestamp":
		return "DATETIME(6)"
	case col.Type == "date":
		return "DATE"
	case col.Type == "time":
		return "TIME(6)"
	case col.Type == "json", strings.HasPrefix(col.Type, "array<"):
		return "JSON"
	case col.Type == "bytes":
		if t.indexed[name] {
			return fmt.Sprintf("VARBINARY(%d)", defaultBytes)
		}
		return "LONGBLOB"
	}
	g.problemf("%s.%s has the logical type %s, which has no MySQL type", t.name, name, col.Type)
	return "TEXT"
}

// keyBytes is the width a column adds to an index key.
func (g *generator) keyBytes(t *tablePlan, column string) int {
	if base, ok := strings.CutSuffix(column, hashSuffix); ok && t.hashed[base] {
		return 32
	}
	typ := t.types[column]
	switch {
	case strings.HasPrefix(typ, "CHAR(36)"):
		return 36
	case strings.HasPrefix(typ, "VARCHAR("):
		var n int
		_, _ = fmt.Sscanf(typ, "VARCHAR(%d)", &n)
		return n * 4
	case strings.HasPrefix(typ, "VARBINARY("):
		var n int
		_, _ = fmt.Sscanf(typ, "VARBINARY(%d)", &n)
		return n
	case typ == "TINYINT(1)":
		return 1
	case typ == "SMALLINT":
		return 2
	case typ == "INT":
		return 4
	case typ == "BIGINT", typ == "DATETIME(6)":
		return 8
	case typ == "TEXT", typ == "JSON", typ == "LONGBLOB":
		return maxKeyBytes + 1 // not indexable
	}
	return 12
}

// --- naming ---------------------------------------------------------------------------

// shortName joins the parts with underscores and shortens the result to a MySQL identifier.
func shortName(prefix string, parts ...string) string {
	name := prefix + "_" + strings.Join(parts, "_")
	if len(name) <= maxIdentifier {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:maxIdentifier-9] + "_" + hex.EncodeToString(sum[:])[:8]
}

func quote(name string) string { return "`" + name + "`" }

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quote(n)
	}
	return strings.Join(quoted, ", ")
}

// claimName registers a database-wide constraint name and reports a clash.
func (g *generator) claimName(name, owner string) {
	if previous, ok := g.names[name]; ok && previous != owner {
		g.problemf("the constraint name %s is used by both %s and %s; MySQL needs it unique per database", name, previous, owner)
	}
	g.names[name] = owner
}
