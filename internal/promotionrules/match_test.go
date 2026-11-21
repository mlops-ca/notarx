package promotionrules

import (
	"errors"
	"strings"
	"testing"
)

func TestGlobTable(t *testing.T) {
	for _, tc := range []struct {
		glob, value string
		want        bool
	}{
		{"releases/*", "releases/app.tgz", true},
		{"releases/*", "releases/sub/app.tgz", false}, // * does not cross /
		{"releases/**", "releases/sub/app.tgz", true}, // ** does
		{"releases/**", "releases/app.tgz", true},
		{"**", "a/b/c", true},
		{"*", "a/b", false},
		{"*", "ab", true},
		{"*.jar", "app.jar", true},
		{"*.jar", "dir/app.jar", false},
		{"**.jar", "dir/app.jar", true},
		{"org.acme:*", "org.acme:lib", true},
		{"org.acme:*", "org.acme:lib:1.0", true},
		{"lib?", "lib?", true}, // ? is a literal
		{"lib?", "liba", false},
		{"a.b", "axb", false}, // metacharacters are literals
		{"a+b(c)", "a+b(c)", true},
		{"[ab]", "a", false},
		{"[ab]", "[ab]", true},
		{"app", "app-1", false}, // anchored
		{"pp", "app", false},
		{"v*", "v1.2", true},
		{"v*", "x1.2", false},
		{"é*", "éa", true},
		{"a***b", "a/x/b", true}, // runs of * collapse into **
	} {
		compiled, err := CompileGlob(tc.glob)
		if err != nil {
			t.Fatalf("CompileGlob(%q) = %v", tc.glob, err)
		}
		if got := compiled.MatchString(tc.value); got != tc.want {
			t.Errorf("glob %q on %q = %v, want %v", tc.glob, tc.value, got, tc.want)
		}
	}
}

