package promotionfanout

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

func rule(t *testing.T, id string, match promotionrules.Match) repository.PromotionRule {
	t.Helper()
	raw, err := match.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return repository.PromotionRule{ID: id, Match: raw, Enabled: true, State: repository.PromotionRuleActive}
}

func glob(pattern string) promotionrules.Match {
	return promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: pattern}}
}

func TestCandidateOfDerivesTheVersionTheWayAPromotionAddressesIt(t *testing.T) {
	for _, tc := range []struct {
		format     repository.Format
		coordinate string
		version    string
		module     bool
	}{
		{repository.FormatNPM, "@scope/pkg@1.2.3", "1.2.3", false},
		{repository.FormatPyPI, "proj@2.0", "2.0", false},
		{repository.FormatCargo, "crate@0.4.0", "0.4.0", false},
		{repository.FormatGo, "example.com/mod@v1.2.3", "v1.2.3", true},
		{repository.FormatConan, "zlib/1.3/user/stable#abc", "1.3", false},
		{repository.FormatMaven, "org.example:widget:1.0.0", "1.0.0", false},
		{repository.FormatMaven, "org.example:widget:1.0.0:sources:jar", "1.0.0", false},
		{repository.FormatMaven, "org.example:widget", "", false},
		{repository.FormatRaw, "tool@1.0.0", "", false},
		{repository.FormatRaw, "releases/app@2x.png", "", false},
		{repository.FormatRaw, "docs/readme.txt", "", false},
		{repository.FormatOCI, "team/widget", "", false},
		{repository.FormatAPT, "pool/main/p/pkg/pkg_1.0_amd64.deb", "", false},
		{repository.FormatConan, "malformed#rev", "", false},
	} {
		got := CandidateOf(tc.format, tc.coordinate)
		if got.Coordinate != tc.coordinate || got.Version != tc.version || got.ModuleVersion != tc.module {
			t.Errorf("%s %q = %+v", tc.format, tc.coordinate, got)
		}
	}
}

func TestCandidatesOfOffersTheVersionedReadingOfARawPath(t *testing.T) {
	got := CandidatesOf(repository.FormatRaw, "tool/1.0.0/bin/tool")
	if len(got) != 2 || got[0].Coordinate != "tool/1.0.0/bin/tool" || got[1].Coordinate != "tool@1.0.0" || got[1].Version != "1.0.0" {
		t.Fatalf("candidates = %+v", got)
	}
	if got = CandidatesOf(repository.FormatRaw, "flat.txt"); len(got) != 1 {
		t.Fatalf("a flat path has one reading: %+v", got)
	}
	if got = CandidatesOf(repository.FormatOCI, "team/widget"); len(got) != 1 {
		t.Fatalf("only Raw has two readings: %+v", got)
	}
}

func TestMatcherLeavesOutOnlyWhatCannotMatch(t *testing.T) {
	rules := []repository.PromotionRule{
		rule(t, "any", glob("**")),
		rule(t, "docs", glob("docs/**")),
		rule(t, "range", promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Version: &promotionrules.VersionRange{Range: ">=2.0.0"}}),
		// A rule whose stored match cannot be read is offered to the evaluator, which fails its evaluation with a reason.
		{ID: "broken", Match: json.RawMessage("{"), Enabled: true},
		// refs and properties are the evaluator's: the fan-out never leaves such a rule out.
		rule(t, "refs", promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "app@*"}, Refs: []string{"latest"}}),
	}
	m := Matcher{}
	event := events.Published("repo", "npm", "app@1.0.0", "sha256:aa", "")
	got := m.Match(event, rules)
	if want := []string{"any", "broken", "refs"}; !slices.Equal(got, want) {
		t.Fatalf("1.0.0 matched %v, want %v", got, want)
	}
	event = events.Published("repo", "npm", "app@2.1.0", "sha256:aa", "")
	if got = m.Match(event, rules); !slices.Equal(got, []string{"any", "range", "broken", "refs"}) {
		t.Fatalf("2.1.0 matched %v", got)
	}
	// A coordinate the format refuses is never matched.
	m.Validate = func(repository.Format) func(string) bool { return func(c string) bool { return c != "app@2.1.0" } }
	if got = m.Match(event, rules); !slices.Equal(got, []string{"broken"}) {
		t.Fatalf("invalid coordinate matched %v", got)
	}
}

func TestMatcherReadsAGoModuleVersionWithItsLeadingV(t *testing.T) {
	rules := []repository.PromotionRule{rule(t, "range", promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Version: &promotionrules.VersionRange{Range: ">=1.2.0"}})}
	if got := (Matcher{}).Match(events.Published("repo", "go", "example.com/mod@v1.3.0", "sha256:aa", ""), rules); !slices.Equal(got, []string{"range"}) {
		t.Fatalf("matched %v", got)
	}
}

type countingSource struct {
	rules []repository.PromotionRule
	calls int
	err   error
}

func (s *countingSource) list(context.Context, repository.RuleQuerier, string) ([]repository.PromotionRule, error) {
	s.calls++
	return s.rules, s.err
}

func TestIndexCachesTheRuleSetAndTheScopeFlushIsTheVersionBump(t *testing.T) {
	registry := readcache.NewRegistry(time.Minute)
	source := &countingSource{rules: []repository.PromotionRule{rule(t, "any", glob("**"))}}
	index := New(source.list, registry, nil)
	event := events.Published("repo", "raw", "a.txt", "sha256:aa", "")
	for range 3 {
		got, err := index.MatchingRules(context.Background(), nil, event)
		if err != nil || !slices.Equal(got, []string{"any"}) {
			t.Fatalf("matched %v, %v", got, err)
		}
	}
	if source.calls != 1 {
		t.Fatalf("the rule set was read %d times, want one cached read", source.calls)
	}
	// A rule change announces the scope; the next publication reads the new set.
	source.rules = []repository.PromotionRule{rule(t, "docs", glob("docs/**"))}
	registry.HandleNotification(readcache.Payload(repository.CacheScopePromotionRule, ""))
	got, err := index.MatchingRules(context.Background(), nil, event)
	if err != nil || len(got) != 0 || source.calls != 2 {
		t.Fatalf("after the flush: matched %v, %v, calls %d", got, err, source.calls)
	}
	// A publication over HTTP carries the bypass mark of management reads; the rule set is served from the cache all the same.
	if _, err = index.MatchingRules(readcache.Bypass(context.Background()), nil, event); err != nil || source.calls != 2 {
		t.Fatalf("bypass: %v, calls %d", err, source.calls)
	}
}

func TestIndexWithTheCacheOffReadsEveryTimeAndReportsTheSourceError(t *testing.T) {
	source := &countingSource{rules: []repository.PromotionRule{rule(t, "any", glob("**"))}}
	index := New(source.list, readcache.NewRegistry(0), nil)
	event := events.Published("repo", "raw", "a.txt", "sha256:aa", "")
	for range 2 {
		if _, err := index.MatchingRules(context.Background(), nil, event); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls != 2 {
		t.Fatalf("calls = %d", source.calls)
	}
	source.err = errors.New("database down")
	if _, err := index.MatchingRules(context.Background(), nil, event); err == nil {
		t.Fatal("a failed rule read must fail the fan-out, and with it the publication")
	}
}
