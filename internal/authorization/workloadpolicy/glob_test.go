package workloadpolicy

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGlobSemantics(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/heads/main", "refs/heads/main2", false},
		{"refs/heads/main", "xrefs/heads/main", false},
		{"refs/heads/*", "refs/heads/main", true},
		{"refs/heads/*", "refs/heads/feature/x", false},
		{"refs/heads/**", "refs/heads/feature/x", true},
		{"refs/heads/**", "refs/heads/", true},
		{"refs/heads/**", "refs/tags/v1", false},
		{"refs/tags/v*", "refs/tags/v1.2.3", true},
		{"refs/tags/v*", "refs/tags/v1/2", false},
		{"repo:acme/*:ref:refs/heads/main", "repo:acme/app:ref:refs/heads/main", true},
		{"repo:acme/*:ref:refs/heads/main", "repo:acme/app/x:ref:refs/heads/main", false},
		{"repo:acme/**:ref:refs/heads/main", "repo:acme/app/x:ref:refs/heads/main", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"a**b", "a/x/b", true},
		{"a***b", "a/x/b", true},
		{"*.example.test", "build.example.test", true},
		{"*.example.test", "a/b.example.test", false},
		{"**/main", "refs/heads/main", true},
		{"**/main", "main", false},
		{"Refs/*", "refs/x", false},
		{"é*", "éa", true},
		{"a.c", "abc", false},
		{"a?c", "a?c", true},
		{"a?c", "abc", false},
		{"[ab]", "a", false},
		{"", "", true},
		{"", "x", false},
	} {
		if got := Glob(tc.pattern, tc.value); got != tc.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
	if Glob("\xff*", "x") {
		t.Error("an invalid UTF-8 pattern must never match")
	}
}

func TestGlobIsLinear(t *testing.T) {
	// A backtracking matcher takes exponential time on this input.
	pattern := strings.Repeat("*a", 40) + "b"
	if Glob(pattern, strings.Repeat("a", 5000)) {
		t.Fatal("unexpected match")
	}
}

// reference translates a glob into an anchored regular expression, an independent oracle.
func reference(pattern string) *regexp.Regexp {
	var out strings.Builder
	out.WriteString("(?s)^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); {
		if runes[i] != '*' {
			out.WriteString(regexp.QuoteMeta(string(runes[i])))
			i++
			continue
		}
		run := 0
		for i < len(runes) && runes[i] == '*' {
			run++
			i++
		}
		if run == 1 {
			out.WriteString("[^/]*")
		} else {
			out.WriteString(".*")
		}
	}
	out.WriteString("$")
	return regexp.MustCompile(out.String())
}

func FuzzGlob(f *testing.F) {
	for _, seed := range [][2]string{{"refs/heads/*", "refs/heads/main"}, {"a**b", "a/b"}, {"*", "x/y"}, {"", ""}, {"***", "///"}, {"a*b*c", "abc"}, {"é*", "éé/"}, {"*/*", "a/b/c"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, value string) {
		if !utf8.ValidString(pattern) || !utf8.ValidString(value) || len(pattern) > 64 || len(value) > 256 {
			t.Skip()
		}
		if got, want := Glob(pattern, value), reference(pattern).MatchString(value); got != want {
			t.Fatalf("Glob(%q, %q) = %v, regexp oracle says %v", pattern, value, got, want)
		}
	})
}
