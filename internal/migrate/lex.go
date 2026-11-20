package migrate

import (
	"errors"
	"fmt"
	"strings"
)

type tokenKind int

const (
	tokWord tokenKind = iota
	tokString
	tokQuoted
	tokSymbol
	// tokHint is an executable comment (/*! ... */ or /*+ ... */), which MySQL runs.
	tokHint
)

type token struct {
	kind tokenKind
	// text is upper-cased for a word and the raw source for every other kind.
	text       string
	start, end int
}

// lex splits MySQL text into tokens. Plain comments are dropped, strings and
// quoted identifiers are single tokens, so a semicolon inside either never ends
// a statement. It does not interpret DELIMITER, which the splitter rejects.
func lex(src string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#':
			i = skipLine(src, i)
		case c == '-' && strings.HasPrefix(src[i:], "--") && (i+2 >= len(src) || isSpaceOrControl(src[i+2])):
			i = skipLine(src, i)
		case c == '/' && strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated block comment")
			}
			stop := i + 2 + end + 2
			if i+2 < len(src) && (src[i+2] == '!' || src[i+2] == '+') {
				tokens = append(tokens, token{kind: tokHint, text: src[i:stop], start: i, end: stop})
			}
			i = stop
		case c == '\'' || c == '"':
			stop, err := scanQuoted(src, i, c, true)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokString, text: src[i:stop], start: i, end: stop})
			i = stop
		case c == '`':
			stop, err := scanQuoted(src, i, c, false)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokQuoted, text: src[i:stop], start: i, end: stop})
			i = stop
		case isWordByte(c):
			stop := i
			for stop < len(src) && isWordByte(src[stop]) {
				stop++
			}
			tokens = append(tokens, token{kind: tokWord, text: strings.ToUpper(src[i:stop]), start: i, end: stop})
			i = stop
		default:
			tokens = append(tokens, token{kind: tokSymbol, text: src[i : i+1], start: i, end: i + 1})
			i++
		}
	}
	return tokens, nil
}

func skipLine(src string, from int) int {
	if end := strings.IndexByte(src[from:], '\n'); end >= 0 {
		return from + end + 1
	}
	return len(src)
}

func isSpaceOrControl(c byte) bool { return c <= ' ' }

func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' || c == '@' || c >= 0x80
}

// scanQuoted returns the offset just past the closing quote that matches the
// opening quote at src[from]. A doubled quote is an escaped quote; backslash
// escapes apply to string literals only.
func scanQuoted(src string, from int, quote byte, backslash bool) (int, error) {
	for i := from + 1; i < len(src); i++ {
		switch {
		case backslash && src[i] == '\\':
			i++
		case src[i] == quote:
			if i+1 < len(src) && src[i+1] == quote {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated quoted text starting at offset %d", from)
}
