package mysqlbaseline

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// This file translates the PostgreSQL expressions of the catalogue (CHECK constraints,
// index predicates) into MySQL 8.4 text. It is a parser for a small, closed grammar, not
// a regex rewrite (ADR 0022 rejects runtime SQL translation): anything outside the grammar
// is an error that names the construct, and the constraint or index then needs an explicit
// decision in migrations/mysql-decisions.yaml.

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokString
	tokNumber
	tokIdent  // unquoted identifier or keyword, lower-cased
	tokQuoted // "identifier"
	tokOp
)

type token struct {
	kind tokenKind
	text string // the decoded string, the number, the identifier or the operator
}

var operators = []string{"<@", "@>", "?&", "!~~*", "~~*", "!~~", "!~*", "->>", "<>", "<=", ">=", "!=", "||", "::", "~~", "!~", "~*", "->", "=", "<", ">", "+", "-", "*", "/", "%", "~", "(", ")", "[", "]", ","}

func tokenize(text string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			var value strings.Builder
			i++
			for {
				if i >= len(text) {
					return nil, fmt.Errorf("unterminated string literal in %q", text)
				}
				if text[i] == '\'' {
					if i+1 < len(text) && text[i+1] == '\'' {
						value.WriteByte('\'')
						i += 2
						continue
					}
					i++
					break
				}
				value.WriteByte(text[i])
				i++
			}
			tokens = append(tokens, token{tokString, value.String()})
		case c == '"':
			end := strings.IndexByte(text[i+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated identifier in %q", text)
			}
			tokens = append(tokens, token{tokQuoted, text[i+1 : i+1+end]})
			i += end + 2
		case c >= '0' && c <= '9':
			j := i
			for j < len(text) && (text[j] >= '0' && text[j] <= '9' || text[j] == '.') {
				j++
			}
			tokens = append(tokens, token{tokNumber, text[i:j]})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i
			for j < len(text) && (text[j] == '_' || unicode.IsLetter(rune(text[j])) || unicode.IsDigit(rune(text[j]))) {
				j++
			}
			word := strings.ToLower(text[i:j])
			if (word == "e" || word == "b" || word == "x") && j < len(text) && text[j] == '\'' {
				return nil, fmt.Errorf("the %s'...' string form in %q is not supported", strings.ToUpper(word), text)
			}
			tokens = append(tokens, token{tokIdent, word})
			i = j
		default:
			matched := false
			for _, op := range operators {
				if strings.HasPrefix(text[i:], op) {
					tokens = append(tokens, token{tokOp, op})
					i += len(op)
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("unexpected character %q in %q", string(c), text)
			}
		}
	}
	return append(tokens, token{kind: tokEOF}), nil
}

// node is a translated sub-expression.
type node struct {
	sql      string
	str      *string  // set for a string literal: the decoded value
	items    []string // set for an ARRAY[...] literal: the translated elements
	array    bool
	num      bool     // a number literal
	ident    bool     // a bare column reference
	strs     []string // set for an ARRAY of string literals: the decoded values
	jsonType bool     // JSON_TYPE(...) of jsonb_typeof(...)
}

type parser struct {
	tokens  []token
	pos     int
	columns map[string]bool // identifiers used, in encounter order irrelevant
	source  string
}

// Translation is the MySQL form of a PostgreSQL expression and the columns it reads.
type Translation struct {
	SQL     string
	Columns []string
}

// Translate converts a PostgreSQL boolean or value expression into MySQL SQL.
func Translate(expression string) (Translation, error) {
	tokens, err := tokenize(expression)
	if err != nil {
		return Translation{}, err
	}
	p := &parser{tokens: tokens, columns: map[string]bool{}, source: expression}
	n, err := p.or()
	if err != nil {
		return Translation{}, fmt.Errorf("%w in %q", err, expression)
	}
	if p.peek().kind != tokEOF {
		return Translation{}, fmt.Errorf("unexpected %q in %q", p.peek().text, expression)
	}
	if n.array {
		return Translation{}, fmt.Errorf("a bare ARRAY in %q", expression)
	}
	if n.ident {
		// MySQL refuses a CHECK whose whole expression is a column ("non-boolean type", error 3812).
		n.sql = "(" + n.sql + " <> 0)"
	}
	columns := make([]string, 0, len(p.columns))
	for c := range p.columns {
		columns = append(columns, c)
	}
	slices.Sort(columns)
	return Translation{SQL: n.sql, Columns: columns}, nil
}

