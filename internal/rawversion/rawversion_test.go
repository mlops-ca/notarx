package rawversion

import (
	"strings"
	"testing"
)

func TestIdentifierRules(t *testing.T) {
	for _, name := range []string{"tool", "a", "tool_x.y-z", strings.Repeat("a", 128)} {
		if !ValidName(name) {
			t.Errorf("name %q must be valid", name)
		}
	}
	for _, name := range []string{"", "Tool", "-tool", ".tool", "to ol", "tôol", strings.Repeat("a", 129), "a/b"} {
		if ValidName(name) {
			t.Errorf("name %q must be invalid", name)
		}
	}
	for _, version := range []string{"1", "1.0.0", "1.0.0-RC1+build~7", "v2_beta", strings.Repeat("1", 128)} {
		if !ValidVersion(version) {
			t.Errorf("version %q must be valid", version)
		}
	}
	for _, version := range []string{"", "-1", "+1", "1 0", "1:0", strings.Repeat("1", 129)} {
		if ValidVersion(version) {
			t.Errorf("version %q must be invalid", version)
		}
	}
	for key, want := range map[string]bool{"git.sha": true, "a": true, "build-number_2": true, "": false, "Git": false, "1k": false, "k k": false, strings.Repeat("k", 65): false} {
		if ValidPropertyKey(key) != want {
			t.Errorf("property key %q: want %v", key, want)
		}
	}
}

func TestFileSegmentsAreSafe(t *testing.T) {
	for _, segment := range []string{"file", "file name", "%20x", ".hidden", "a.tar.gz.sha256", "tôol"} {
		if !ValidFileSegment(segment) {
			t.Errorf("segment %q must be valid", segment)
		}
	}
	for _, segment := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b", "a\nb", "a\u202eb", "a\u2066b"} {
		if ValidFileSegment(segment) {
			t.Errorf("segment %q must be refused", segment)
		}
	}
}
