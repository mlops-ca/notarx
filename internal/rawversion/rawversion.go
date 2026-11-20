// Package rawversion holds the identifier rules of versioned Raw repositories (ADR 0028) that the server's store guard and
// the notarxctl client both apply, so the two cannot drift apart: package name, version, property key and the limits of a
// version's files and properties.
package rawversion

import (
	"regexp"
	"strings"
	"unicode"
)

// Limits of a version (ADR 0028).
const (
	// MaxFileSegments is the most path segments a file of a version may have below its version directory.
	MaxFileSegments = 8
	// MinPathSegments is name, version and at least one file segment.
	MinPathSegments = 3
	// ReservedSegment is the first file segment no upload may use: future metadata cannot be spoofed by a file.
	ReservedSegment = ".notarx"
	// MaxProperties is the most properties a version carries.
	MaxProperties = 32
	// MaxPathBytes mirrors the longest Raw path the data plane accepts (v2contract.MaxRawPathBytes).
	MaxPathBytes = 4096
	// MaxFilesPerVersion is the largest files-per-version cap a deployment can configure; the file read of a version, and
	// the size of a seal request, are bounded by it.
	MaxFilesPerVersion = 20000
)

var (
	namePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	versionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`)
	propertyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

// ValidName reports whether name is a valid package name: lower case ASCII, so names cannot collide by case.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidVersion reports whether version is a valid version identifier; semantic versioning is not required.
func ValidVersion(version string) bool { return versionPattern.MatchString(version) }

// ValidPropertyKey reports whether key is a valid property key.
func ValidPropertyKey(key string) bool { return propertyKeyPattern.MatchString(key) }

// InvalidRune reports a control or bidirectional character, which no path segment or property value may contain.
func InvalidRune(value rune) bool {
	return unicode.IsControl(value) || value == '\u061c' || value == '\u200e' || value == '\u200f' || value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}

// ValidFileSegment reports whether one segment of a file path is safe: not empty, not a dot segment, without a
// backslash, a slash or a control or bidirectional character.
func ValidFileSegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.ContainsAny(segment, "\\/") && strings.IndexFunc(segment, InvalidRune) < 0
}
