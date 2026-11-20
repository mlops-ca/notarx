package mysqlbaseline

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mlops-ca/notarx/internal/schemaparity"
)

// indexName shortens prefix and parts to a MySQL index name that no other index of the table has:
// the column list alone cannot tell two indexes apart (a and a DESC, or the helper key of an AUTO_INCREMENT column).
func (t *tablePlan) indexName(prefix string, parts ...string) string {
	if t.usedIndexes == nil {
		t.usedIndexes = map[string]bool{}
	}
	name := shortName(prefix, parts...)
	for n := 2; t.usedIndexes[name]; n++ {
		name = shortName(prefix, append(slices.Clone(parts), strconv.Itoa(n))...)
	}
	t.usedIndexes[name] = true
	return name
}

func (t *tablePlan) keyColumn(column string) string {
	if t.hashed[column] {
		return column + "_sha256"
	}
	return column
}

func (t *tablePlan) keyColumns(columns []string) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = t.keyColumn(c)
	}
	return out
}

func (g *generator) buildTable(t *tablePlan, specials []specialPlan) {
	var lines []string
	for _, name := range slices.Sorted(mapKeys(t.table.Columns)) {
		lines = append(lines, g.columnLine(t, name))
	}
	for _, name := range slices.Sorted(mapKeys(t.hashed)) {
		col := t.table.Columns[name]
		null := "NOT NULL"
		if col.Nullable {
			null = "NULL"
		}
		if _, clash := t.table.Columns[name+hashSuffix]; clash {
			g.problemf("%s.%s already exists: it cannot carry the hash of %s", t.name, name+hashSuffix, name)
		}
		lines = append(lines, fmt.Sprintf("%s %s GENERATED ALWAYS AS (UNHEX(SHA2(%s, 256))) STORED %s", quote(name+hashSuffix), hashType, quote(name), null))
	}

	var keys []string
	checkKey := func(owner string, columns []string) {
		total := 0
		for _, c := range columns {
			total += g.keyBytes(t, c)
			if t.types[c] == "" && !t.hashed[strings.TrimSuffix(c, hashSuffix)] {
				g.problemf("%s: %s names the unknown column %s", t.name, owner, c)
			}
		}
		if total > maxKeyBytes {
			g.problemf("%s: %s needs %d key bytes; InnoDB allows %d (narrow a width or hash a column)", t.name, owner, total, maxKeyBytes)
		}
		if len(columns) > 16 {
			g.problemf("%s: %s has %d columns; InnoDB allows 16", t.name, owner, len(columns))
		}
	}
	pk := t.keyColumns(t.table.PrimaryKey)
	checkKey("the primary key", pk)
	keys = append(keys, "PRIMARY KEY ("+quoteAll(pk)+")")
	for _, set := range t.table.Unique {
		columns := t.keyColumns(set)
		name := t.indexName("uq_"+t.name, columns...)
		checkKey("unique key "+name, columns)
		keys = append(keys, "UNIQUE KEY "+quote(name)+" ("+quoteAll(columns)+")")
	}
	for _, index := range t.table.Indexes {
		parts := splitIndexColumns(index)
		columns := make([]string, len(parts))
		items := make([]string, len(parts))
		nameParts := make([]string, len(parts))
		for i, p := range parts {
			columns[i] = t.keyColumn(p.column)
			items[i] = quote(columns[i])
			nameParts[i] = columns[i]
			if p.desc {
				items[i] += " DESC"
				nameParts[i] += "_desc"
			}
		}
		name := t.indexName("ix_"+t.name, nameParts...)
		checkKey("index "+name, columns)
		keys = append(keys, "KEY "+quote(name)+" ("+strings.Join(items, ", ")+")")
	}
	if t.autoInc != "" && !slices.Contains(keys, "PRIMARY KEY ("+quote(t.autoInc)+")") && !strings.HasPrefix(strings.TrimPrefix(keys[0], "PRIMARY KEY ("), quote(t.autoInc)) {
		// AUTO_INCREMENT needs a key that starts with the column (the primary key here starts with a partition column).
		keys = append(keys, "KEY "+quote(t.indexName("ix_"+t.name, t.autoInc))+" ("+quote(t.autoInc)+")")
	}
	for _, s := range specials {
		line, generated := g.specialLines(t, s, checkKey)
		if line != "" {
			keys = append(keys, line)
		}
		lines = append(lines, generated...)
	}
	lines = append(lines, keys...)
	lines = append(lines, g.checkLines(t)...)
	t.lines = lines
	if t.table.Partitioned {
		g.report.partitioned = append(g.report.partitioned, t.name)
	}
}

