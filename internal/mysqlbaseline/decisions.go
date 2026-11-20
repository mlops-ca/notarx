package mysqlbaseline

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DecisionsVersion is the version of the decisions file format.
const DecisionsVersion = 1

// Decisions is migrations/mysql-decisions.yaml: the per-column, per-index and
// per-trigger decisions ADR 0022 asks slice 3c to commit, in a form the generator reads.
// Rules that hold for most of the schema are written once; everything else is an entry
// with a reason, and the generator fails when an object has no rule and no entry, so a
// new column, index or trigger forces a decision.
type Decisions struct {
	Version int `yaml:"version"`
	// DefaultKeyWidth is the VARCHAR width of a text column that appears in a key or an
	// index and has no entry below.
	DefaultKeyWidth int `yaml:"defaultKeyWidth"`
	// KeyWidths sets the width of an indexed text column by column name, for every table.
	KeyWidths map[string]int `yaml:"keyWidths"`
	// HashNames lists column names whose indexed values are too long for an index key: in
	// every table the value stays in TEXT and a stored SHA-256 column carries the key (ADR 0022).
	// The value is the reason.
	HashNames map[string]string `yaml:"hashNames"`
	// Columns holds the exceptions, keyed "table.column".
	Columns map[string]ColumnDecision `yaml:"columns"`
	// Indexes decides every index the catalogue lists under special, keyed by index name.
	// An index without an entry follows the rule of its kind (see indexRule).
	Indexes map[string]IndexDecision `yaml:"indexes"`
	// Checks holds the CHECK constraints that are skipped or written by hand, keyed by constraint name.
	Checks map[string]CheckDecision `yaml:"checks"`
	// Defaults replaces the MySQL default clause of a column, keyed "table.column", for a
	// default the generator cannot translate. The value is the clause without the DEFAULT keyword.
	Defaults map[string]string `yaml:"defaults"`
	// Triggers classifies every PostgreSQL trigger of the catalogue.
	Triggers map[string]TriggerDecision `yaml:"triggers"`
	// Views classifies every PostgreSQL view of the catalogue.
	Views map[string]ViewDecision `yaml:"views"`
	// Seeds gives, per table and column, the MySQL expression for a seed value the catalogue
	// leaves out because it differs per installation (a generated identifier).
	Seeds map[string]map[string]string `yaml:"seeds"`
	// Arrays records why each TEXT[] column is a JSON array, keyed "table.column".
	Arrays map[string]string `yaml:"arrays"`
}

// ColumnDecision is an exception to the type rules.
type ColumnDecision struct {
	// Width sets the VARCHAR width of an indexed text column.
	Width int `yaml:"width,omitempty"`
	// Hash keeps the value in TEXT and keys the column by a stored SHA-256 column
	// named <column>_sha256 (ADR 0022): the value is too long to index.
	Hash bool `yaml:"hash,omitempty"`
	// Type replaces the MySQL type of the column outright.
	Type   string `yaml:"type,omitempty"`
	Reason string `yaml:"reason"`
}

// Index actions.
const (
	ActionFull       = "full"        // a plain index over the same columns, the predicate and operator classes dropped
	ActionDrop       = "drop"        // no MySQL index
	ActionFlag       = "flag"        // a generated flag column from the predicate, leading a plain index
	ActionUniqueFlag = "unique-flag" // a unique index over the columns and a generated column that is NULL outside the predicate
	ActionGenerated  = "generated"   // an index over generated columns that compute the PostgreSQL expression
)

// GeneratedColumn is a virtual column that computes what a PostgreSQL expression index stores.
type GeneratedColumn struct {
	Name string `yaml:"name"`
	// Type is the MySQL type, for example "VARCHAR(128)".
	Type string `yaml:"type"`
	// Expression is the MySQL expression; a NULL result keeps a row out of a unique index.
	Expression string `yaml:"expression"`
	Nullable   bool   `yaml:"nullable,omitempty"`
}

// IndexDecision decides one index of the special list.
type IndexDecision struct {
	Action string `yaml:"action"`
	// Columns replaces the key columns of the index, for an expression index whose MySQL
	// form is over plain columns.
	Columns []string `yaml:"columns,omitempty"`
	// Flag names the generated column of ActionFlag.
	Flag string `yaml:"flag,omitempty"`
	// Generated lists the columns of ActionGenerated; Columns then names the key, generated columns included.
	Generated []GeneratedColumn `yaml:"generated,omitempty"`
	Reason    string            `yaml:"reason"`
}

// CheckDecision skips a CHECK constraint or supplies its MySQL form.
type CheckDecision struct {
	// SQL is the MySQL expression; empty with Skip set.
	SQL  string `yaml:"sql,omitempty"`
	Skip bool   `yaml:"skip,omitempty"`
	// Reason says why the translation is not used.
	Reason string `yaml:"reason"`
}

// Trigger dispositions.
const (
	DispositionDropped = "dropped" // polling or a counter replaces it; nothing is created
	DispositionPorted  = "ported"  // its logic moves to application code in the store slice that owns the table
	DispositionKept    = "kept"    // a MySQL trigger with SIGNAL SQLSTATE '45000' guards the invariant
)

// TriggerDecision classifies a PostgreSQL trigger.
type TriggerDecision struct {
	Disposition string `yaml:"disposition"`
	Reason      string `yaml:"reason"`
}

// ViewDecision classifies a PostgreSQL view.
type ViewDecision struct {
	Disposition string `yaml:"disposition"` // "dropped" or "ported"
	Reason      string `yaml:"reason"`
}

// LoadDecisions reads the decisions file.
func LoadDecisions(path string) (Decisions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Decisions{}, err
	}
	var decisions Decisions
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&decisions); err != nil {
		return Decisions{}, fmt.Errorf("read %s: %w", path, err)
	}
	if decisions.Version != DecisionsVersion {
		return Decisions{}, fmt.Errorf("%s has version %d; this tool reads version %d", path, decisions.Version, DecisionsVersion)
	}
	if decisions.DefaultKeyWidth <= 0 {
		return Decisions{}, fmt.Errorf("%s: defaultKeyWidth must be positive", path)
	}
	return decisions, nil
}
