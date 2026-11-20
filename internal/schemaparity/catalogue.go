// Package schemaparity holds the dialect-neutral schema catalogue of ADR 0022:
// a checked-in YAML description of the tables, columns, logical types,
// nullability, keys, unique sets, generated columns and seed rows that
// every supported database must carry. The catalogue is introspected from a
// migrated PostgreSQL (the reference backend), then compared with the
// information_schema of a migrated PostgreSQL and a migrated MySQL, through an
// explicit logical-type mapping, so a drift in either dialect fails a check.
package schemaparity

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// CatalogueVersion is the version of the catalogue file format.
const CatalogueVersion = 1

// Catalogue is the neutral description of a schema.
type Catalogue struct {
	Version int              `yaml:"version"`
	Tables  map[string]Table `yaml:"tables"`
	// Seeds are the rows a freshly migrated database holds: the singleton rows
	// the code expects. Timestamps and generated identifiers are left out.
	Seeds map[string][]map[string]string `yaml:"seeds,omitempty"`
}

// Table describes one table.
type Table struct {
	Columns    map[string]Column `yaml:"columns"`
	PrimaryKey []string          `yaml:"primaryKey,omitempty,flow"`
	// Unique lists the unique sets over plain columns, each sorted by column
	// name. Partial and expression indexes are not in the catalogue: their
	// MySQL form is a generated column (ADR 0022), which the comparison allows.
	Unique [][]string `yaml:"unique,omitempty,flow"`
	// ForeignKeys are written "col[,col] -> table(col[,col])", sorted.
	ForeignKeys []string `yaml:"foreignKeys,omitempty"`
}

// Column is a logical type with its nullability and generated flag. In the
// file it is one string: "<type>", with "?" appended when the column is
// nullable and " generated" when the database computes it.
type Column struct {
	Type      string
	Nullable  bool
	Generated bool
}

// MarshalYAML writes the compact form.
func (c Column) MarshalYAML() (any, error) {
	text := c.Type
	if c.Nullable {
		text += "?"
	}
	if c.Generated {
		text += " generated"
	}
	return text, nil
}

// UnmarshalYAML reads the compact form.
func (c *Column) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return err
	}
	text, c.Generated = strings.CutSuffix(text, " generated")
	text, c.Nullable = strings.CutSuffix(text, "?")
	if text == "" || strings.ContainsAny(text, " ?") {
		return fmt.Errorf("line %d: %q is not a column type", node.Line, node.Value)
	}
	c.Type = text
	return nil
}

func (c Column) String() string {
	text, _ := c.MarshalYAML()
	return text.(string)
}

// Header is written at the top of the checked-in file.
const Header = `# The dialect-neutral schema catalogue (ADR 0022, slice 3b).
#
# Generated from a migrated PostgreSQL with
#   go run ./tools/schemaparity generate
# and verified against PostgreSQL and MySQL with
#   make schema-parity
# Do not edit by hand. Column types are logical types (see internal/schemaparity/types.go):
# "?" marks a nullable column, "generated" a computed one. Partial and expression
# indexes are not listed.
`

// Load reads a catalogue file.
func Load(path string) (Catalogue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalogue{}, err
	}
	catalogue, err := parse(data)
	if err != nil {
		return Catalogue{}, fmt.Errorf("read catalogue %s: %w", path, err)
	}
	return catalogue, nil
}

func parse(data []byte) (Catalogue, error) {
	var catalogue Catalogue
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalogue); err != nil {
		return Catalogue{}, err
	}
	if catalogue.Version != CatalogueVersion {
		return Catalogue{}, fmt.Errorf("the catalogue has version %d; this tool reads version %d", catalogue.Version, CatalogueVersion)
	}
	return catalogue, nil
}

// Marshal renders the catalogue file, header included.
func Marshal(catalogue Catalogue) ([]byte, error) {
	catalogue.Version = CatalogueVersion
	var out strings.Builder
	out.WriteString(Header)
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(catalogue); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// TableNames returns the table names in sorted order.
func (c Catalogue) TableNames() []string {
	names := make([]string, 0, len(c.Tables))
	for name := range c.Tables {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