const hashSuffix = "_sha256"

func (g *generator) columnLine(t *tablePlan, name string) string {
	col := t.table.Columns[name]
	var b strings.Builder
	b.WriteString(quote(name) + " " + t.types[name])
	if col.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	clause, auto := g.defaultClause(t, name, col)
	if auto {
		b.WriteString(" AUTO_INCREMENT")
		t.autoInc = name
	} else if clause != "" {
		b.WriteString(" DEFAULT " + clause)
	}
	return b.String()
}

var (
	numberLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	stringLiteral = regexp.MustCompile(`^'(?:[^']|'')*'$`)
	zoneSuffix    = regexp.MustCompile(`[+-]00(:00)?'$`)
)

// defaultClause returns the MySQL default of a column, without the DEFAULT keyword, and
// whether the column is an AUTO_INCREMENT identity.
func (g *generator) defaultClause(t *tablePlan, name string, col schemaparity.Column) (string, bool) {
	key := t.name + "." + name
	if clause, ok := g.decisions.Defaults[key]; ok {
		return clause, false
	}
	normalized, ok := t.table.Defaults[name]
	if !ok {
		return "", false
	}
	typ := t.types[name]
	needsParens := typ == "TEXT" || typ == "JSON" || typ == "LONGBLOB"
	wrap := func(s string) string {
		if needsParens {
			return "(" + s + ")"
		}
		return s
	}
	switch {
	case normalized == "identity":
		if typ != "BIGINT" && typ != "INT" && typ != "SMALLINT" {
			g.problemf("%s: an identity default needs an integer column", key)
		}
		return "", true
	case normalized == "now()":
		return "CURRENT_TIMESTAMP(6)", false
	case normalized == "uuid()":
		return "(UUID())", false
	case normalized == "true" || normalized == "false":
		return map[string]string{"true": "1", "false": "0"}[normalized], false
	case numberLiteral.MatchString(normalized):
		return normalized, false
	case stringLiteral.MatchString(normalized):
		body := normalized[1 : len(normalized)-1]
		// A backslash escapes in a MySQL string literal and not in PostgreSQL's.
		escaped := strings.ReplaceAll(normalized, "\\", "\\\\")
		switch {
		case col.Type == "bytes":
			if hex, ok := strings.CutPrefix(body, "\\x"); ok {
				return "(X'" + hex + "')", false
			}
		case col.Type == "json" || strings.HasPrefix(col.Type, "array<"):
			return "(CAST(" + escaped + " AS JSON))", false
		case col.Type == "timestamp":
			return zoneSuffix.ReplaceAllString(escaped, "'"), false
		case col.Type == "bool":
			if value, ok := map[string]string{"t": "1", "true": "1", "f": "0", "false": "0"}[body]; ok {
				return value, false
			}
			g.problemf("default of %s is the boolean text %q: add it under defaults:", key, body)
			return "", false
		}
		return wrap(escaped), false
	}
	g.problemf("default of %s is %q, which the generator cannot translate: add it under defaults:", key, normalized)
	return "", false
}

