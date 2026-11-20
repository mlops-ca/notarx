package schemaparity

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Dialects lists the migration directories under migrations/, one per dialect.
var Dialects = []string{DialectPostgres, DialectMySQL}

// ScopeFile names, relative to migrations/, the migrations that ship for one dialect only.
const ScopeFile = "dialect-scope.yaml"

// scope is the content of migrations/dialect-scope.yaml.
type scope struct {
	Version    int                   `yaml:"version"`
	Migrations map[string]scopeEntry `yaml:"migrations"`
}

type scopeEntry struct {
	Dialect string `yaml:"dialect"`
	Reason  string `yaml:"reason"`
}

// CheckDialectScope enforces the ADR 0022 rule for every migration after the
// baseline: it ships in both dialect directories, or migrations/dialect-scope.yaml
// names the one dialect it applies to and why. It also rejects an entry for a
// file that does not exist or that ships in both directories after all. The
// baselines (000001) are generated per dialect and are not covered.
func CheckDialectScope(migrationsDir string) ([]string, error) {
	present := map[string]map[string]bool{} // file -> dialect -> shipped
	for _, dialect := range Dialects {
		entries, err := os.ReadDir(filepath.Join(migrationsDir, dialect))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".sql") || strings.HasPrefix(name, "000001_") {
				continue
			}
			if present[name] == nil {
				present[name] = map[string]bool{}
			}
			present[name][dialect] = true
		}
	}
	declared := scope{}
	data, err := os.ReadFile(filepath.Join(migrationsDir, ScopeFile))
	switch {
	case err == nil:
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&declared); err != nil {
			return nil, fmt.Errorf("read %s: %w", ScopeFile, err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	var problems []string
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range present {
			if !yield(name) {
				return
			}
		}
	})
	for _, name := range names {
		shipped := present[name]
		entry, named := declared.Migrations[name]
		if len(shipped) == len(Dialects) {
			if named {
				problems = append(problems, fmt.Sprintf("%s ships in every dialect directory, so %s must not name it", name, ScopeFile))
			}
			continue
		}
		var only string
		for dialect := range shipped {
			only = dialect
		}
		switch {
		case !named:
			problems = append(problems, fmt.Sprintf("%s ships only in migrations/%s: add it to the other dialect or name its dialect and the reason in migrations/%s", name, only, ScopeFile))
		case entry.Dialect != only:
			problems = append(problems, fmt.Sprintf("%s is declared %s-only in %s but ships in migrations/%s", name, entry.Dialect, ScopeFile, only))
		case strings.TrimSpace(entry.Reason) == "":
			problems = append(problems, fmt.Sprintf("%s needs a reason in %s", name, ScopeFile))
		}
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range declared.Migrations {
			if !yield(name) {
				return
			}
		}
	}) {
		if present[name] == nil {
			problems = append(problems, fmt.Sprintf("%s names %s, which no dialect directory ships", ScopeFile, name))
		}
	}
	return problems, nil
}
