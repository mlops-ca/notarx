package schemaparity

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// hashSuffix names the stored SHA-256 column that carries a key on MySQL when the
// value is too long to index (internal/mysqlbaseline). The catalogue describes the
// key by the original column, so MySQL introspection maps the hash column back.
const hashSuffix = "_sha256"

// baseColumn maps a generated hash column of a table to the column it hashes.
func baseColumn(t Table, column string) string {
	if base, ok := strings.CutSuffix(column, hashSuffix); ok {
		if _, known := t.Columns[base]; known && t.Columns[column].Generated {
			return base
		}
	}
	return column
}

const postgresDefaultsSQL = `SELECT c.relname, a.attname, pg_get_expr(d.adbin, d.adrelid)
FROM pg_attrdef d
JOIN pg_class c ON c.oid = d.adrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND NOT a.attisdropped AND a.attgenerated = ''`

// Every index except the primary key: the key columns with their direction, and
// the definition for the ones the catalogue lists under Special.
const postgresIndexesSQL = `SELECT c.relname, ic.relname, i.indisunique, i.indpred IS NOT NULL, i.indexprs IS NOT NULL, am.amname,
  pg_get_indexdef(i.indexrelid),
  COALESCE((SELECT string_agg(a.attname || CASE WHEN (i.indoption::int2[])[k.ord - 1] & 1 = 1 THEN ' desc' ELSE '' END, ',' ORDER BY k.ord)
    FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
    WHERE k.ord <= i.indnkeyatts AND k.attnum <> 0), '')
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_am am ON am.oid = ic.relam
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND NOT i.indisprimary`

const postgresChecksSQL = `SELECT c.relname, con.conname, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.contype = 'c' AND n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND con.conparentid = 0`

const postgresTriggersSQL = `SELECT t.tgname, c.relname, pg_get_triggerdef(t.oid)
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal AND n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition`

const postgresViewsSQL = `SELECT c.relname
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('v', 'm')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')`

var triggerDefinitionPrefix = regexp.MustCompile(`^CREATE (?:OR REPLACE )?(?:CONSTRAINT )?TRIGGER \S+ `)

// introspectPostgresObjects adds the triggers and views the MySQL baseline does not mirror.
func introspectPostgresObjects(ctx context.Context, db *sql.DB, catalogue *Catalogue) error {
	if err := rows(ctx, db, postgresTriggersSQL, func(r *sql.Rows) error {
		var name, table, definition string
		if err := r.Scan(&name, &table, &definition); err != nil {
			return err
		}
		if _, ok := catalogue.Tables[table]; !ok {
			return nil
		}
		if catalogue.Triggers == nil {
			catalogue.Triggers = map[string]string{}
		}
		text := triggerDefinitionPrefix.ReplaceAllString(definition, "")
		catalogue.Triggers[name] = table + ": " + strings.ReplaceAll(text, "public.", "")
		return nil
	}); err != nil {
		return fmt.Errorf("list triggers: %w", err)
	}
	if err := rows(ctx, db, postgresViewsSQL, func(r *sql.Rows) error {
		var name string
		if err := r.Scan(&name); err != nil {
			return err
		}
		catalogue.Views = append(catalogue.Views, name)
		return nil
	}); err != nil {
		return fmt.Errorf("list views: %w", err)
	}
	slices.Sort(catalogue.Views)
	return nil
}

// introspectPostgresDDL adds the defaults, indexes and CHECK constraints of a migrated PostgreSQL.
func introspectPostgresDDL(ctx context.Context, db *sql.DB, catalogue Catalogue) error {
	if err := rows(ctx, db, postgresDefaultsSQL, func(r *sql.Rows) error {
		var table, column, expression string
		if err := r.Scan(&table, &column, &expression); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		if t.Defaults == nil {
			t.Defaults = map[string]string{}
			catalogue.Tables[table] = t
		}
		t.Defaults[column] = normalizeDefault(expression, t.Columns[column].Type)
		return nil
	}); err != nil {
		return fmt.Errorf("list defaults: %w", err)
	}
	if err := rows(ctx, db, postgresIndexesSQL, func(r *sql.Rows) error {
		var table, name, method, definition, columns string
		var unique, partial, expression bool
		if err := r.Scan(&table, &name, &unique, &partial, &expression, &method, &definition, &columns); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		switch {
		case unique && !partial && !expression:
			return nil // a unique set, listed by Unique
		case plainIndex(unique, partial, expression, method, definition):
			t.Indexes = append(t.Indexes, columns)
		default:
			if t.Special == nil {
				t.Special = map[string]string{}
			}
			t.Special[name] = normalizeIndexDefinition(definition, unique)
		}
		catalogue.Tables[table] = t
		return nil
	}); err != nil {
		return fmt.Errorf("list indexes: %w", err)
	}
	if err := rows(ctx, db, postgresChecksSQL, func(r *sql.Rows) error {
		var table, name, definition string
		if err := r.Scan(&table, &name, &definition); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		if t.Checks == nil {
			t.Checks = map[string]string{}
			catalogue.Tables[table] = t
		}
		t.Checks[name] = normalizeCheck(definition)
		return nil
	}); err != nil {
		return fmt.Errorf("list checks: %w", err)
	}
	return nil
}

