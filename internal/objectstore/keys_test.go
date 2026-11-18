package objectstore

import (
	"strings"
	"testing"
)

var hostileNames = []string{"", ".", "..", ".hidden", ".conan", "a/b", "a\\b", "50%", "%2Ehidden", "tab\there", "nul\x00", "bad\xff", strings.Repeat("x", 300), strings.Repeat("\u00e9", 200), "tmp", "meta"}

func TestSafeKeySegmentIsValidDeterministicAndInjective(t *testing.T) {
	seen := map[string]string{}
	for _, name := range hostileNames {
		got := SafeKeySegment(name)
		if err := ValidateKey("native/x/" + got); err != nil {
			t.Errorf("SafeKeySegment(%q) = %q: %v", name, got, err)
		}
		if strings.Contains(got, "/") {
			t.Errorf("SafeKeySegment(%q) = %q contains a slash", name, got)
		}
		if again := SafeKeySegment(name); again != got {
			t.Errorf("SafeKeySegment(%q) is not deterministic", name)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("%q and %q both map to %q", prev, name, got)
		}
		seen[got] = name
	}
	if got := SafeKeySegment("pkg-1.0.tar.gz"); got != "pkg-1.0.tar.gz" {
		t.Errorf("valid name rewritten to %q", got)
	}
}

func TestSafeKeyPathStaysValid(t *testing.T) {
	for _, p := range []string{"a/../b", "a//b", ".conan/conanfile.py", "/lead", "trail/", strings.Repeat("seg/", 400), strings.Repeat("y", 2000)} {
		key := "native/conan/migration/id/session/" + strings.Repeat("a", 64) + "/" + SafeKeyPath(p, 600)
		if err := ValidateKey(key); err != nil {
			t.Errorf("path %.40q: %v", p, err)
		}
	}
	if got := SafeKeyPath("a/b/c.txt", 600); got != "a/b/c.txt" {
		t.Errorf("valid path rewritten to %q", got)
	}
}
