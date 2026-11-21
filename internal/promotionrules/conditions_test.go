package promotionrules

import (
	"errors"
	"testing"
	"time"
)

func TestConditionsValidate(t *testing.T) {
	for name, conditions := range map[string]Conditions{
		"empty":                 {},
		"security policy":       {SecurityPolicy: "pass"},
		"scan completed":        {Scan: &Scan{Completed: true}},
		"scan with severity":    {Scan: &Scan{Completed: true, MaxSeverity: "medium"}},
		"min age":               {MinAge: "72h"},
		"min age at the limit":  {MinAge: MaxMinAge.String()},
		"evidence requirements": {RequireSignature: true, RequireSBOM: true, RequireProvenance: true},
		"everything":            {SecurityPolicy: "pass", Scan: &Scan{Completed: true, MaxSeverity: "none"}, MinAge: "1h"},
	} {
		if err := conditions.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, conditions := range map[string]Conditions{
		"policy value":        {SecurityPolicy: "warn"},
		"severity":            {Scan: &Scan{Completed: true, MaxSeverity: "severe"}},
		"severity w/o scan":   {Scan: &Scan{MaxSeverity: "low"}},
		"unparsable age":      {MinAge: "three days"},
		"negative age":        {MinAge: "-1h"},
		"zero age":            {MinAge: "0s"},
		"age above the bound": {MinAge: (MaxMinAge + time.Hour).String()},
	} {
		err := conditions.Validate()
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Code != CodeInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParseConditions(t *testing.T) {
	if got, err := ParseConditions(nil); err != nil || got != (Conditions{}) {
		t.Fatalf("empty = %#v, %v", got, err)
	}
	if _, err := ParseConditions([]byte(`{"minAge":"1h","unknown":true}`)); err == nil {
		t.Fatal("unknown member accepted")
	}
	got, err := ParseConditions([]byte(`{"securityPolicy":"pass","scan":{"completed":true,"maxSeverity":"high"},"minAge":"72h","requireSbom":true}`))
	if err != nil || got.SecurityPolicy != "pass" || got.Scan == nil || got.Scan.MaxSeverity != "high" || got.MinAge != "72h" || !got.RequireSBOM {
		t.Fatalf("parsed = %#v, %v", got, err)
	}
	if age, ageErr := got.MinAgeDuration(); ageErr != nil || age != 72*time.Hour {
		t.Fatalf("age = %v, %v", age, ageErr)
	}
	raw, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if again, parseErr := ParseConditions(raw); parseErr != nil || again.Scan.MaxSeverity != "high" {
		t.Fatalf("round trip = %#v, %v", again, parseErr)
	}
}

func TestSeverityExceeds(t *testing.T) {
	for _, tc := range []struct {
		limit  string
		counts map[string]int
		want   bool
	}{
		{"medium", map[string]int{"low": 3, "medium": 1}, false},
		{"medium", map[string]int{"high": 1}, true},
		{"medium", map[string]int{"critical": 1}, true},
		{"none", map[string]int{"low": 1}, true},
		{"none", map[string]int{}, false},
		{"critical", map[string]int{"critical": 9}, false},
		{"high", map[string]int{"high": 0, "critical": 0}, false},
		{"", map[string]int{"critical": 1}, false},
	} {
		if got := SeverityExceeds(tc.limit, tc.counts); got != tc.want {
			t.Errorf("limit %q counts %v = %v, want %v", tc.limit, tc.counts, got, tc.want)
		}
	}
}

func TestOutcome(t *testing.T) {
	pass := ConditionVerdict{Verdict: VerdictPass}
	fail := ConditionVerdict{Verdict: VerdictFail}
	skipped := ConditionVerdict{Verdict: VerdictNotEvaluated}
	for name, tc := range map[string]struct {
		verdicts []ConditionVerdict
		want     string
	}{
		"none":                {nil, OutcomeEligible},
		"all pass":            {[]ConditionVerdict{pass, pass}, OutcomeEligible},
		"not evaluated":       {[]ConditionVerdict{pass, skipped}, OutcomeUndetermined},
		"fail":                {[]ConditionVerdict{pass, fail}, OutcomeBlocked},
		"fail beats the rest": {[]ConditionVerdict{skipped, fail}, OutcomeBlocked},
	} {
		if got := Outcome(tc.verdicts); got != tc.want {
			t.Errorf("%s = %s, want %s", name, got, tc.want)
		}
	}
}
