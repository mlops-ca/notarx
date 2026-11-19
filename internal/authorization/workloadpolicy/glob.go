package workloadpolicy

import (
	"errors"
	"unicode/utf8"
)

// Glob semantics: a single star matches any run of characters except "/", a double
// star (any longer run of stars is the same) matches any run including "/", every
// other character is literal, and the whole value must match (anchored).

type globKind uint8

const (
	globLiteral globKind = iota
	globStar
	globDoubleStar
)

type globToken struct {
	kind globKind
	r    rune
}

func compileGlob(pattern string) ([]globToken, error) {
	if !utf8.ValidString(pattern) {
		return nil, errors.New("glob is not valid UTF-8")
	}
	var tokens []globToken
	runes := []rune(pattern)
	for index := 0; index < len(runes); {
		if runes[index] != '*' {
			tokens = append(tokens, globToken{kind: globLiteral, r: runes[index]})
			index++
			continue
		}
		run := 0
		for index < len(runes) && runes[index] == '*' {
			run++
			index++
		}
		if run == 1 {
			tokens = append(tokens, globToken{kind: globStar})
		} else {
			tokens = append(tokens, globToken{kind: globDoubleStar})
		}
	}
	return tokens, nil
}

// bareWildcard reports a glob made only of stars, which restricts nothing.
func bareWildcard(tokens []globToken) bool {
	for _, token := range tokens {
		if token.kind == globLiteral {
			return false
		}
	}
	return true
}

// globMatch is a linear-state matcher: no backtracking, so a hostile value or pattern
// cannot cause exponential work (cost is tokens times input length).
func globMatch(tokens []globToken, value string) bool {
	reach := make([]bool, len(tokens)+1)
	next := make([]bool, len(tokens)+1)
	reach[0] = true
	closure := func(states []bool) {
		for index, token := range tokens {
			if states[index] && token.kind != globLiteral {
				states[index+1] = true
			}
		}
	}
	closure(reach)
	for _, r := range value {
		clear(next)
		for index, token := range tokens {
			if !reach[index] {
				continue
			}
			switch token.kind {
			case globLiteral:
				if token.r == r {
					next[index+1] = true
				}
			case globStar:
				if r != '/' {
					next[index] = true
				}
			case globDoubleStar:
				next[index] = true
			}
		}
		closure(next)
		reach, next = next, reach
	}
	return reach[len(tokens)]
}

// Glob reports whether value matches pattern. An invalid pattern never matches.
func Glob(pattern, value string) bool {
	tokens, err := compileGlob(pattern)
	return err == nil && globMatch(tokens, value)
}