// specialLines builds the KEY line of a special index and the generated columns it needs.
func (g *generator) specialLines(t *tablePlan, s specialPlan, checkKey func(string, []string)) (string, []string) {
	switch s.action {
	case ActionDrop:
		g.report.droppedIndexes = append(g.report.droppedIndexes, fmt.Sprintf("| %s | %s | %s |", t.name, s.name, s.reason))
		g.report.indexActions = append(g.report.indexActions, fmt.Sprintf("| %s | %s | %s | %s |", t.name, s.name, ActionDrop, s.reason))
		return "", nil
	case ActionFull:
		if slices.ContainsFunc(s.columns, func(p keyPart) bool { return t.hashed[p.column] }) {
			reason := "the column is keyed by its SHA-256, so prefix and ordered scans have no index; the owning store port adds a prefix index where a query needs it"
			g.report.droppedIndexes = append(g.report.droppedIndexes, fmt.Sprintf("| %s | %s | %s |", t.name, s.name, reason))
			g.report.indexActions = append(g.report.indexActions, fmt.Sprintf("| %s | %s | %s | %s |", t.name, s.name, ActionDrop, reason))
			return "", nil
		}
		columns, items := g.indexItems(t, s.columns)
		checkKey("index "+s.name, columns)
		g.report.indexActions = append(g.report.indexActions, fmt.Sprintf("| %s | %s | %s | %s |", t.name, s.name, ActionFull, s.reason))
		return "KEY " + quote(t.indexName("ix", s.name)) + " (" + strings.Join(items, ", ") + ")", nil
	case ActionGenerated:
		var generated []string
		for _, c := range s.generated {
			null := "NOT NULL"
			if c.Nullable {
				null = "NULL"
			}
			generated = append(generated, fmt.Sprintf("%s %s GENERATED ALWAYS AS (%s) VIRTUAL %s", quote(c.Name), c.Type, c.Expression, null))
			t.types[c.Name] = c.Type
		}
		columns, items := g.indexItems(t, s.columns)
		checkKey("index "+s.name, columns)
		g.report.indexActions = append(g.report.indexActions, fmt.Sprintf("| %s | %s | %s | %s |", t.name, s.name, ActionGenerated, s.reason))
		kind, prefix := "KEY ", "ix"
		if s.unique {
			kind, prefix = "UNIQUE KEY ", "uq"
		}
		return kind + quote(t.indexName(prefix, s.name)) + " (" + strings.Join(items, ", ") + ")", generated
	case ActionFlag, ActionUniqueFlag:
		translated, err := Translate(s.predicate)
		if err != nil {
			g.problemf("index %s of %s: predicate %q: %v", s.name, t.name, s.predicate, err)
			return "", nil
		}
		for _, c := range translated.Columns {
			if _, ok := t.table.Columns[c]; !ok {
				g.problemf("index %s of %s: the predicate names the unknown column %s", s.name, t.name, c)
			}
		}
		flag := s.flag
		expression := "IF(" + translated.SQL + ", 1, 0)"
		if s.action == ActionUniqueFlag {
			flag = shortName("live", s.name)
			expression = "IF(" + translated.SQL + ", 1, NULL)"
		}
		if _, clash := t.table.Columns[flag]; clash {
			g.problemf("index %s of %s: the generated column %s clashes with a column", s.name, t.name, flag)
		}
		var generated []string
		if previous, ok := t.flags[flag]; ok && previous != expression {
			g.problemf("index %s of %s: the flag column %s has two different predicates", s.name, t.name, flag)
		} else if !ok {
			t.flags[flag] = expression
			null := "NOT NULL"
			if s.action == ActionUniqueFlag {
				null = "NULL"
			}
			generated = append(generated, fmt.Sprintf("%s TINYINT(1) GENERATED ALWAYS AS (%s) VIRTUAL %s", quote(flag), expression, null))
		}
		columns, items := g.indexItems(t, s.columns)
		var line string
		if s.action == ActionFlag {
			columns = append([]string{flag}, columns...)
			items = append([]string{quote(flag)}, items...)
			line = "KEY " + quote(t.indexName("ix", s.name)) + " (" + strings.Join(items, ", ") + ")"
		} else {
			columns = append(columns, flag)
			items = append(items, quote(flag))
			line = "UNIQUE KEY " + quote(t.indexName("uq", s.name)) + " (" + strings.Join(items, ", ") + ")"
		}
		t.types[flag] = "TINYINT(1)"
		checkKey("index "+s.name, columns)
		g.report.indexActions = append(g.report.indexActions, fmt.Sprintf("| %s | %s | %s | %s |", t.name, s.name, s.action, s.reason))
		return line, generated
	}
	return "", nil
}

func (g *generator) indexItems(t *tablePlan, parts []keyPart) (columns, items []string) {
	for _, p := range parts {
		c := t.keyColumn(p.column)
		columns = append(columns, c)
		item := quote(c)
		if p.desc {
			item += " DESC"
		}
		items = append(items, item)
	}
	return columns, items
}

// referentialNoOp reports whether a foreign key action leaves the child row alone: MySQL refuses a CHECK
// on a column that any other action rewrites or removes (error 3823).
func referentialNoOp(action string) bool { return action == "NO ACTION" || action == "RESTRICT" }

