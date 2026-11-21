// Package promotionrules is the rule model of ADR 0021 ("Rules"): the match document that selects artifacts, the
// conditions that gate a promotion, and their validation against what the source format can do. It evaluates a match
// against an artifact; it never reads a repository and never enqueues anything.
package promotionrules

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Masterminds/semver/v3"
	"github.com/mlops-ca/notarx/internal/rawversion"
)

// Bounds of a match document.
const (
	// MaxPatternBytes bounds a glob, a regular expression and a property glob. A regular expression is Go RE2
	// (linear time), so the bound limits compile cost and memory rather than guarding against backtracking.
	MaxPatternBytes = 512
	// MaxRefs is the most ref globs a rule lists.
	MaxRefs = 32
	// MaxRangeBytes bounds a semver range.
	MaxRangeBytes = 256
)

// Scopes of a rule. A coordinate rule promotes the artifacts that match; a release rule promotes the whole release the
// matching coordinate belongs to, as the format defines it (format.ReleaseSet).
const (
	ScopeCoordinate = "coordinate"
	ScopeRelease    = "release"
)

// Refusal codes of Validate; the HTTP layer maps them to problem codes.
const (
	// CodeInvalid is a malformed match or conditions document.
	CodeInvalid = "invalid_rule"
	// CodeUnsupportedMatch is a match member the source repository cannot satisfy (properties on a repository that is
	// not a versioned generic one, refs on a format without tags).
	CodeUnsupportedMatch = "unsupported_match"
	// CodeUnsupportedFormat is a scope the source format does not support (release without the ReleaseSet capability).
	CodeUnsupportedFormat = "unsupported_format"
)

// Error is a refusal of a rule document with a stable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func invalid(format string, args ...any) *Error {
	return &Error{Code: CodeInvalid, Message: fmt.Sprintf(format, args...)}
}

// Coordinate selects artifacts by canonical coordinate: exactly one of Glob and Regex.
type Coordinate struct {
	Glob  string `json:"glob,omitempty"`
	Regex string `json:"regex,omitempty"`
}

// VersionRange selects artifacts by semantic version.
type VersionRange struct {
	Range string `json:"range"`
}

// Match is the match document of a rule.
type Match struct {
	Coordinate Coordinate    `json:"coordinate"`
	Version    *VersionRange `json:"version,omitempty"`
	// Refs are globs over the mutable refs (OCI tags, npm dist-tags) that point at the artifact.
	Refs []string `json:"refs,omitempty"`
	// Properties maps a property key to a glob over its value. Properties exist only on versioned generic
	// repositories (ADR 0028), where they are frozen when a version is sealed.
	Properties map[string]string `json:"properties,omitempty"`
	// Scope is "coordinate" (the default) or "release".
	Scope string `json:"scope,omitempty"`
}

// ParseMatch decodes a stored or submitted match document, refusing unknown members.
func ParseMatch(raw []byte) (Match, error) {
	var match Match
	if err := decodeStrict(raw, &match); err != nil {
		return Match{}, invalid("match is invalid: %v", err)
	}
	return match, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
}

// Marshal returns the canonical stored form of the match.
func (m Match) Marshal() ([]byte, error) { return json.Marshal(m) }

// EffectiveScope returns the scope with the default applied.
func (m Match) EffectiveScope() string {
	if m.Scope == "" {
		return ScopeCoordinate
	}
	return m.Scope
}

// Source describes what the source repository of a rule can do; the app derives it from the repository and the format
// registry.
type Source struct {
	// VersionedGeneric is true for a versioned generic (Raw) repository, the only kind with properties.
	VersionedGeneric bool
	// Tagged is true when the format declares format.Tagged.
	Tagged bool
	// ReleaseSet is true when the format declares format.ReleaseSet.
	ReleaseSet bool
}

