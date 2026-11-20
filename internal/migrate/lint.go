package migrate

import (
	"fmt"
	"slices"
)

// Finding is one violation of the re-runnable-migration rules.
type Finding struct {
	File string
	// Statement is the 1-based position of the statement in the file, 0 for a
	// problem with the file as a whole.
	Statement int
	Message   string
}

func (f Finding) String() string {
	if f.Statement == 0 {
		return fmt.Sprintf("%s: %s", f.File, f.Message)
	}
	return fmt.Sprintf("%s: statement %d: %s", f.File, f.Statement, f.Message)
}

// baselineFile is exempt from the lint: it only ever runs against an empty database.
const baselineFile = "000001_baseline.sql"

// LintDir checks the MySQL migrations of dir after the baseline. MySQL has no
// transactional DDL and no guard for most DDL, so a crash can leave a file half
// applied and `notarx migrate --repair` resumes it statement by statement
// (ADR 0022). That only works when the file is written for it:
//
//   - one DDL operation per unit: an ALTER TABLE carries a single specification;
//   - a data backfill can run twice: INSERT ... ON DUPLICATE KEY UPDATE (never
//     INSERT IGNORE or REPLACE), UPDATE and DELETE with a WHERE clause;
//   - what has a guard uses it: CREATE TABLE IF NOT EXISTS, DROP TABLE IF EXISTS,
//     CREATE OR REPLACE VIEW, CREATE TRIGGER IF NOT EXISTS;
//   - nothing the splitter cannot run: DELIMITER, routines, BEGIN ... END bodies.
func LintDir(dir string) ([]Finding, error) {
	files, err := LoadDir(dir)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, file := range files {
		if file.Name == baselineFile {
			continue
		}
		findings = append(findings, LintSQL(file.Name, file.Body)...)
	}
	return findings, nil
}

// LintSQL checks one file body, which must already be stripped of its Down section.
func LintSQL(name, body string) []Finding {
	statements, err := parseStatements(body)
	if err != nil {
		return []Finding{{File: name, Message: err.Error()}}
	}
	var findings []Finding
	for i, s := range statements {
		for _, message := range lintStatement(s) {
			findings = append(findings, Finding{File: name, Statement: i + 1, Message: message})
		}
	}
	return findings
}

func lintStatement(s statement) []string {
	if slices.ContainsFunc(s.tokens, func(t token) bool { return t.kind == tokHint }) {
		return []string{"executable comments (/*! ... */) are not linted; write the statement plainly"}
	}
	words := s.words()
	if len(words) == 0 {
		return nil
	}
	has := func(sequence ...string) bool { return containsSequence(words, sequence) }
	switch words[0] {
	case "TRUNCATE":
		return []string{"TRUNCATE would wipe rows a later statement backfilled when it is replayed; use DELETE ... WHERE"}
	case "CREATE":
		return lintCreate(words, has)
	case "ALTER":
		if len(words) < 2 || words[1] != "TABLE" {
			return []string{"only ALTER TABLE is allowed"}
		}
		if s.topLevelCommas() > 0 {
			return []string{"ALTER TABLE must carry one specification per statement"}
		}
	case "DROP":
		if len(words) > 1 && (words[1] == "TABLE" || words[1] == "VIEW" || words[1] == "TRIGGER") && !has("IF", "EXISTS") {
			return []string{"DROP " + words[1] + " needs IF EXISTS"}
		}
	case "RENAME", "SET":
	case "INSERT":
		if len(words) > 1 && words[1] == "IGNORE" {
			return []string{"INSERT IGNORE also swallows foreign-key and truncation errors; use ON DUPLICATE KEY UPDATE"}
		}
		if !has("ON", "DUPLICATE", "KEY", "UPDATE") {
			return []string{"a backfill INSERT must end in ON DUPLICATE KEY UPDATE so it can run twice"}
		}
	case "REPLACE":
		return []string{"REPLACE deletes and re-inserts rows; use INSERT ... ON DUPLICATE KEY UPDATE"}
	case "UPDATE", "DELETE":
		if !slices.Contains(words, "WHERE") {
			return []string{words[0] + " without WHERE is not re-runnable"}
		}
	default:
		return []string{"statement " + words[0] + " is not allowed in a migration"}
	}
	return nil
}

func lintCreate(words []string, has func(...string) bool) []string {
	switch createKind(words) {
	case "TABLE":
		if !has("IF", "NOT", "EXISTS") {
			return []string{"CREATE TABLE needs IF NOT EXISTS"}
		}
	case "VIEW":
		if !has("OR", "REPLACE") {
			return []string{"CREATE VIEW needs OR REPLACE"}
		}
	case "TRIGGER":
		if !has("IF", "NOT", "EXISTS") {
			return []string{"CREATE TRIGGER needs IF NOT EXISTS"}
		}
	case "INDEX":
		// MySQL has no guard for an index: the statement counter covers it.
	default:
		return []string{"unsupported CREATE statement"}
	}
	return nil
}

func containsSequence(words, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(words); i++ {
		if slices.Equal(words[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

// topLevelCommas counts the commas outside parentheses.
func (s statement) topLevelCommas() int {
	depth, commas := 0, 0
	for _, t := range s.tokens {
		if t.kind != tokSymbol {
			continue
		}
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				commas++
			}
		}
	}
	return commas
}
