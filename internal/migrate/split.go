package migrate

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// statement is one ;-separated unit of a migration file.
type statement struct {
	text   string
	tokens []token
}

// goose Down sections are never applied; the PostgreSQL runner cuts the file at
// the first such line, and so does this one for both dialects.
const gooseDown = "-- +goose Down"

// StripDown removes the "-- +goose Down" section, from the first line that
// contains the marker to the end of the file, as scripts/run-migrations.sh does.
func StripDown(body string) string {
	offset := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		if strings.Contains(line, gooseDown) {
			return body[:offset]
		}
		offset += len(line)
	}
	return body
}

// SplitStatements splits MySQL migration text into statements. It understands
// string literals, quoted identifiers and comments, and refuses what the runner
// cannot execute one statement at a time: the DELIMITER command, stored
// routines, events and triggers with a BEGIN ... END body.
func SplitStatements(src string) ([]string, error) {
	statements, err := parseStatements(src)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(statements))
	for i, s := range statements {
		texts[i] = s.text
	}
	return texts, nil
}

var errCompound = errors.New("stored routines, events and BEGIN ... END bodies are not supported; use one plain statement per unit")

func parseStatements(src string) ([]statement, error) {
	tokens, err := lex(src)
	if err != nil {
		return nil, err
	}
	var statements []statement
	start := 0
	flush := func(end int) error {
		group := tokens[start:end]
		start = end + 1
		if len(group) == 0 {
			return nil
		}
		if group[0].kind == tokWord && group[0].text == "DELIMITER" {
			return errors.New("the DELIMITER command is not supported")
		}
		statements = append(statements, statement{text: src[group[0].start:group[len(group)-1].end], tokens: group})
		return nil
	}
	for i, t := range tokens {
		if t.kind == tokSymbol && t.text == ";" {
			if err := flush(i); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(len(tokens)); err != nil {
		return nil, err
	}
	for i, s := range statements {
		if compound(s) {
			return nil, fmt.Errorf("statement %d: %w", i+1, errCompound)
		}
	}
	return statements, nil
}

// createModifiers are the words that may sit between CREATE and the kind of object it creates.
var createModifiers = map[string]bool{
	"OR": true, "REPLACE": true, "TEMPORARY": true, "UNIQUE": true, "FULLTEXT": true, "SPATIAL": true, "DEFINER": true, "CURRENT_USER": true,
	"SQL": true, "SECURITY": true, "INVOKER": true, "ALGORITHM": true, "UNDEFINED": true, "MERGE": true, "TEMPTABLE": true, "AGGREGATE": true,
}

// createKind returns what a CREATE statement creates (TABLE, INDEX, VIEW, TRIGGER, ...), or "" for any other statement.
// Words such as EVENT or FUNCTION are only keywords right after the modifiers; later they are plain identifiers.
func createKind(words []string) string {
	if len(words) == 0 || words[0] != "CREATE" {
		return ""
	}
	for _, word := range words[1:] {
		if createModifiers[word] || strings.HasPrefix(word, "@") {
			continue
		}
		return word
	}
	return ""
}

// compound reports a CREATE of a routine, an event, or a trigger whose body is a
// BEGIN ... END block, none of which can be split on semicolons.
func compound(s statement) bool {
	words := s.words()
	switch createKind(words) {
	case "PROCEDURE", "FUNCTION", "EVENT":
		return true
	case "TRIGGER":
		return slices.Contains(words, "BEGIN")
	}
	return false
}

// words returns the upper-cased word tokens at parenthesis depth 0.
func (s statement) words() []string {
	var out []string
	depth := 0
	for _, t := range s.tokens {
		switch {
		case t.kind == tokSymbol && t.text == "(":
			depth++
		case t.kind == tokSymbol && t.text == ")":
			depth--
		case t.kind == tokWord && depth == 0:
			out = append(out, t.text)
		}
	}
	return out
}