// Validate checks the match document against the source: pattern syntax and size first (CodeInvalid), then what the
// source supports (CodeUnsupportedMatch, CodeUnsupportedFormat).
func (m Match) Validate(source Source) error {
	if _, err := m.Compile(); err != nil {
		return err
	}
	if m.Scope != "" && m.Scope != ScopeCoordinate && m.Scope != ScopeRelease {
		return invalid("scope must be %q or %q", ScopeCoordinate, ScopeRelease)
	}
	if m.EffectiveScope() == ScopeRelease && !source.ReleaseSet {
		return &Error{Code: CodeUnsupportedFormat, Message: "scope release is not supported for this repository format"}
	}
	if len(m.Properties) > 0 && !source.VersionedGeneric {
		return &Error{Code: CodeUnsupportedMatch, Message: "properties can be matched only on a versioned generic repository"}
	}
	if len(m.Refs) > 0 && !source.Tagged {
		return &Error{Code: CodeUnsupportedMatch, Message: "refs can be matched only on a format with tags or dist-tags"}
	}
	return nil
}

// Compiled is a match with its patterns compiled; it is safe for concurrent use.
type Compiled struct {
	coordinate *regexp.Regexp
	version    *semver.Constraints
	refs       []*regexp.Regexp
	properties map[string]*regexp.Regexp
	scope      string
}

// Compile validates and compiles every pattern of the match.
func (m Match) Compile() (*Compiled, error) {
	compiled := &Compiled{scope: m.EffectiveScope()}
	switch {
	case m.Coordinate.Glob != "" && m.Coordinate.Regex != "":
		return nil, invalid("coordinate takes either glob or regex, not both")
	case m.Coordinate.Glob == "" && m.Coordinate.Regex == "":
		return nil, invalid("coordinate requires a glob or a regex")
	case m.Coordinate.Glob != "":
		expression, err := CompileGlob(m.Coordinate.Glob)
		if err != nil {
			return nil, invalid("coordinate glob is invalid: %v", err)
		}
		compiled.coordinate = expression
	default:
		expression, err := CompileRegex(m.Coordinate.Regex)
		if err != nil {
			return nil, invalid("coordinate regex is invalid: %v", err)
		}
		compiled.coordinate = expression
	}
	if m.Version != nil {
		if m.Version.Range == "" || len(m.Version.Range) > MaxRangeBytes || !utf8.ValidString(m.Version.Range) {
			return nil, invalid("version range must be between 1 and %d bytes", MaxRangeBytes)
		}
		constraints, err := semver.NewConstraint(m.Version.Range)
		if err != nil {
			return nil, invalid("version range is invalid: %v", err)
		}
		compiled.version = constraints
	}
	if len(m.Refs) > MaxRefs {
		return nil, invalid("refs lists at most %d globs", MaxRefs)
	}
	for _, ref := range m.Refs {
		expression, err := CompileGlob(ref)
		if err != nil {
			return nil, invalid("ref glob %q is invalid: %v", ref, err)
		}
		compiled.refs = append(compiled.refs, expression)
	}
	if len(m.Properties) > rawversion.MaxProperties {
		return nil, invalid("properties lists at most %d keys", rawversion.MaxProperties)
	}
	if len(m.Properties) > 0 {
		compiled.properties = make(map[string]*regexp.Regexp, len(m.Properties))
	}
	for key, pattern := range m.Properties {
		if !rawversion.ValidPropertyKey(key) {
			return nil, invalid("property key %q is invalid", key)
		}
		expression, err := CompileGlob(pattern)
		if err != nil {
			return nil, invalid("property %q glob is invalid: %v", key, err)
		}
		compiled.properties[key] = expression
	}
	return compiled, nil
}