func TestRegexTable(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{"releases/.*", "releases/a/b", true},
		{"releases/[^/]+", "releases/a/b", false},
		{"app-[0-9]+", "app-12", true},
		{"app-[0-9]+", "xapp-12", false}, // anchored at the start
		{"app", "app-1", false},          // and at the end
		{"a|b", "ab", false},             // the alternation is inside the anchors
		{"a|b", "b", true},
		{"(?i)APP", "app", true},
	} {
		compiled, err := CompileRegex(tc.pattern)
		if err != nil {
			t.Fatalf("CompileRegex(%q) = %v", tc.pattern, err)
		}
		if got := compiled.MatchString(tc.value); got != tc.want {
			t.Errorf("regex %q on %q = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestRegexIsRE2OnlyAndBounded(t *testing.T) {
	for name, pattern := range map[string]string{
		"lookahead":      "(?=a)a",
		"negative":       "(?!a)b",
		"lookbehind":     "(?<=a)b",
		"backreference":  "(a)\\1",
		"possessive":     "a*+",
		"unclosed group": "(a",
		"bad class":      "[a-",
		"empty":          "",
		"nul":            "a\x00b",
	} {
		if _, err := CompileRegex(pattern); err == nil {
			t.Errorf("%s: %q was accepted", name, pattern)
		}
	}
	atLimit := strings.Repeat("a", MaxPatternBytes)
	if _, err := CompileRegex(atLimit); err != nil {
		t.Fatalf("a %d byte pattern must compile: %v", MaxPatternBytes, err)
	}
	if _, err := CompileRegex(atLimit + "a"); err == nil {
		t.Fatalf("a %d byte pattern must be refused", MaxPatternBytes+1)
	}
	if _, err := CompileGlob(strings.Repeat("a", MaxPatternBytes+1)); err == nil {
		t.Fatal("an over-long glob must be refused")
	}
	if _, err := CompileGlob(strings.Repeat("a", MaxPatternBytes)); err != nil {
		t.Fatalf("a %d byte glob must compile: %v", MaxPatternBytes, err)
	}
	// The multi-byte limit counts bytes, not characters.
	if _, err := CompileRegex(strings.Repeat("é", MaxPatternBytes/2+1)); err == nil {
		t.Fatal("the limit is in bytes")
	}
	// A pathological pattern still runs in linear time (RE2 has no backtracking).
	evil, err := CompileRegex("(a+)+$")
	if err != nil {
		t.Fatal(err)
	}
	if evil.MatchString(strings.Repeat("a", 50000) + "!") {
		t.Fatal("unexpected match")
	}
	if _, err = CompileRegex(`\xff`); err != nil {
		t.Fatalf("an escape is not invalid UTF-8 text: %v", err)
	}
	if _, err = CompileRegex(string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("invalid UTF-8 must be refused")
	}
}

func TestCoordinateRequiresExactlyOneOfGlobAndRegex(t *testing.T) {
	for name, match := range map[string]Match{
		"neither": {},
		"both":    {Coordinate: Coordinate{Glob: "a", Regex: "a"}},
	} {
		if _, err := match.Compile(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (Match{Coordinate: Coordinate{Glob: "a/*"}}).Compile(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRangeMatchesSemverOnly(t *testing.T) {
	compiled, err := (Match{Coordinate: Coordinate{Glob: "**"}, Version: &VersionRange{Range: ">=1.2.0 <2.0.0"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"1.2.0", true}, {"1.9.9", true}, {"2.0.0", false}, {"1.1.9", false},
		{"1.2", false},          // not a semantic version
		{"latest", false},       // not a semantic version
		{"v1.2.3", false},       // a leading v is not semver
		{"1.2.3.4", false},      // four components
		{"1.2.3+build.5", true}, // build metadata is ignored
		{"", false},
		{"2025-11-01", false},
		{"nightly-1.5.0", false},
	} {
		if got := compiled.MatchesVersion(tc.version); got != tc.want {
			t.Errorf("version %q = %v, want %v", tc.version, got, tc.want)
		}
	}
	none, err := (Match{Coordinate: Coordinate{Glob: "**"}}).Compile()
	if err != nil || !none.MatchesVersion("latest") || !none.MatchesVersion("") {
		t.Fatalf("a match without a range accepts every version: %v", err)
	}
	for _, bad := range []string{"", "not a range", ">=", strings.Repeat("1", MaxRangeBytes+1)} {
		if _, err = (Match{Coordinate: Coordinate{Glob: "a"}, Version: &VersionRange{Range: bad}}).Compile(); err == nil {
			t.Errorf("range %q accepted", bad)
		}
	}
}

func TestPropertiesMatch(t *testing.T) {
	compiled, err := (Match{Coordinate: Coordinate{Glob: "**"}, Properties: map[string]string{"channel": "stable", "team": "platform-*"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		properties map[string]string
		want       bool
	}{
		{"all match", map[string]string{"channel": "stable", "team": "platform-core"}, true},
		{"extra keys are fine", map[string]string{"channel": "stable", "team": "platform-x", "other": "y"}, true},
		{"value differs", map[string]string{"channel": "beta", "team": "platform-core"}, false},
		{"glob differs", map[string]string{"channel": "stable", "team": "infra"}, false},
		{"key missing", map[string]string{"channel": "stable"}, false},
		{"none", nil, false},
	} {
		if got := compiled.MatchesProperties(tc.properties); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
	for name, properties := range map[string]map[string]string{
		"upper case key": {"Channel": "x"},
		"empty key":      {"": "x"},
		"empty glob":     {"channel": ""},
		"long glob":      {"channel": strings.Repeat("a", MaxPatternBytes+1)},
	} {
		if _, err = (Match{Coordinate: Coordinate{Glob: "a"}, Properties: properties}).Compile(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i < 33; i++ {
		tooMany["k"+strings.Repeat("a", i)] = "v"
	}
	if _, err = (Match{Coordinate: Coordinate{Glob: "a"}, Properties: tooMany}).Compile(); err == nil {
		t.Fatal("more than 32 properties accepted")
	}
}

func TestRefsMatch(t *testing.T) {
	compiled, err := (Match{Coordinate: Coordinate{Glob: "**"}, Refs: []string{"latest", "v*"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		refs []string
		want bool
	}{
		{[]string{"latest"}, true}, {[]string{"beta", "v1.0"}, true}, {[]string{"beta"}, false}, {nil, false},
	} {
		if got := compiled.MatchesRefs(tc.refs); got != tc.want {
			t.Errorf("refs %v = %v, want %v", tc.refs, got, tc.want)
		}
	}
	if !(&Compiled{}).MatchesRefs(nil) {
		t.Fatal("a match without refs accepts every artifact")
	}
	if _, err = (Match{Coordinate: Coordinate{Glob: "a"}, Refs: make([]string, MaxRefs+1)}).Compile(); err == nil {
		t.Fatal("too many refs accepted")
	}
}

func TestMatchesAppliesPatternsOnlyToCanonicalCoordinates(t *testing.T) {
	compiled, err := (Match{Coordinate: Coordinate{Glob: "**"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(coordinate string) bool { return !strings.Contains(coordinate, "..") }
	if !compiled.Matches(Candidate{Coordinate: "a/b"}, canonical) {
		t.Fatal("a canonical coordinate must match")
	}
	if compiled.Matches(Candidate{Coordinate: "a/../b"}, canonical) {
		t.Fatal("a coordinate the format refuses must never match")
	}
	if !compiled.Matches(Candidate{Coordinate: "a/../b"}, nil) {
		t.Fatal("a format without Governance applies the pattern as is")
	}
}

func TestValidateAgainstTheSource(t *testing.T) {
	base := Match{Coordinate: Coordinate{Glob: "**"}}
	withProperties := base
	withProperties.Properties = map[string]string{"channel": "stable"}
	withRefs := base
	withRefs.Refs = []string{"latest"}
	release := base
	release.Scope = ScopeRelease
	bad := base
	bad.Scope = "everything"
	for _, tc := range []struct {
		name   string
		match  Match
		source Source
		code   string
	}{
		{"plain match", base, Source{}, ""},
		{"properties on a versioned generic repository", withProperties, Source{VersionedGeneric: true}, ""},
		{"properties elsewhere", withProperties, Source{}, CodeUnsupportedMatch},
		{"properties on a tagged format", withProperties, Source{Tagged: true, ReleaseSet: true}, CodeUnsupportedMatch},
		{"refs on a tagged format", withRefs, Source{Tagged: true}, ""},
		{"refs without tags", withRefs, Source{}, CodeUnsupportedMatch},
		{"release with ReleaseSet", release, Source{ReleaseSet: true}, ""},
		{"release without ReleaseSet", release, Source{}, CodeUnsupportedFormat},
		{"release on a versioned repository without ReleaseSet", release, Source{VersionedGeneric: true}, CodeUnsupportedFormat},
		{"unknown scope", bad, Source{ReleaseSet: true}, CodeInvalid},
		{"invalid pattern", Match{Coordinate: Coordinate{Regex: "(?=x)"}}, Source{}, CodeInvalid},
	} {
		err := tc.match.Validate(tc.source)
		if tc.code == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Code != tc.code {
			t.Errorf("%s: err = %v, want code %s", tc.name, err, tc.code)
		}
	}
	if (Match{}).EffectiveScope() != ScopeCoordinate {
		t.Fatal("the default scope is coordinate")
	}
}

func TestParseMatchRefusesUnknownMembersAndRoundTrips(t *testing.T) {
	if _, err := ParseMatch([]byte(`{"coordinate":{"glob":"a"},"surprise":1}`)); err == nil {
		t.Fatal("unknown member accepted")
	}
	if _, err := ParseMatch([]byte(`{"coordinate":{"glob":"a"}} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	original := Match{Coordinate: Coordinate{Regex: "a.*"}, Version: &VersionRange{Range: "^1.0.0"}, Refs: []string{"v*"}, Properties: map[string]string{"k": "v"}, Scope: ScopeRelease}
	raw, err := original.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMatch(raw)
	if err != nil || parsed.Coordinate != original.Coordinate || parsed.Version.Range != "^1.0.0" || parsed.Scope != ScopeRelease || parsed.Properties["k"] != "v" || len(parsed.Refs) != 1 {
		t.Fatalf("round trip = %#v, %v", parsed, err)
	}
}

func TestCompileRegexCannotEscapeItsAnchors(t *testing.T) {
	for _, pattern := range []string{"a)|(b", "a)|b|(c", ")(", "a(?:", "(?i", "[a"} {
		if _, err := CompileRegex(pattern); err == nil {
			t.Errorf("pattern %q accepted", pattern)
		}
	}
	compiled, err := CompileRegex("a|b")
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]bool{"a": true, "b": true, "axxxx": false, "xxxb": false} {
		if got := compiled.MatchString(value); got != want {
			t.Errorf("a|b on %q = %v, want %v", value, got, want)
		}
	}
}

func TestModuleVersionMatchesGoSyntax(t *testing.T) {
	ranged, err := (Match{Coordinate: Coordinate{Glob: "**"}, Version: &VersionRange{Range: ">=1.2.0 <2.0.0"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]bool{
		"v1.2.3": true, "v1.9.9": true, "v2.0.0": false, "v1.1.0": false, "v1.2.3-rc.1": false,
		"1.2.3": false, "vv1.2.3": false, "v": false, "v1.2": false, "latest": false, "": false,
		"v1.5.0+incompatible": true,
	} {
		if got := ranged.MatchesModuleVersion(version); got != want {
			t.Errorf("module version %q = %v, want %v", version, got, want)
		}
	}
	candidate := Candidate{Coordinate: "example.com/m@v1.2.3", Version: "v1.2.3", ModuleVersion: true}
	if !ranged.Matches(candidate, nil) {
		t.Error("a Go candidate in range was refused")
	}
	candidate.ModuleVersion = false
	if ranged.Matches(candidate, nil) {
		t.Error("a non-Go v-prefixed version matched a range")
	}
	none, _ := (Match{Coordinate: Coordinate{Glob: "**"}}).Compile()
	if !none.MatchesModuleVersion("v1.2.3") || !none.MatchesModuleVersion("") {
		t.Error("a match without a range accepts every version")
	}
}
