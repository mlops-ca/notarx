package schemaparity

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// Tables that belong to the migration machinery, not to the schema: the ledger
// of both runners and the MySQL lock-bucket table that stands in for advisory
// locks (ADR 0022). They are never part of the catalogue.
var ignoredTables = map[string]bool{"notarx_schema_migrations": true, "notarx_lock_buckets": true}

// Dialect names, equal to the NOTARX_DATABASE_DRIVER values.
const (
	DialectPostgres = "postgres"
	DialectMySQL    = "mysql"
)

// Introspect reads the schema of the database db (a migrated PostgreSQL or MySQL)
// into the neutral form: logical types, keys, unique sets and generated columns.
// Seeds are read by IntrospectSeeds, which needs the column types of this result.
func Introspect(ctx context.Context, db *sql.DB, dialect string) (Catalogue, error) {
	switch dialect {
	case DialectPostgres:
		return introspectPostgres(ctx, db)
	case DialectMySQL:
		return introspectMySQL(ctx, db)
	default:
		return Catalogue{}, fmt.Errorf("unsupported dialect %q", dialect)
	}
}

// rows runs a query and hands each row to scan.
func rows(ctx context.Context, db *sql.DB, query string, scan func(*sql.Rows) error) error {
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = result.Close() }()
	for result.Next() {
		if err := scan(result); err != nil {
			return err
		}
	}
	return result.Err()
}

func splitList(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(list, ",")
}

func newCatalogue() Catalogue {
	return Catalogue{Version: CatalogueVersion, Tables: map[string]Table{}}
}

func (c Catalogue) table(name string) Table {
	t, ok := c.Tables[name]
	if !ok {
		t = Table{Columns: map[string]Column{}}
	}
	return t
}

// addUnique records a unique set in its key order, once: the order decides which lookups
// the index serves, so MySQL must declare the same one.
func (t *Table) addUnique(columns []string) {
	set := slices.Clone(columns)
	for _, existing := range t.Unique {
		if slices.Equal(existing, set) {
			return
		}
	}
	t.Unique = append(t.Unique, set)
}

// finish sorts the lists of a table so that equal schemas render equal files.
func (t *Table) finish() {
	slices.SortFunc(t.Unique, func(a, b []string) int { return strings.Compare(strings.Join(a, ","), strings.Join(b, ",")) })
	slices.Sort(t.ForeignKeys)
	slices.Sort(t.Indexes)
	t.Indexes = slices.Compact(t.Indexes)
}

func foreignKey(referencedTable string, referenced, local []string, onDelete, onUpdate string) string {
	// Pair the columns, sort the pairs by the local column, so the text does not depend on constraint order.
	type pair struct{ local, referenced string }
	pairs := make([]pair, len(local))
	for i := range local {
		pairs[i] = pair{local[i], referenced[i]}
	}
	slices.SortFunc(pairs, func(a, b pair) int { return strings.Compare(a.local, b.local) })
	var localNames, referencedNames []string
	for _, p := range pairs {
		localNames = append(localNames, p.local)
		referencedNames = append(referencedNames, p.referenced)
	}
	text := strings.Join(localNames, ",") + " -> " + referencedTable + "(" + strings.Join(referencedNames, ",") + ")"
	if onDelete != "" {
		text += " on delete " + onDelete
	}
	if onUpdate != "" {
		text += " on update " + onUpdate
	}
	return text
}

// postgresAction maps pg_constraint.confdeltype and confupdtype to the catalogue word;
// the default, NO ACTION, is the empty string.
func postgresAction(code string) string {
	switch code {
	case "r":
		return "restrict"
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "d":
		return "set default"
	default:
		return ""
	}
}

// mysqlAction maps referential_constraints.delete_rule and update_rule the same way.
func mysqlAction(rule string) string {
	switch rule {
	case "RESTRICT":
		return "restrict"
	case "CASCADE":
		return "cascade"
	case "SET NULL":
		return "set null"
	case "SET DEFAULT":
		return "set default"
	default:
		return ""
	}
}

const postgresTablesSQL = `SELECT c.relname, c.relkind = 'p'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')`

const postgresColumnsSQL = `SELECT table_name, column_name, data_type, udt_name, is_nullable, is_generated
FROM information_schema.columns WHERE table_schema = 'public'`