// CompileGlob compiles a glob anchored to the whole value: "*" matches any run of characters except "/", "**" matches
// any run including "/", and every other character matches itself.
func CompileGlob(glob string) (*regexp.Regexp, error) {
	if err := checkPattern(glob); err != nil {
		return nil, err
	}
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(glob); {
		switch {
		case strings.HasPrefix(glob[i:], "**"):
			expression.WriteString(".*")
			i += 2
			for i < len(glob) && glob[i] == '*' {
				i++
			}
		case glob[i] == '*':
			expression.WriteString("[^/]*")
			i++
		default:
			_, size := utf8.DecodeRuneInString(glob[i:])
			expression.WriteString(regexp.QuoteMeta(glob[i : i+size]))
			i += size
		}
	}
	expression.WriteString("$")
	return regexp.Compile("(?s)" + expression.String())
}

// CompileRegex compiles a Go RE2 regular expression anchored to the whole value. RE2 has no backtracking, so the
// match runs in time linear in the value; look-around and back-references are not part of RE2 and are refused.
func CompileRegex(pattern string) (*regexp.Regexp, error) {
	if err := checkPattern(pattern); err != nil {
		return nil, err
	}
	return regexp.Compile("^(?:" + pattern + ")$")
}

func checkPattern(pattern string) error {
	switch {
	case pattern == "":
		return errors.New("pattern is empty")
	case len(pattern) > MaxPatternBytes:
		return fmt.Errorf("pattern is longer than %d bytes", MaxPatternBytes)
	case !utf8.ValidString(pattern) || strings.ContainsRune(pattern, 0):
		return errors.New("pattern is not valid text")
	}
	return nil
}

// Scope returns the scope of the compiled match.
func (c *Compiled) Scope() string { return c.scope }

// MatchesCoordinate reports whether the canonical coordinate matches the coordinate pattern.
func (c *Compiled) MatchesCoordinate(coordinate string) bool {
	return c.coordinate.MatchString(coordinate)
}

// MatchesVersion reports whether version satisfies the version range; a match without a range accepts every version.
// A version that is not a semantic version never satisfies a range: "1.2" and "latest" do not match.
func (c *Compiled) MatchesVersion(version string) bool {
	if c.version == nil {
		return true
	}
	parsed, err := semver.StrictNewVersion(version)
	if err != nil {
		return false
	}
	return c.version.Check(parsed)
}

// HasRefs reports whether the match restricts refs.
func (c *Compiled) HasRefs() bool { return len(c.refs) > 0 }

// MatchesRefs reports whether any of the refs pointing at the artifact matches any ref glob; a match without refs
// accepts every artifact.
func (c *Compiled) MatchesRefs(refs []string) bool {
	if len(c.refs) == 0 {
		return true
	}
	for _, ref := range refs {
		for _, expression := range c.refs {
			if expression.MatchString(ref) {
				return true
			}
		}
	}
	return false
}

// MatchesProperties reports whether every property glob matches the value of its key; a missing key never matches. A
// match without properties accepts every artifact.
func (c *Compiled) MatchesProperties(properties map[string]string) bool {
	keys := make([]string, 0, len(c.properties))
	for key := range c.properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := properties[key]
		if !ok || !c.properties[key].MatchString(value) {
			return false
		}
	}
	return true
}

// Candidate is one artifact offered to a match: everything the match reads is pinned at publication or frozen at seal.
type Candidate struct {
	Coordinate string
	Version    string
	Refs       []string
	Properties map[string]string
}

// Matches reports whether the candidate satisfies every member of the match. validCoordinate is the format's
// Governance.ValidCoordinate (nil when the format declares none); a coordinate it refuses is never matched, so a
// pattern is applied only to canonical coordinates.
func (c *Compiled) Matches(candidate Candidate, validCoordinate func(string) bool) bool {
	if validCoordinate != nil && !validCoordinate(candidate.Coordinate) {
		return false
	}
	return c.MatchesCoordinate(candidate.Coordinate) && c.MatchesVersion(candidate.Version) &&
		c.MatchesRefs(candidate.Refs) && c.MatchesProperties(candidate.Properties)
}
