package objectstore

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// ValidateKey reports whether key is acceptable to every object store driver.
// It applies the strictest driver rules (the filesystem driver), so a key that
// passes is also valid for the S3 driver.
func ValidateKey(key string) error { return validateFileKey(key) }

// MaxKeyBytes is the longest key every driver accepts.
const MaxKeyBytes = fsMaxKeyBytes

// SafeKeySegment returns a deterministic, single-segment form of a
// user-controlled name that is valid for every driver. A name that is already
// valid is returned unchanged. Any other name (empty, dot-leading, containing a
// slash, percent sign, control character, backslash, invalid UTF-8, or too long)
// is percent-encoded, or replaced by a digest when the encoding is still too
// long. Every rewritten form contains a percent sign and every unchanged name
// does not, so distinct names never map to the same segment.
func SafeKeySegment(name string) string {
	if segmentIsSafe(name) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		unreserved := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '~' || (c == '.' && i > 0)
		if unreserved {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte("0123456789ABCDEF"[c>>4])
		b.WriteByte("0123456789ABCDEF"[c&15])
	}
	encoded := b.String()
	if encoded == "" {
		return "%empty"
	}
	if len(encoded) > fsMaxSegmentBytes {
		sum := sha256.Sum256([]byte(name))
		return "%sha256-" + hex.EncodeToString(sum[:])
	}
	return encoded
}

// SafeKeyPath applies SafeKeySegment to every slash-separated segment of a
// user-controlled relative path. When the result is longer than maxBytes the
// whole path is replaced by a single digest segment, keeping the key
// deterministic and within MaxKeyBytes once combined with a fixed prefix.
func SafeKeyPath(userPath string, maxBytes int) string {
	parts := strings.Split(userPath, "/")
	for i, part := range parts {
		parts[i] = SafeKeySegment(part)
	}
	out := strings.Join(parts, "/")
	if len(out) > maxBytes {
		sum := sha256.Sum256([]byte(userPath))
		return "%sha256-" + hex.EncodeToString(sum[:])
	}
	return out
}

// KeySegmentUsable reports whether s is already a valid single key segment for
// every driver. Unlike SafeKeySegment it accepts percent signs, for callers that
// percent-encode a name themselves and want to keep their existing keys.
func KeySegmentUsable(s string) bool { return segmentValid(s, true) }

func segmentIsSafe(s string) bool { return segmentValid(s, false) }

func segmentValid(s string, allowPercent bool) bool {
	if s == "" || len(s) > fsMaxSegmentBytes || s[0] == '.' || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' || r == '/' || r == '%' && !allowPercent {
			return false
		}
	}
	return true
}