// checkLines translates the CHECK constraints of a table.
func (g *generator) checkLines(t *tablePlan) []string {
	restricted := map[string]string{} // column -> foreign key action that forbids a CHECK on it
	for _, fk := range t.table.ForeignKeys {
		local, _ := parseForeignKey(fk)
		onDelete, onUpdate := foreignKeyActions(fk)
		if !referentialNoOp(onDelete) || !referentialNoOp(onUpdate) {
			for _, c := range local.columns {
				restricted[c] = fk
			}
		}
	}
	var lines []string
	for _, name := range slices.Sorted(mapKeys(t.table.Checks)) {
		g.claimName(name, t.name)
		decision, decided := g.decisions.Checks[name]
		switch {
		case decided && decision.Skip:
			if strings.TrimSpace(decision.Reason) == "" {
				g.problemf("checks: %s needs a reason", name)
			}
			g.report.skippedChecks = append(g.report.skippedChecks, fmt.Sprintf("| %s | %s | %s |", t.name, name, decision.Reason))
			continue
		case decided && decision.SQL != "":
			lines = append(lines, fmt.Sprintf("CONSTRAINT %s CHECK (%s)", quote(name), decision.SQL))
			continue
		case decided:
			g.problemf("checks: %s needs skip: true or sql:", name)
			continue
		}
		translated, err := Translate(t.table.Checks[name])
		if err != nil {
			g.problemf("check %s of %s (%s): %v: add it under checks:", name, t.name, t.table.Checks[name], err)
			continue
		}
		var blocked string
		for _, c := range translated.Columns {
			if _, ok := t.table.Columns[c]; !ok {
				g.problemf("check %s of %s names the unknown column %s", name, t.name, c)
			}
			if fk, bad := restricted[c]; bad {
				blocked = fk
			}
		}
		if blocked != "" {
			g.report.autoSkipped = append(g.report.autoSkipped, fmt.Sprintf("| %s | %s | the column is in the referential action of the foreign key `%s`; MySQL refuses a CHECK on it (error 3823); the store validates it |", t.name, name, blocked))
			continue
		}
		lines = append(lines, fmt.Sprintf("CONSTRAINT %s CHECK (%s)", quote(name), translated.SQL))
	}
	return lines
}

// --- output ---------------------------------------------------------------------------

// header opens the generated file.
const header = `-- The MySQL 8.4 baseline of notarx (ADR 0022, slice 3c).
--
-- Generated by: go run ./tools/mysqlbaseline
-- from migrations/catalogue.yaml and migrations/mysql-decisions.yaml. Do not edit by hand:
-- make test fails when this file differs from what the generator produces. The decisions
-- behind it are listed in docs/mysql-schema-decisions.md.
`

func (g *generator) emit() {
	g.sql.WriteString(header)
	g.sql.WriteString("\n-- Every identifier is lower-case snake case; all text compares bytewise (utf8mb4_0900_bin).\n")
	g.sql.WriteString("\nCREATE TABLE `notarx_lock_buckets` (\n  `bucket` SMALLINT UNSIGNED NOT NULL,\n  PRIMARY KEY (`bucket`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;\n")
	for _, name := range g.catalogue.TableNames() {
		t := g.tables[name]
		g.sql.WriteString("\nCREATE TABLE " + quote(name) + " (\n")
		for i, line := range t.lines {
			sep := ","
			if i == len(t.lines)-1 {
				sep = ""
			}
			g.sql.WriteString("  " + line + sep + "\n")
		}
		g.sql.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;\n")
	}
	g.sql.WriteString("\n-- Foreign keys come after every table exists, so the table order is free. Their columns follow the\n-- order of the referenced key, which InnoDB requires.\n")
	for _, name := range g.catalogue.TableNames() {
		t := g.tables[name]
		for _, fk := range t.table.ForeignKeys {
			local, remote := parseForeignKey(fk)
			onDelete, onUpdate := foreignKeyActions(fk)
			parent := g.tables[remote.table]
			if parent == nil {
				g.problemf("%s: the foreign key %s references the unknown table %s", name, fk, remote.table)
				continue
			}
			localOrdered, remoteOrdered, ok := g.orderForeignKey(t, parent, local.columns, remote.columns)
			if !ok {
				g.problemf("%s: the foreign key %s does not match a primary or unique key of %s", name, fk, remote.table)
				continue
			}
			constraint := shortName("fk_"+name, local.columns...)
			g.claimName(constraint, name)
			fmt.Fprintf(&g.sql, "ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s ON UPDATE %s;\n",
				quote(name), quote(constraint), quoteAll(localOrdered), quote(remote.table), quoteAll(remoteOrdered), onDelete, onUpdate)
		}
	}
	g.emitSeeds()
}

