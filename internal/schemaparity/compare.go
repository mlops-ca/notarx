package schemaparity

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Compare lists how the schema got differs from the catalogue want. PostgreSQL
// is the reference, so its schema must match exactly. MySQL may carry what its
// design adds (ADR 0022): extra columns that are generated (the form of a partial
// or expression index), unique sets that include one, and a JSON column where the
// catalogue has an array. Anything else that differs is reported.
func Compare(want, got Catalogue, dialect string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, name := range want.TableNames() {
		wantTable := want.Tables[name]
		gotTable, ok := got.Tables[name]
		if !ok {
			add("table %s is missing", name)
			continue
		}
		if dialect != DialectMySQL && wantTable.Partitioned != gotTable.Partitioned {
			add("table %s partitioned=%t; the catalogue has partitioned=%t", name, gotTable.Partitioned, wantTable.Partitioned)
		}
		compareColumns(name, wantTable, gotTable, dialect, add)
		compareKeys(name, wantTable, gotTable, dialect, add)
		compareDDL(name, wantTable, gotTable, dialect, add)
	}
	for _, name := range got.TableNames() {
		if _, ok := want.Tables[name]; !ok {
			add("table %s is not in the catalogue", name)
		}
	}
	return problems
}

func compareColumns(table string, want, got Table, dialect string, add func(string, ...any)) {
	for _, name := range sortedKeys(want.Columns) {
		w := want.Columns[name]
		g, ok := got.Columns[name]
		switch {
		case !ok:
			add("column %s.%s is missing", table, name)
		case dialect == DialectMySQL && !compatible(w.Type, g.Type) || dialect != DialectMySQL && w.Type != g.Type:
			add("column %s.%s has type %s; the catalogue has %s", table, name, g.Type, w.Type)
		case w.Nullable != g.Nullable:
			add("column %s.%s is %s; the catalogue has it %s", table, name, nullability(g.Nullable), nullability(w.Nullable))
		case w.Generated != g.Generated:
			add("column %s.%s generated=%t; the catalogue has generated=%t", table, name, g.Generated, w.Generated)
		}
	}
	for _, name := range sortedKeys(got.Columns) {
		if _, ok := want.Columns[name]; !ok && (dialect != DialectMySQL || !got.Columns[name].Generated) {
			add("column %s.%s is not in the catalogue", table, name)
		}
	}
}

func nullability(nullable bool) string {
	if nullable {
		return "nullable"
	}
	return "NOT NULL"
}

func compareKeys(table string, want, got Table, dialect string, add func(string, ...any)) {
	if !slices.Equal(want.PrimaryKey, got.PrimaryKey) {
		add("table %s has primary key (%s); the catalogue has (%s)", table, strings.Join(got.PrimaryKey, ","), strings.Join(want.PrimaryKey, ","))
	}
	wantSets, gotSets := setOf(want.Unique), setOf(got.Unique)
	for set := range wantSets {
		if !gotSets[set] {
			add("table %s lacks the unique set (%s)", table, set)
		}
	}
	for set := range gotSets {
		if wantSets[set] {
			continue
		}
		if dialect == DialectMySQL && usesGenerated(got, strings.Split(set, ",")) {
			continue
		}
		add("table %s has the unique set (%s), which the catalogue lacks", table, set)
	}
	if !slices.Equal(want.ForeignKeys, got.ForeignKeys) {
		add("table %s has foreign keys %v; the catalogue has %v", table, got.ForeignKeys, want.ForeignKeys)
	}
}

// compareDDL compares what the catalogue records beyond keys. PostgreSQL must match
// exactly. MySQL must carry every plain index and may carry more: InnoDB adds an index for
// a foreign key and the baseline adds generated-column indexes. Defaults, special indexes
// and CHECK constraints are not introspected on MySQL: the baseline is generated from them.
func compareDDL(table string, want, got Table, dialect string, add func(string, ...any)) {
	wantIndexes, gotIndexes := stringSet(want.Indexes), stringSet(got.Indexes)
	for index := range wantIndexes {
		if !gotIndexes[index] {
			add("table %s lacks the index (%s)", table, index)
		}
	}
	if dialect == DialectMySQL {
		return
	}
	for index := range gotIndexes {
		if !wantIndexes[index] {
			add("table %s has the index (%s), which the catalogue lacks", table, index)
		}
	}
	compareMaps(table, "default of column", want.Defaults, got.Defaults, add)
	compareMaps(table, "special index", want.Special, got.Special, add)
	compareMaps(table, "check constraint", want.Checks, got.Checks, add)
}

func compareMaps(table, what string, want, got map[string]string, add func(string, ...any)) {
	for _, name := range sortedKeys(want) {
		switch value, ok := got[name]; {
		case !ok:
			add("table %s lacks the %s %s", table, what, name)
		case value != want[name]:
			add("table %s has the %s %s as %q; the catalogue has %q", table, what, name, value, want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			add("table %s has the %s %s, which the catalogue lacks", table, what, name)
		}
	}
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func setOf(sets [][]string) map[string]bool {
	out := map[string]bool{}
	for _, set := range sets {
		out[strings.Join(set, ",")] = true
	}
	return out
}

func usesGenerated(t Table, columns []string) bool {
	return slices.ContainsFunc(columns, func(name string) bool { return t.Columns[name].Generated })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
}

// CompareSeeds lists the seed rows that differ between the catalogue and a database.
func CompareSeeds(want, got map[string][]map[string]string) []string {
	var problems []string
	for _, name := range sortedKeys(want) {
		wantRows, gotRows := rowCounts(want[name]), rowCounts(got[name])
		for _, row := range sortedKeys(wantRows) {
			if gotRows[row] != wantRows[row] {
				problems = append(problems, fmt.Sprintf("seed row {%s} of %s is present %d time(s); the catalogue has it %d time(s)", row, name, gotRows[row], wantRows[row]))
			}
		}
		for _, row := range sortedKeys(gotRows) {
			if _, ok := wantRows[row]; !ok {
				problems = append(problems, fmt.Sprintf("seed row {%s} of %s is not in the catalogue", row, name))
			}
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			problems = append(problems, fmt.Sprintf("table %s holds seed rows the catalogue lacks", name))
		}
	}
	return problems
}

// rowCounts counts the seed rows by content, so a duplicated row is a difference.
func rowCounts(rows []map[string]string) map[string]int {
	counts := map[string]int{}
	for _, row := range rows {
		counts[rowKey(row)]++
	}
	return counts
}