// Unique indexes over plain columns, constraint-backed or not. Partitions are left out: they repeat their parent.
const postgresUniqueSQL = `SELECT c.relname, i.indisprimary, string_agg(a.attname, ',' ORDER BY k.ord)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
WHERE i.indisunique AND i.indpred IS NULL AND i.indexprs IS NULL AND k.ord <= i.indnkeyatts
  AND n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
GROUP BY c.relname, i.indexrelid, i.indisprimary`

const postgresForeignKeysSQL = `SELECT c.relname, rc.relname,
  (SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum),
  (SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord) JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum),
  con.confdeltype::text, con.confupdtype::text
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_class rc ON rc.oid = con.confrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.contype = 'f' AND n.nspname = 'public' AND NOT c.relispartition AND con.conparentid = 0`

func introspectPostgres(ctx context.Context, db *sql.DB) (Catalogue, error) {
	catalogue := newCatalogue()
	if err := rows(ctx, db, postgresTablesSQL, func(r *sql.Rows) error {
		var name string
		var partitioned bool
		if err := r.Scan(&name, &partitioned); err != nil {
			return err
		}
		if !ignoredTables[name] {
			t := catalogue.table(name)
			t.Partitioned = partitioned
			catalogue.Tables[name] = t
		}
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list tables: %w", err)
	}
	if err := rows(ctx, db, postgresColumnsSQL, func(r *sql.Rows) error {
		var table, column, dataType, udt, nullable, generated string
		if err := r.Scan(&table, &column, &dataType, &udt, &nullable, &generated); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil // a view, a partition or a table the catalogue leaves out
		}
		logical, err := postgresLogicalType(dataType, udt)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", table, column, err)
		}
		t.Columns[column] = Column{Type: logical, Nullable: nullable == "YES", Generated: generated == "ALWAYS"}
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list columns: %w", err)
	}
	if err := rows(ctx, db, postgresUniqueSQL, func(r *sql.Rows) error {
		var table, columns string
		var primary bool
		if err := r.Scan(&table, &primary, &columns); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		if primary {
			t.PrimaryKey = splitList(columns)
		} else {
			t.addUnique(splitList(columns))
		}
		catalogue.Tables[table] = t
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list keys: %w", err)
	}
	if err := rows(ctx, db, postgresForeignKeysSQL, func(r *sql.Rows) error {
		var table, referenced, local, remote, onDelete, onUpdate string
		if err := r.Scan(&table, &referenced, &local, &remote, &onDelete, &onUpdate); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		t.ForeignKeys = append(t.ForeignKeys, foreignKey(referenced, splitList(remote), splitList(local), postgresAction(onDelete), postgresAction(onUpdate)))
		catalogue.Tables[table] = t
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list foreign keys: %w", err)
	}
	if err := introspectPostgresDDL(ctx, db, catalogue); err != nil {
		return Catalogue{}, err
	}
	if err := introspectPostgresObjects(ctx, db, &catalogue); err != nil {
		return Catalogue{}, err
	}
	return finishCatalogue(catalogue), nil
}

func finishCatalogue(catalogue Catalogue) Catalogue {
	for name, t := range catalogue.Tables {
		t.finish()
		catalogue.Tables[name] = t
	}
	return catalogue
}

const mysqlTablesSQL = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'`

const mysqlColumnsSQL = `SELECT table_name, column_name, data_type, column_type, character_maximum_length, collation_name, is_nullable, extra
FROM information_schema.columns WHERE table_schema = DATABASE()`

// Index key parts in order; a functional key part has no column name and is skipped with its index.
const mysqlIndexesSQL = `SELECT table_name, index_name, seq_in_index, column_name
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND non_unique = 0
ORDER BY table_name, index_name, seq_in_index`

const mysqlForeignKeysSQL = `SELECT k.table_name, k.constraint_name, k.column_name, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule
FROM information_schema.key_column_usage k
JOIN information_schema.referential_constraints r
  ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name AND r.table_name = k.table_name
WHERE k.table_schema = DATABASE() AND k.referenced_table_name IS NOT NULL
ORDER BY k.table_name, k.constraint_name, k.ordinal_position`

// foreignParts collects the columns of one MySQL foreign key, which information_schema lists a row per column.
type foreignParts struct {
	local, remote      []string
	table              string
	onDelete, onUpdate string
}

func introspectMySQL(ctx context.Context, db *sql.DB) (Catalogue, error) {
	catalogue := newCatalogue()
	if err := rows(ctx, db, mysqlTablesSQL, func(r *sql.Rows) error {
		var name string
		if err := r.Scan(&name); err != nil {
			return err
		}
		if !ignoredTables[name] {
			catalogue.Tables[name] = catalogue.table(name)
		}
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list tables: %w", err)
	}
	if err := rows(ctx, db, mysqlColumnsSQL, func(r *sql.Rows) error {
		var table, column, dataType, columnType, nullable, extra string
		var length sql.NullInt64
		var collation sql.NullString
		if err := r.Scan(&table, &column, &dataType, &columnType, &length, &collation, &nullable, &extra); err != nil {
			return err
		}
		t, ok := catalogue.Tables[table]
		if !ok {
			return nil
		}
		logical, err := mysqlLogicalType(dataType, columnType, length, collation)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", table, column, err)
		}
		generated := strings.Contains(extra, "STORED GENERATED") || strings.Contains(extra, "VIRTUAL GENERATED")
		t.Columns[column] = Column{Type: logical, Nullable: nullable == "YES", Generated: generated}
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list columns: %w", err)
	}
	if err := mysqlKeys(ctx, db, catalogue); err != nil {
		return Catalogue{}, err
	}
	if err := introspectMySQLIndexes(ctx, db, catalogue); err != nil {
		return Catalogue{}, err
	}
	foreign := map[string]map[string]*foreignParts{}
	if err := rows(ctx, db, mysqlForeignKeysSQL, func(r *sql.Rows) error {
		var table, constraint, column, referencedTable, referencedColumn, deleteRule, updateRule string
		if err := r.Scan(&table, &constraint, &column, &referencedTable, &referencedColumn, &deleteRule, &updateRule); err != nil {
			return err
		}
		if foreign[table] == nil {
			foreign[table] = map[string]*foreignParts{}
		}
		fk := foreign[table][constraint]
		if fk == nil {
			fk = &foreignParts{table: referencedTable, onDelete: mysqlAction(deleteRule), onUpdate: mysqlAction(updateRule)}
			foreign[table][constraint] = fk
		}
		// A key on a hashed column is declared on its SHA-256 column; the catalogue names the original.
		fk.local = append(fk.local, baseColumn(catalogue.Tables[table], column))
		fk.remote = append(fk.remote, baseColumn(catalogue.Tables[referencedTable], referencedColumn))
		return nil
	}); err != nil {
		return Catalogue{}, fmt.Errorf("list foreign keys: %w", err)
	}
	for table, constraints := range foreign {
		t, ok := catalogue.Tables[table]
		if !ok {
			continue
		}
		for _, fk := range constraints {
			t.ForeignKeys = append(t.ForeignKeys, foreignKey(fk.table, fk.remote, fk.local, fk.onDelete, fk.onUpdate))
		}
		catalogue.Tables[table] = t
	}
	return finishCatalogue(catalogue), nil
}

// mysqlKeys reads the primary key and the unique indexes over plain columns.
func mysqlKeys(ctx context.Context, db *sql.DB, catalogue Catalogue) error {
	type key struct{ table, index string }
	parts := map[key][]string{}
	functional := map[key]bool{}
	var order []key
	if err := rows(ctx, db, mysqlIndexesSQL, func(r *sql.Rows) error {
		var table, index string
		var seq int
		var column sql.NullString
		if err := r.Scan(&table, &index, &seq, &column); err != nil {
			return err
		}
		k := key{table, index}
		if _, seen := parts[k]; !seen {
			order = append(order, k)
		}
		if !column.Valid {
			functional[k] = true
		}
		parts[k] = append(parts[k], baseColumn(catalogue.Tables[table], column.String))
		return nil
	}); err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	for _, k := range order {
		t, ok := catalogue.Tables[k.table]
		if !ok || functional[k] {
			continue
		}
		if k.index == "PRIMARY" {
			t.PrimaryKey = parts[k]
		} else {
			t.addUnique(parts[k])
		}
		catalogue.Tables[k.table] = t
	}
	return nil
}
