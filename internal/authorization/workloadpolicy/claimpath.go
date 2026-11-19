package workloadpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	maxClaimPathDepth   = 8
	maxClaimSegmentSize = 256
)

// ClaimPath addresses a claim inside the claim object. The canonical form is the
// array of keys; a literal top-level key such as "kubernetes.io" is one element.
type ClaimPath []string

func (p ClaimPath) String() string {
	data, _ := json.Marshal([]string(p))
	return string(data)
}

// schemaNode lists the claim names a kind is known to carry, so that the dotted
// shorthand can be resolved by the longest existing-key match.
type schemaNode map[string]schemaNode

var kubernetesClaims = schemaNode{
	"kubernetes.io": {
		"namespace":      nil,
		"serviceaccount": {"name": nil, "uid": nil},
		"pod":            {"name": nil, "uid": nil},
		"node":           {"name": nil, "uid": nil},
	},
}

func schemaFor(kind Kind) schemaNode {
	if kind == KindKubernetes {
		return kubernetesClaims
	}
	return nil
}

// ParseClaimPath converts one of the three accepted spellings into the canonical
// array: a JSON array of keys, an RFC 6901 style pointer ("/kubernetes.io/namespace",
// with ~1 for "/" and ~0 for "~"), or a dotted shorthand ("kubernetes.io.namespace").
// The shorthand is resolved against the claims the issuer kind is known to carry and
// rejected when it has no unique reading; use the array form then.
func ParseClaimPath(kind Kind, raw json.RawMessage) (ClaimPath, error) {
	var segments []string
	if err := json.Unmarshal(raw, &segments); err == nil {
		return checkClaimPath(segments)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, errors.New("claim must be an array of keys or a string")
	}
	if strings.HasPrefix(text, "/") {
		return parsePointer(text)
	}
	return parseShorthand(kind, text)
}

func checkClaimPath(segments []string) (ClaimPath, error) {
	if len(segments) == 0 || len(segments) > maxClaimPathDepth {
		return nil, fmt.Errorf("claim path must have between 1 and %d keys", maxClaimPathDepth)
	}
	for _, segment := range segments {
		if segment == "" || len(segment) > maxClaimSegmentSize {
			return nil, fmt.Errorf("claim keys must be non-empty and at most %d bytes", maxClaimSegmentSize)
		}
	}
	return ClaimPath(append([]string(nil), segments...)), nil
}

func parsePointer(text string) (ClaimPath, error) {
	tokens := strings.Split(text[1:], "/")
	segments := make([]string, 0, len(tokens))
	for _, token := range tokens {
		for index := 0; index < len(token); index++ {
			if token[index] == '~' && (index+1 >= len(token) || (token[index+1] != '0' && token[index+1] != '1')) {
				return nil, errors.New("claim pointer has an invalid ~ escape")
			}
		}
		segments = append(segments, strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~"))
	}
	return checkClaimPath(segments)
}

func parseShorthand(kind Kind, text string) (ClaimPath, error) {
	if text == "" {
		return nil, errors.New("claim is empty")
	}
	parts := strings.Split(text, ".")
	if len(parts) == 1 {
		return checkClaimPath(parts)
	}
	readings := resolveShorthand(schemaFor(kind), parts)
	switch len(readings) {
	case 1:
		return checkClaimPath(readings[0])
	case 0:
		return nil, fmt.Errorf("claim %q has no known reading for this issuer kind; use the array form", text)
	}
	return nil, fmt.Errorf("claim %q is ambiguous for this issuer kind; use the array form", text)
}

// resolveShorthand returns every way the dot-separated parts can be read as a path of
// known keys, descending the schema; the last unknown key may only be a single part.
func resolveShorthand(node schemaNode, parts []string) [][]string {
	var readings [][]string
	for length := len(parts); length >= 1; length-- {
		key := strings.Join(parts[:length], ".")
		child, known := node[key]
		if !known {
			continue
		}
		rest := parts[length:]
		if len(rest) == 0 {
			readings = append(readings, []string{key})
			continue
		}
		for _, tail := range resolveShorthand(child, rest) {
			readings = append(readings, append([]string{key}, tail...))
		}
	}
	if len(readings) == 0 && len(parts) == 1 {
		readings = append(readings, []string{parts[0]})
	}
	return readings
}

// lookup walks the path through nested objects.
func (p ClaimPath) lookup(claims map[string]any) (any, bool) {
	var current any = claims
	for _, segment := range p {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = object[segment]; !ok {
			return nil, false
		}
	}
	return current, true
}