// emitSeeds writes the lock buckets and the singleton rows of the catalogue.
func (g *generator) emitSeeds() {
	g.sql.WriteString("\n-- notarx_lock_buckets backs Locker.LockTx: 4096 rows, one SELECT ... FOR UPDATE per bucket (ADR 0022).\n")
	digits := make([]string, 16)
	for i := range digits {
		digits[i] = "SELECT " + strconv.Itoa(i) + " AS n"
	}
	union := strings.Join(digits, " UNION ALL ")
	fmt.Fprintf(&g.sql, "INSERT INTO `notarx_lock_buckets` (`bucket`)\nSELECT a.n * 256 + b.n * 16 + c.n\nFROM (%s) a, (%s) b, (%s) c;\n", union, union, union)
	if LockBuckets != 16*16*16 {
		g.problemf("the lock bucket generator is written for 4096 buckets")
	}
	for _, name := range slices.Sorted(mapKeys(g.catalogue.Seeds)) {
		t := g.tables[name]
		if t == nil {
			g.problemf("seeds: %s is not a table", name)
			continue
		}
		g.report.seedTables = append(g.report.seedTables, name)
		for _, row := range g.catalogue.Seeds[name] {
			values := map[string]string{}
			for column, value := range row {
				values[column] = g.seedValue(t, column, value)
			}
			for column, expression := range g.decisions.Seeds[name] {
				values[column] = expression
			}
			columns := slices.Sorted(mapKeys(values))
			expressions := make([]string, len(columns))
			for i, c := range columns {
				expressions[i] = values[c]
			}
			fmt.Fprintf(&g.sql, "INSERT INTO %s (%s) VALUES (%s);\n", quote(name), quoteAll(columns), strings.Join(expressions, ", "))
		}
	}
}

func (g *generator) seedValue(t *tablePlan, column, value string) string {
	col, ok := t.table.Columns[column]
	if !ok {
		g.problemf("seed column %s.%s is not a column", t.name, column)
		return "NULL"
	}
	switch {
	case col.Type == "bool":
		if bit, ok := map[string]string{"true": "1", "false": "0"}[value]; ok {
			return bit
		}
		g.problemf("seed %s.%s is the boolean text %q, which is neither true nor false", t.name, column, value)
		return "NULL"
	case col.Type == "json", strings.HasPrefix(col.Type, "array<"):
		return "CAST('" + escapeLiteral(value) + "' AS JSON)"
	case numberLiteral.MatchString(value) && (col.Type == "int16" || col.Type == "int32" || col.Type == "int64" || col.Type == "decimal"):
		return value
	}
	return "'" + escapeLiteral(value) + "'"
}

// orderForeignKey returns the key columns of a foreign key in the order of the primary or unique
// key it references, with a hashed column on both sides replaced by its hash column.
func (g *generator) orderForeignKey(child, parent *tablePlan, local, remote []string) ([]string, []string, bool) {
	candidates := [][]string{parent.table.PrimaryKey}
	candidates = append(candidates, parent.table.Unique...)
	for _, key := range candidates {
		if len(key) != len(remote) || !sameSet(key, remote) {
			continue
		}
		var localOrdered, remoteOrdered []string
		for _, r := range key {
			i := slices.Index(remote, r)
			l := local[i]
			if child.hashed[l] != parent.hashed[r] {
				g.problemf("%s: the foreign key column %s and %s.%s must both be hashed or neither", child.name, l, parent.name, r)
			}
			if a, b := typeFamily(child.types[l]), typeFamily(parent.types[r]); a != b {
				g.problemf("%s: the foreign key column %s is %s but %s.%s is %s; MySQL needs matching types", child.name, l, child.types[l], parent.name, r, parent.types[r])
			}
			localOrdered = append(localOrdered, child.keyColumn(l))
			remoteOrdered = append(remoteOrdered, parent.keyColumn(r))
		}
		return localOrdered, remoteOrdered, true
	}
	return nil, nil, false
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// typeFamily is the type of a column as a foreign key sees it: the length of a string type does not matter, the rest does.
func typeFamily(mysqlType string) string {
	if strings.HasPrefix(mysqlType, "VARCHAR(") {
		return "VARCHAR"
	}
	return mysqlType
}