// plainIndex reports whether an index is an ordinary btree over columns in their default order and operator class.
func plainIndex(unique, partial, expression bool, method, definition string) bool {
	return !unique && !partial && !expression && method == "btree" &&
		!strings.Contains(definition, "_pattern_ops") && !strings.Contains(definition, "COLLATE") && !strings.Contains(definition, " INCLUDE ")
}

var indexDefinitionPrefix = regexp.MustCompile(`^CREATE (?:UNIQUE )?INDEX \S+ ON (?:ONLY )?(?:public\.)?\S+ `)

// normalizeIndexDefinition drops the index and table names from pg_get_indexdef: the
// catalogue keys the definition by the index name, so a rename shows as drift, but a
// move of the table does not.
func normalizeIndexDefinition(definition string, unique bool) string {
	text := indexDefinitionPrefix.ReplaceAllString(definition, "")
	text = strings.ReplaceAll(text, "public.", "")
	if unique {
		text = "unique " + text
	}
	return text
}

// normalizeCheck turns pg_get_constraintdef output, "CHECK ((x > 0))", into the expression.
func normalizeCheck(definition string) string {
	text := strings.TrimSpace(definition)
	text = strings.TrimSuffix(text, " NOT VALID")
	text = strings.TrimPrefix(text, "CHECK ")
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") && balanced(text[1:len(text)-1]) {
		text = text[1 : len(text)-1]
	}
	return strings.TrimSpace(text)
}

// balanced reports whether the parentheses of text, outside string literals, pair up
// without closing early, so that stripping one outer pair is safe.
func balanced(text string) bool {
	depth := 0
	quoted := false
	for _, r := range text {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !quoted
}

var (
	defaultCast       = regexp.MustCompile(`::(?:character varying|timestamp with time zone|timestamp without time zone|double precision|[a-z_][a-z0-9_]*)(?:\[\])*`)
	defaultNow        = regexp.MustCompile(`^\(?(?:now|clock_timestamp|statement_timestamp|transaction_timestamp)\(\)\)?$|^CURRENT_TIMESTAMP$`)
	defaultBraceArray = regexp.MustCompile(`^'\{([^']*)\}'$`)
	defaultArray      = regexp.MustCompile(`^ARRAY\[(.*)\]$`)
)

// normalizeDefault turns a PostgreSQL column default into the neutral form of the
// catalogue: "identity" for a sequence, "now()" for a timestamp function, "uuid()" for a
// generated identifier, a JSON text for an array, and otherwise the expression without
// its casts.
func normalizeDefault(expression, logical string) string {
	text := strings.TrimSpace(expression)
	switch {
	case strings.HasPrefix(text, "nextval("):
		return "identity"
	case text == "gen_random_uuid()" || text == "uuidv7()":
		return "uuid()"
	}
	text = strings.TrimSpace(defaultCast.ReplaceAllString(text, ""))
	if defaultNow.MatchString(text) {
		return "now()"
	}
	if strings.HasPrefix(logical, "array<") {
		if text == "'{}'" || text == "ARRAY[]" {
			return "'[]'"
		}
		if m := defaultBraceArray.FindStringSubmatch(text); m != nil {
			// A PostgreSQL array literal, '{a,b}': only plain elements have a JSON form here.
			items := strings.Split(m[1], ",")
			for i, item := range items {
				if item == "" || strings.ContainsAny(item, "\"'\\{}") || strings.TrimSpace(item) != item {
					return text
				}
				items[i] = "\"" + item + "\""
			}
			return "'[" + strings.Join(items, ",") + "]'"
		}
		if m := defaultArray.FindStringSubmatch(text); m != nil {
			items := strings.Split(m[1], ", ")
			for i, item := range items {
				if len(item) < 2 || item[0] != '\'' || item[len(item)-1] != '\'' || strings.ContainsAny(item[1:len(item)-1], "'\"\\") {
					return text
				}
				items[i] = "\"" + item[1:len(item)-1] + "\""
			}
			return "'[" + strings.Join(items, ",") + "]'"
		}
	}
	return text
}

const mysqlPlainIndexesSQL = `SELECT table_name, index_name, column_name, collation
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND non_unique = 1 AND index_type = 'BTREE'
ORDER BY table_name, index_name, seq_in_index`

// introspectMySQLIndexes adds the secondary btree indexes over plain columns.
func introspectMySQLIndexes(ctx context.Context, db *sql.DB, catalogue Catalogue) error {
	type key struct{ table, index string }
	parts := map[key][]string{}
	functional := map[key]bool{}
	var order []key
	if err := rows(ctx, db, mysqlPlainIndexesSQL, func(r *sql.Rows) error {
		var table, index string
		var column, collation sql.NullString
		if err := r.Scan(&table, &index, &column, &collation); err != nil {
			return err
		}
		k := key{table, index}
		if _, seen := parts[k]; !seen {
			order = append(order, k)
		}
		if !column.Valid {
			functional[k] = true
			return nil
		}
		part := baseColumn(catalogue.Tables[table], column.String)
		if collation.String == "D" {
			part += " desc"
		}
		parts[k] = append(parts[k], part)
		return nil
	}); err != nil {
		return fmt.Errorf("list indexes: %w", err)
	}
	for _, k := range order {
		t, ok := catalogue.Tables[k.table]
		if !ok || functional[k] {
			continue
		}
		t.Indexes = append(t.Indexes, strings.Join(parts[k], ","))
		catalogue.Tables[k.table] = t
	}
	return nil
}