func (p *parser) peek() token { return p.tokens[p.pos] }
func (p *parser) next() token {
	t := p.tokens[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) isWord(word string) bool {
	t := p.peek()
	return t.kind == tokIdent && t.text == word
}

func (p *parser) isOp(op string) bool {
	t := p.peek()
	return t.kind == tokOp && t.text == op
}

func (p *parser) acceptWord(word string) bool {
	if p.isWord(word) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) acceptOp(op string) bool {
	if p.isOp(op) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expectOp(op string) error {
	if !p.acceptOp(op) {
		return fmt.Errorf("expected %q, found %q", op, p.peek().text)
	}
	return nil
}

func (p *parser) or() (node, error) {
	left, err := p.and()
	if err != nil {
		return node{}, err
	}
	for p.acceptWord("or") {
		right, err := p.and()
		if err != nil {
			return node{}, err
		}
		left = node{sql: "(" + left.sql + " OR " + right.sql + ")"}
	}
	return left, nil
}

func (p *parser) and() (node, error) {
	left, err := p.not()
	if err != nil {
		return node{}, err
	}
	for p.acceptWord("and") {
		right, err := p.not()
		if err != nil {
			return node{}, err
		}
		left = node{sql: "(" + left.sql + " AND " + right.sql + ")"}
	}
	return left, nil
}

func (p *parser) not() (node, error) {
	if p.acceptWord("not") {
		inner, err := p.not()
		if err != nil {
			return node{}, err
		}
		return node{sql: "(NOT " + inner.sql + ")"}, nil
	}
	return p.compare()
}

var comparisons = map[string]string{"=": "=", "<>": "<>", "!=": "<>", "<": "<", "<=": "<=", ">": ">", ">=": ">="}

func (p *parser) compare() (node, error) {
	left, err := p.other()
	if err != nil {
		return node{}, err
	}
	t := p.peek()
	switch {
	case t.kind == tokOp && comparisons[t.text] != "":
		p.pos++
		op := comparisons[t.text]
		if p.isWord("any") || p.isWord("all") {
			return p.quantified(left, op)
		}
		right, err := p.other()
		if err != nil {
			return node{}, err
		}
		return compareNodes(left, op, right)
	case t.kind == tokOp && (t.text == "<@" || t.text == "@>"):
		p.pos++
		right, err := p.other()
		if err != nil {
			return node{}, err
		}
		if t.text == "<@" {
			return node{sql: "JSON_CONTAINS(" + jsonValue(right) + ", " + jsonValue(left) + ")"}, nil
		}
		return node{sql: "JSON_CONTAINS(" + jsonValue(left) + ", " + jsonValue(right) + ")"}, nil
	case t.kind == tokOp && t.text == "?&":
		p.pos++
		right, err := p.other()
		if err != nil {
			return node{}, err
		}
		if !right.array || right.strs == nil {
			return node{}, fmt.Errorf("?& needs an ARRAY of string literals")
		}
		paths := make([]string, len(right.strs))
		for i, key := range right.strs {
			paths[i] = "'$.\"" + escapeLiteral(key) + "\"'"
		}
		return node{sql: "JSON_CONTAINS_PATH(" + left.sql + ", 'all', " + strings.Join(paths, ", ") + ")"}, nil
	case t.kind == tokOp && (t.text == "~" || t.text == "!~" || t.text == "~*" || t.text == "!~*"):
		p.pos++
		right, err := p.other()
		if err != nil {
			return node{}, err
		}
		flags := "'c'"
		if strings.HasSuffix(t.text, "*") {
			flags = "'i'"
		}
		call := "REGEXP_LIKE(" + left.sql + ", " + right.sql + ", " + flags + ")"
		if strings.HasPrefix(t.text, "!") {
			call = "(NOT " + call + ")"
		}
		return node{sql: call}, nil
	case t.kind == tokOp && (t.text == "~~" || t.text == "!~~" || t.text == "~~*" || t.text == "!~~*"):
		p.pos++
		right, err := p.other()
		if err != nil {
			return node{}, err
		}
		l, r := left.sql, right.sql
		if strings.HasSuffix(t.text, "*") {
			l, r = "LOWER("+l+")", "LOWER("+r+")"
		}
		op := " LIKE "
		if strings.HasPrefix(t.text, "!") {
			op = " NOT LIKE "
		}
		return node{sql: "(" + l + op + r + ")"}, nil
	case t.kind == tokIdent && t.text == "is":
		p.pos++
		negated := p.acceptWord("not")
		switch {
		case p.acceptWord("null"):
			if negated {
				return node{sql: "(" + left.sql + " IS NOT NULL)"}, nil
			}
			return node{sql: "(" + left.sql + " IS NULL)"}, nil
		case p.acceptWord("true"):
			return node{sql: "(" + left.sql + map[bool]string{false: " IS TRUE)", true: " IS NOT TRUE)"}[negated]}, nil
		case p.acceptWord("false"):
			return node{sql: "(" + left.sql + map[bool]string{false: " IS FALSE)", true: " IS NOT FALSE)"}[negated]}, nil
		case p.acceptWord("distinct"):
			if !p.acceptWord("from") {
				return node{}, fmt.Errorf("expected FROM after IS DISTINCT")
			}
			right, err := p.other()
			if err != nil {
				return node{}, err
			}
			if negated {
				return node{sql: "(" + left.sql + " <=> " + right.sql + ")"}, nil
			}
			return node{sql: "(NOT (" + left.sql + " <=> " + right.sql + "))"}, nil
		}
		return node{}, fmt.Errorf("unsupported IS form")
	}
	return left, nil
}

// jsonValue renders a node as a JSON document: an ARRAY literal becomes JSON_ARRAY.
func jsonValue(n node) string {
	if n.array {
		return "JSON_ARRAY(" + strings.Join(n.items, ", ") + ")"
	}
	return n.sql
}

func compareNodes(left node, op string, right node) (node, error) {
	if left.jsonType && right.str != nil {
		mapped, ok := map[string]string{"object": "OBJECT", "array": "ARRAY", "string": "STRING", "boolean": "BOOLEAN", "null": "NULL"}[*right.str]
		if !ok {
			return node{}, fmt.Errorf("jsonb_typeof compared with %q has no MySQL JSON_TYPE equivalent", *right.str)
		}
		return node{sql: "(" + left.sql + " " + op + " '" + mapped + "')"}, nil
	}
	return node{sql: "(" + left.sql + " " + op + " " + right.sql + ")"}, nil
}

// quantified handles "= ANY (ARRAY[...])" and "<> ALL (ARRAY[...])", which become IN and NOT IN.
func (p *parser) quantified(left node, op string) (node, error) {
	quantifier := p.next().text
	if err := p.expectOp("("); err != nil {
		return node{}, err
	}
	list, err := p.or()
	if err != nil {
		return node{}, err
	}
	if err := p.expectOp(")"); err != nil {
		return node{}, err
	}
	if !list.array || len(list.items) == 0 {
		return node{}, fmt.Errorf("%s %s needs a non-empty ARRAY[...] literal", op, strings.ToUpper(quantifier))
	}
	switch {
	case op == "=" && quantifier == "any":
		return node{sql: "(" + left.sql + " IN (" + strings.Join(list.items, ", ") + "))"}, nil
	case op == "<>" && quantifier == "all":
		return node{sql: "(" + left.sql + " NOT IN (" + strings.Join(list.items, ", ") + "))"}, nil
	}
	return node{}, fmt.Errorf("%s %s over an array is not supported", op, strings.ToUpper(quantifier))
}

func (p *parser) other() (node, error) {
	left, err := p.add()
	if err != nil {
		return node{}, err
	}
	for {
		switch {
		case p.acceptOp("||"):
			right, err := p.add()
			if err != nil {
				return node{}, err
			}
			left = node{sql: "CONCAT(" + left.sql + ", " + right.sql + ")"}
		case p.isOp("->>"):
			p.pos++
			key := p.next()
			if key.kind != tokString {
				return node{}, fmt.Errorf("->> needs a string literal key")
			}
			for p.acceptOp("::") {
				_, _ = p.cast()
			}
			left = node{sql: "JSON_UNQUOTE(JSON_EXTRACT(" + left.sql + ", '$.\"" + escapeLiteral(key.text) + "\"'))"}
		default:
			return left, nil
		}
	}
}

func (p *parser) add() (node, error) {
	left, err := p.mul()
	if err != nil {
		return node{}, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		right, err := p.mul()
		if err != nil {
			return node{}, err
		}
		left = node{sql: "(" + left.sql + " " + op + " " + right.sql + ")"}
	}
	return left, nil
}

func (p *parser) mul() (node, error) {
	left, err := p.unary()
	if err != nil {
		return node{}, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("%") {
		op := p.next().text
		right, err := p.unary()
		if err != nil {
			return node{}, err
		}
		left = node{sql: "(" + left.sql + " " + op + " " + right.sql + ")"}
	}
	return left, nil
}

func (p *parser) unary() (node, error) {
	if p.acceptOp("-") {
		inner, err := p.unary()
		if err != nil {
			return node{}, err
		}
		return node{sql: "(-" + inner.sql + ")"}, nil
	}
	n, err := p.primary()
	if err != nil {
		return node{}, err
	}
	for p.acceptOp("::") { // a cast: MySQL compares by the operand's own type, so the cast is dropped
		name, ok := p.cast()
		if !ok {
			return node{}, fmt.Errorf("expected a type name after the cast operator")
		}
		switch {
		case (name == "jsonb" || name == "json") && n.str != nil:
			n = node{sql: "CAST(" + n.sql + " AS JSON)"}
		case integerCasts[name] && !n.num:
			// A text value that PostgreSQL casts must compare as a number in MySQL too.
			n = node{sql: "CAST(" + n.sql + " AS SIGNED)"}
		case decimalCasts[name] && !n.num:
			n = node{sql: "CAST(" + n.sql + " AS DECIMAL(30,10))"}
		}
	}
	return n, nil
}

// cast consumes a type name: words, an optional (n) and []s. It returns the first word.
func (p *parser) cast() (string, bool) {
	if p.peek().kind != tokIdent {
		return "", false
	}
	name := p.peek().text
	for p.peek().kind == tokIdent {
		p.pos++
	}
	if p.isOp("(") {
		for !p.isOp(")") && p.peek().kind != tokEOF {
			p.pos++
		}
		p.pos++
	}
	for p.isOp("[") {
		p.pos += 2
	}
	return name, true
}

var (
	integerCasts = map[string]bool{"integer": true, "int": true, "int2": true, "int4": true, "int8": true, "smallint": true, "bigint": true}
	decimalCasts = map[string]bool{"numeric": true, "decimal": true, "real": true, "double": true, "float4": true, "float8": true}
)

// functions maps PostgreSQL functions to MySQL ones with the same arguments.
var functions = map[string]string{
	"length": "CHAR_LENGTH", "char_length": "CHAR_LENGTH", "character_length": "CHAR_LENGTH", "octet_length": "LENGTH",
	"lower": "LOWER", "upper": "UPPER", "substr": "SUBSTR", "left": "LEFT", "right": "RIGHT", "coalesce": "COALESCE",
	"abs": "ABS", "md5": "MD5", "btrim": "TRIM", "replace": "REPLACE",
	"cardinality": "JSON_LENGTH", "jsonb_array_length": "JSON_LENGTH",
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.kind {
	case tokString:
		value := t.text
		return node{sql: "'" + escapeLiteral(value) + "'", str: &value}, nil
	case tokNumber:
		return node{sql: t.text, num: true}, nil
	case tokQuoted:
		p.columns[t.text] = true
		return node{sql: "`" + t.text + "`"}, nil
	case tokOp:
		if t.text == "(" {
			inner, err := p.or()
			if err != nil {
				return node{}, err
			}
			if err := p.expectOp(")"); err != nil {
				return node{}, err
			}
			if inner.array || inner.str != nil {
				return inner, nil
			}
			return node{sql: inner.sql, jsonType: inner.jsonType, num: inner.num, ident: inner.ident}, nil
		}
		return node{}, fmt.Errorf("unexpected %q", t.text)
	case tokIdent:
		return p.word(t.text)
	}
	return node{}, fmt.Errorf("unexpected end")
}

func (p *parser) word(word string) (node, error) {
	switch word {
	case "true", "false", "null":
		return node{sql: strings.ToUpper(word)}, nil
	case "array":
		if err := p.expectOp("["); err != nil {
			return node{}, err
		}
		var items, strs []string
		allStrings := true
		for !p.isOp("]") {
			item, err := p.other()
			if err != nil {
				return node{}, err
			}
			items = append(items, item.sql)
			if item.str == nil {
				allStrings = false
			} else {
				strs = append(strs, *item.str)
			}
			if !p.acceptOp(",") {
				break
			}
		}
		if err := p.expectOp("]"); err != nil {
			return node{}, err
		}
		if !allStrings {
			strs = nil
		}
		return node{array: true, items: items, strs: strs}, nil
	}
	if p.isOp("(") {
		p.pos++
		var args []string
		var first node
		for !p.isOp(")") {
			arg, err := p.or()
			if err != nil {
				return node{}, err
			}
			if len(args) == 0 {
				first = arg
			}
			args = append(args, arg.sql)
			if !p.acceptOp(",") {
				break
			}
		}
		if err := p.expectOp(")"); err != nil {
			return node{}, err
		}
		if word == "jsonb_typeof" || word == "json_typeof" {
			if len(args) != 1 {
				return node{}, fmt.Errorf("%s takes one argument", word)
			}
			return node{sql: "JSON_TYPE(" + first.sql + ")", jsonType: true}, nil
		}
		if word == "split_part" {
			// Only the first field has a MySQL form: SUBSTRING_INDEX(s, delimiter, 1).
			if len(args) != 3 || args[2] != "1" {
				return node{}, fmt.Errorf("split_part() is supported for the first field only")
			}
			return node{sql: "SUBSTRING_INDEX(" + args[0] + ", " + args[1] + ", 1)"}, nil
		}
		name, ok := functions[word]
		if !ok {
			return node{}, fmt.Errorf("the function %s() has no MySQL translation", word)
		}
		return node{sql: name + "(" + strings.Join(args, ", ") + ")"}, nil
	}
	p.columns[word] = true
	return node{sql: "`" + word + "`", ident: true}, nil
}

// escapeLiteral escapes the body of a MySQL string literal: backslash is an escape
// character there, unlike in PostgreSQL with standard_conforming_strings.
func escapeLiteral(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "'", "''")
}
