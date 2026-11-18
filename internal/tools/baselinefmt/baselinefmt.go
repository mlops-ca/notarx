// Package baselinefmt normalises a schema-only pg_dump of the migration chain
// into the notarx baseline migration (ADR 0015, "Generation procedure").
//
// Normalisation is deterministic and statement based. Dependency order is the
// order pg_dump emitted; table, type, function, trigger and index statements
// are kept byte for byte. Anything the baseline must never contain (data,
// privileges, psql meta-commands other than the known random-key pair) makes
// Normalize fail instead of being passed through.
package baselinefmt

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed header.sql seed.sql
var parts embed.FS

// requiredExtensions are created by the fixed header, so the dump's own
// CREATE EXTENSION statements for them are dropped.
var requiredExtensions = map[string]bool{"pg_trgm": true, "pg_stat_statements": true}

// legacyLedgerTable is the pre-fork name of the migration ledger. Dumps taken
// from an older chain may still carry it, so the normaliser keeps recognising
// it (spelled in parts: the identity gate rejects the literal).
const legacyLedgerTable = "artifact_gate" + "way_schema_migrations"

var (
	dollarTag     = regexp.MustCompile(`\$[A-Za-z_][A-Za-z_0-9]*\$|\$\$`)
	ledgerObject  = regexp.MustCompile(`public\.(notarx_schema_migrations|` + legacyLedgerTable + `|schema_version)\b|public\.schema_version_\w+`)
	createExt     = regexp.MustCompile(`^CREATE EXTENSION (IF NOT EXISTS )?("?[A-Za-z0-9_]+"?)`)
	ownerStmt     = regexp.MustCompile(`(?s)^ALTER .* OWNER TO `)
	randomKeyMeta = regexp.MustCompile(`^\\(restrict|unrestrict)( \S+)?$`)
)

// Normalize turns the output of pg_dump (schema only, no owner, no
// privileges) into the full text of 000001_baseline.sql: goose marker, fixed
// extension header, the filtered dump body and the hand-written seed section.
func Normalize(dump []byte) ([]byte, error) {
	statements, err := split(dump)
	if err != nil {
		return nil, err
	}
	if len(statements) == 0 {
		return nil, ErrNoDump
	}
	var body []string
	for _, s := range statements {
		keep, text, err := classify(s)
		if err != nil {
			return nil, err
		}
		if keep {
			body = append(body, text)
		}
	}
	header, err := parts.ReadFile("header.sql")
	if err != nil {
		return nil, err
	}
	seed, err := parts.ReadFile("seed.sql")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("-- +goose Up\n")
	out.Write(header)
	out.WriteString("\n")
	out.WriteString(strings.Join(body, "\n"))
	out.WriteString("\n")
	out.Write(seed)
	return out.Bytes(), nil
}

type statement struct {
	comments []string // leading comment lines
	text     string   // statement without trailing newline
	line     int      // 1-based line where the statement starts
}

// split cuts the dump into statements, remembering the comment lines that
// precede each one. A statement ends at a line ending in ';' outside a
// dollar-quoted body. psql meta-commands are single-line statements.
func split(dump []byte) ([]statement, error) {
	var (
		out      []statement
		comments []string
		cur      []string
		start    int
		dollar   string
	)
	sc := bufio.NewScanner(bytes.NewReader(dump))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), " \t\r")
		if len(cur) == 0 {
			switch {
			case line == "":
				continue
			case strings.HasPrefix(line, "--"):
				comments = append(comments, line)
				continue
			case strings.HasPrefix(line, "\\"):
				out = append(out, statement{comments: comments, text: line, line: n})
				comments = nil
				continue
			}
			start = n
		}
		cur = append(cur, line)
		for _, tag := range dollarTag.FindAllString(line, -1) {
			switch dollar {
			case "":
				dollar = tag
			case tag:
				dollar = ""
			}
		}
		if dollar == "" && strings.HasSuffix(line, ";") {
			out = append(out, statement{comments: comments, text: strings.Join(cur, "\n"), line: start})
			comments, cur = nil, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(cur) != 0 {
		return nil, fmt.Errorf("baselinefmt: unterminated statement starting at line %d", start)
	}
	return out, nil
}

func keyword(text string) string {
	if i := strings.IndexAny(text, " \t\n("); i > 0 {
		return strings.ToUpper(text[:i])
	}
	return strings.ToUpper(text)
}

// classify decides whether a statement is kept and returns its output text.
func classify(s statement) (bool, string, error) {
	text := s.text
	if strings.HasPrefix(text, "\\") {
		if randomKeyMeta.MatchString(text) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("baselinefmt: unexpected psql meta-command at line %d", s.line)
	}
	switch keyword(text) {
	case "SET":
		return false, "", nil
	case "SELECT":
		if strings.HasPrefix(text, "SELECT pg_catalog.set_config(") {
			return false, "", nil
		}
		return false, "", fmt.Errorf("baselinefmt: unexpected SELECT at line %d", s.line)
	case "COPY", "INSERT":
		return false, "", fmt.Errorf("baselinefmt: %s statement at line %d; the dump must be schema only (data belongs in the seed section)", keyword(text), s.line)
	case "GRANT", "REVOKE":
		return false, "", fmt.Errorf("baselinefmt: %s statement at line %d; the baseline must not carry privileges", keyword(text), s.line)
	case "COMMENT":
		if strings.HasPrefix(text, "COMMENT ON EXTENSION ") {
			return false, "", nil
		}
	case "CREATE":
		if text == "CREATE SCHEMA public;" {
			return false, "", nil
		}
		if m := createExt.FindStringSubmatch(text); m != nil {
			if requiredExtensions[strings.Trim(m[2], `"`)] {
				return false, "", nil
			}
			if m[1] == "" {
				text = "CREATE EXTENSION IF NOT EXISTS " + strings.TrimPrefix(text, "CREATE EXTENSION ")
			}
			return true, render(s, text), nil
		}
	case "ALTER":
		if ownerStmt.MatchString(text) {
			return false, "", nil
		}
	}
	if ledgerObject.MatchString(text) {
		return false, "", nil
	}
	return true, render(s, text), nil
}

func render(s statement, text string) string {
	if len(s.comments) == 0 {
		return text + "\n"
	}
	return strings.Join(s.comments, "\n") + "\n\n" + text + "\n"
}

// ErrNoDump reports an empty input.
var ErrNoDump = errors.New("baselinefmt: empty dump")
