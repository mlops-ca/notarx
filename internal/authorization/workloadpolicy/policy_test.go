package workloadpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decodeClaims(t testing.TB, body string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// A token body as issued by a Kubernetes API server for a projected service account token.
const kubernetesToken = `{
  "aud": ["https://notarx.example.test"],
  "exp": 1800000600, "iat": 1800000000, "nbf": 1800000000,
  "iss": "https://kubernetes.default.svc.cluster.local",
  "jti": "9d4d6d2e-34a3-4a8e-a3d1-6c0f9e0b8f55",
  "kubernetes.io": {
    "namespace": "team-a",
    "node": {"name": "ip-10-0-1-5", "uid": "5e1f8a9e-3d4c-4b8b-a1a0-0f5ad1a1c001"},
    "pod": {"name": "builder-7d9f6c-x2", "uid": "a7f0b2c4-1d3e-4a5b-9c6d-7e8f9a0b1c2d"},
    "serviceaccount": {"name": "builder", "uid": "0b1c2d3e-4f50-4a6b-8c7d-9e0f1a2b3c4d"}
  },
  "sub": "system:serviceaccount:team-a:builder"
}`

const githubToken = `{
  "iss": "https://token.actions.example.test",
  "sub": "repo:acme/app:ref:refs/heads/main",
  "aud": "https://notarx.example.test",
  "repository": "acme/app", "repository_id": "1296269", "repository_owner": "acme", "repository_owner_id": "9919",
  "ref": "refs/heads/main", "ref_type": "branch", "environment": "packages-publish", "event_name": "push",
  "job_workflow_ref": "acme/app/.github/workflows/release.yml@refs/heads/main", "run_attempt": 1, "fork": false
}`

const gitlabToken = `{
  "iss": "https://gitlab.example.test", "sub": "project_path:acme/app:ref_type:branch:ref:main",
  "aud": "https://notarx.example.test", "project_id": "42", "namespace_id": "7", "project_path": "acme/app",
  "ref": "main", "ref_type": "branch", "ref_protected": "true"
}`

func conditions(t testing.TB, kind Kind, raw string) []Condition {
	t.Helper()
	parsed, err := ParseConditions(kind, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestValidateAcceptsMandatoryConditions(t *testing.T) {
	for name, tc := range map[string]struct {
		kind    Kind
		raw     string
		options ValidateOptions
	}{
		"github repository id and ref": {KindGitHubActions, `[{"claim":"repository_id","op":"eq","values":["1296269"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`, ValidateOptions{}},
		"github id and environment":    {KindGitHubActions, `[{"claim":"repository_id","op":"in","values":["1","2"]},{"claim":"environment","op":"eq","values":["packages-publish"]}]`, ValidateOptions{}},
		"github id and workflow ref":   {KindGitHubActions, `[{"claim":"repository_id","op":"eq","values":["1"]},{"claim":"job_workflow_ref","op":"glob","values":["acme/app/.github/workflows/release.yml@refs/heads/*"]}]`, ValidateOptions{}},
		"github owner id and repo":     {KindGitHubActions, `[{"claim":"repository_owner_id","op":"eq","values":["9919"]},{"claim":"repository","op":"eq","values":["acme/app"]},{"claim":"ref","op":"prefix","values":["refs/tags/v"]}]`, ValidateOptions{}},
		"github any ref acknowledged":  {KindGitHubActions, `[{"claim":"repository_id","op":"eq","values":["1"]}]`, ValidateOptions{AllowAnyRef: true}},
		"gitlab protected":             {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]},{"claim":"ref_protected","op":"eq","values":["true"]}]`, ValidateOptions{}},
		"gitlab ref":                   {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]},{"claim":"ref","op":"eq","values":["main"]}]`, ValidateOptions{}},
		"gitlab namespace and path":    {KindGitLabCI, `[{"claim":"namespace_id","op":"eq","values":["7"]},{"claim":"project_path","op":"eq","values":["acme/app"]},{"claim":"ref_protected","op":"eq","values":["true"]}]`, ValidateOptions{}},
		"gitlab any ref acknowledged":  {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]}]`, ValidateOptions{AllowAnyRef: true}},
		"kubernetes subject":           {KindKubernetes, `[{"claim":"sub","op":"eq","values":["system:serviceaccount:team-a:builder"]},{"claim":"kubernetes.io.namespace","op":"eq","values":["team-a"]}]`, ValidateOptions{}},
		"generic sub eq":               {KindGeneric, `[{"claim":"sub","op":"eq","values":["ci-runner"]}]`, ValidateOptions{}},
		"generic sub prefix":           {KindGeneric, `[{"claim":"sub","op":"prefix","values":["ci:"]}]`, ValidateOptions{}},
		"generic sub glob":             {KindGeneric, `[{"claim":"sub","op":"glob","values":["ci:*:main"]}]`, ValidateOptions{}},
	} {
		t.Run(name, func(t *testing.T) {
			canonical, parsed, err := Validate(tc.kind, []byte(tc.raw), tc.options)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			again, _, err := Validate(tc.kind, canonical, tc.options)
			if err != nil || !bytes.Equal(again, canonical) {
				t.Fatalf("canonical form is not stable: %s vs %s (%v)", canonical, again, err)
			}
			if len(parsed) == 0 {
				t.Fatal("no conditions returned")
			}
		})
	}
}

func TestValidateRejectsWeakPolicies(t *testing.T) {
	for name, tc := range map[string]struct {
		kind Kind
		raw  string
	}{
		"github owner id only":           {KindGitHubActions, `[{"claim":"repository_owner_id","op":"eq","values":["9919"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github owner id and name only":  {KindGitHubActions, `[{"claim":"repository_owner","op":"eq","values":["acme"]},{"claim":"repository","op":"eq","values":["acme/app"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github rename squat repository": {KindGitHubActions, `[{"claim":"repository","op":"eq","values":["acme/app"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github repo name only no id":    {KindGitHubActions, `[{"claim":"repository_owner","op":"eq","values":["acme"]},{"claim":"repository","op":"eq","values":["acme/app"]}]`},
		"github owner id glob repo":      {KindGitHubActions, `[{"claim":"repository_owner_id","op":"eq","values":["9919"]},{"claim":"repository","op":"glob","values":["acme/*"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github id glob":                 {KindGitHubActions, `[{"claim":"repository_id","op":"glob","values":["12*"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github id prefix":               {KindGitHubActions, `[{"claim":"repository_id","op":"prefix","values":["12"]},{"claim":"ref","op":"eq","values":["refs/heads/main"]}]`},
		"github no ref restriction":      {KindGitHubActions, `[{"claim":"repository_id","op":"eq","values":["1296269"]},{"claim":"event_name","op":"eq","values":["push"]}]`},
		"github nested claim not top":    {KindGitHubActions, `[{"claim":["x","repository_id"],"op":"eq","values":["1"]},{"claim":"ref","op":"eq","values":["r"]}]`},
		"gitlab namespace id only":       {KindGitLabCI, `[{"claim":"namespace_id","op":"eq","values":["7"]},{"claim":"ref_protected","op":"eq","values":["true"]}]`},
		"gitlab no ref restriction":      {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]}]`},
		"gitlab ref_protected false":     {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]},{"claim":"ref_protected","op":"eq","values":["false"]}]`},
		"gitlab ref_protected in":        {KindGitLabCI, `[{"claim":"project_id","op":"eq","values":["42"]},{"claim":"ref_protected","op":"in","values":["true","false"]}]`},
		"gitlab project path glob":       {KindGitLabCI, `[{"claim":"namespace_id","op":"eq","values":["7"]},{"claim":"project_path","op":"glob","values":["acme/*"]},{"claim":"ref_protected","op":"eq","values":["true"]}]`},
		"kubernetes no sub":              {KindKubernetes, `[{"claim":"kubernetes.io.namespace","op":"eq","values":["team-a"]}]`},
		"kubernetes sub glob":            {KindKubernetes, `[{"claim":"sub","op":"glob","values":["system:serviceaccount:team-a:*"]}]`},
		"kubernetes sub prefix":          {KindKubernetes, `[{"claim":"sub","op":"prefix","values":["system:serviceaccount:team-a:"]}]`},
		"kubernetes sub in":              {KindKubernetes, `[{"claim":"sub","op":"in","values":["system:serviceaccount:team-a:a","system:serviceaccount:team-a:b"]}]`},
		"kubernetes bad subject":         {KindKubernetes, `[{"claim":"sub","op":"eq","values":["system:node:worker-1"]}]`},
		"kubernetes subject with colon":  {KindKubernetes, `[{"claim":"sub","op":"eq","values":["system:serviceaccount:a:b:c"]}]`},
		"generic no sub":                 {KindGeneric, `[{"claim":"email","op":"eq","values":["ci@example.test"]}]`},
		"generic bare star":              {KindGeneric, `[{"claim":"sub","op":"glob","values":["*"]}]`},
		"generic bare double star":       {KindGeneric, `[{"claim":"sub","op":"glob","values":["**"]}]`},
		"generic bare stars any of":      {KindGeneric, `[{"claim":"sub","op":"glob","values":["ci:*","***"]}]`},
		"unknown kind":                   {"ldap", `[{"claim":"sub","op":"eq","values":["x"]}]`},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Validate(tc.kind, []byte(tc.raw), ValidateOptions{})
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestParseConditionsShapeErrors(t *testing.T) {
	many := strings.Repeat(`{"claim":"sub","op":"eq","values":["a"]},`, MaxConditions) + `{"claim":"sub","op":"eq","values":["a"]}`
	longValue := strings.Repeat("a", MaxValueBytes+1)
	nine := `"1","2","3","4","5","6","7","8","9"`
	for name, raw := range map[string]string{
		"not json":            `nope`,
		"object":              `{}`,
		"empty list":          `[]`,
		"trailing data":       `[{"claim":"sub","op":"eq","values":["a"]}] x`,
		"unknown field":       `[{"claim":"sub","op":"eq","values":["a"],"extra":1}]`,
		"17 conditions":       "[" + many + "]",
		"unknown op":          `[{"claim":"sub","op":"regex","values":["a"]}]`,
		"missing op":          `[{"claim":"sub","values":["a"]}]`,
		"eq two values":       `[{"claim":"sub","op":"eq","values":["a","b"]}]`,
		"no values":           `[{"claim":"sub","op":"in","values":[]}]`,
		"9 values":            `[{"claim":"sub","op":"in","values":[` + nine + `]}]`,
		"empty value":         `[{"claim":"sub","op":"eq","values":[""]}]`,
		"long value":          `[{"claim":"sub","op":"eq","values":["` + longValue + `"]}]`,
		"non string value":    `[{"claim":"sub","op":"eq","values":[5]}]`,
		"claim number":        `[{"claim":5,"op":"eq","values":["a"]}]`,
		"claim empty":         `[{"claim":"","op":"eq","values":["a"]}]`,
		"claim empty segment": `[{"claim":["a",""],"op":"eq","values":["a"]}]`,
		"claim too deep":      `[{"claim":["a","b","c","d","e","f","g","h","i"],"op":"eq","values":["a"]}]`,
		"claim missing":       `[{"op":"eq","values":["a"]}]`,
		"bad pointer escape":  `[{"claim":"/a~2b","op":"eq","values":["a"]}]`,
		"dangling tilde":      `[{"claim":"/a~","op":"eq","values":["a"]}]`,
		"empty pointer key":   `[{"claim":"/a//b","op":"eq","values":["a"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConditions(KindGeneric, []byte(raw)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	big := bytes.Repeat([]byte(" "), maxPolicyBytes+1)
	if _, err := ParseConditions(KindGeneric, big); err == nil {
		t.Fatal("oversized policy accepted")
	}
	if _, err := ParseConditions(KindGeneric, []byte(`[{"claim":"sub","op":"in","values":[`+strings.TrimSuffix(nine, ",\"9\"")+`]}]`)); err != nil {
		t.Fatalf("8 values rejected: %v", err)
	}
}

func TestClaimPathSpellings(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		raw  string
		want string
	}{
		{KindKubernetes, `["kubernetes.io","namespace"]`, `["kubernetes.io","namespace"]`},
		{KindKubernetes, `"/kubernetes.io/namespace"`, `["kubernetes.io","namespace"]`},
		{KindKubernetes, `"kubernetes.io.namespace"`, `["kubernetes.io","namespace"]`},
		{KindKubernetes, `"kubernetes.io.serviceaccount.name"`, `["kubernetes.io","serviceaccount","name"]`},
		{KindKubernetes, `"kubernetes.io.pod.uid"`, `["kubernetes.io","pod","uid"]`},
		{KindKubernetes, `"sub"`, `["sub"]`},
		{KindGeneric, `"/a~1b/c~0d"`, `["a/b","c~d"]`},
		{KindGeneric, `"/~01"`, `["~1"]`},
		{KindGeneric, `["a.b","c"]`, `["a.b","c"]`},
		{KindGeneric, `"email"`, `["email"]`},
		{KindGitHubActions, `"repository_id"`, `["repository_id"]`},
	} {
		got, err := ParseClaimPath(tc.kind, json.RawMessage(tc.raw))
		if err != nil || got.String() != tc.want {
			t.Errorf("ParseClaimPath(%s, %s) = %s, %v; want %s", tc.kind, tc.raw, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		kind Kind
		raw  string
	}{
		{KindGeneric, `"kubernetes.io.namespace"`},       // no known reading on a generic issuer
		{KindGitHubActions, `"repository.owner"`},        // flat claims have no dotted shorthand
		{KindKubernetes, `"kubernetes.io.unknown.deep"`}, // known prefix, unknown multi-part rest
		{KindKubernetes, `"kubernetes.namespace"`},
	} {
		if got, err := ParseClaimPath(tc.kind, json.RawMessage(tc.raw)); err == nil {
			t.Errorf("ParseClaimPath(%s, %s) = %s, want an error", tc.kind, tc.raw, got)
		}
	}
}

func TestShorthandAmbiguityIsRejected(t *testing.T) {
	// Two keys that can both be read from "a.b.c": "a.b" then "c", or "a" then "b.c".
	node := schemaNode{"a": {"b.c": nil}, "a.b": {"c": nil}}
	readings := resolveShorthand(node, []string{"a", "b", "c"})
	if len(readings) != 2 {
		t.Fatalf("readings = %v, want two", readings)
	}
}

func TestEvaluateKubernetesFixture(t *testing.T) {
	claims := decodeClaims(t, kubernetesToken)
	raw := `[{"claim":"sub","op":"eq","values":["system:serviceaccount:team-a:builder"]},
	  {"claim":"kubernetes.io.namespace","op":"eq","values":["team-a"]},
	  {"claim":"kubernetes.io.serviceaccount.name","op":"in","values":["builder","deployer"]},
	  {"claim":["kubernetes.io","pod","name"],"op":"glob","values":["builder-*"]},
	  {"claim":"/kubernetes.io/node/name","op":"prefix","values":["ip-10-"]}]`
	canonical, _, err := Validate(KindKubernetes, []byte(raw), ValidateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy(KindKubernetes, "p1", "builder", "sa-1", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Evaluate([]Policy{policy}, claims); err != nil || got.ID != "p1" {
		t.Fatalf("Evaluate = %+v, %v", got, err)
	}
	other := decodeClaims(t, strings.Replace(kubernetesToken, `"namespace": "team-a"`, `"namespace": "team-b"`, 1))
	_, err = Evaluate([]Policy{policy}, other)
	var noMatch *NoMatchError
	if !errors.Is(err, ErrNoMatch) || !errors.As(err, &noMatch) || len(noMatch.Failures) != 1 {
		t.Fatalf("namespace mismatch err = %v", err)
	}
	if failure := noMatch.Failures[0]; failure.Condition != 1 || failure.Claim.String() != `["kubernetes.io","namespace"]` || failure.Reason != "claim value does not match" {
		t.Fatalf("failure = %+v", failure)
	}
	if text := noMatch.Failures[0].String(); strings.Contains(text, "team-a") || strings.Contains(text, "team-b") {
		t.Fatalf("failure detail leaks claim values: %s", text)
	}
}

func TestEvaluateGitHubAndGitLabTokens(t *testing.T) {
	github := decodeClaims(t, githubToken)
	policy := func(kind Kind, name, sa, raw string) Policy {
		t.Helper()
		canonical, _, err := Validate(kind, []byte(raw), ValidateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := NewPolicy(kind, name+"-id", name, sa, canonical)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	release := policy(KindGitHubActions, "release", "sa-1", `[{"claim":"repository_id","op":"eq","values":["1296269"]},{"claim":"environment","op":"eq","values":["packages-publish"]},{"claim":"job_workflow_ref","op":"glob","values":["acme/app/.github/workflows/release.yml@refs/**"]},{"claim":"event_name","op":"in","values":["push","workflow_dispatch"]}]`)
	if got, err := Evaluate([]Policy{release}, github); err != nil || got.Name != "release" {
		t.Fatalf("Evaluate = %+v, %v", got, err)
	}
	renamed := decodeClaims(t, strings.Replace(githubToken, `"repository_id": "1296269"`, `"repository_id": "7777777"`, 1))
	if _, err := Evaluate([]Policy{release}, renamed); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a re-registered repository (new id, same name) matched: %v", err)
	}
	pullRequest := decodeClaims(t, strings.Replace(githubToken, `"event_name": "push"`, `"event_name": "pull_request_target"`, 1))
	if _, err := Evaluate([]Policy{release}, pullRequest); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("pull_request_target matched: %v", err)
	}
	gitlab := policy(KindGitLabCI, "gl", "sa-2", `[{"claim":"project_id","op":"eq","values":["42"]},{"claim":"ref_protected","op":"eq","values":["true"]}]`)
	if got, err := Evaluate([]Policy{gitlab}, decodeClaims(t, gitlabToken)); err != nil || got.Name != "gl" {
		t.Fatalf("gitlab Evaluate = %+v, %v", got, err)
	}
	unprotected := decodeClaims(t, strings.Replace(gitlabToken, `"ref_protected": "true"`, `"ref_protected": "false"`, 1))
	if _, err := Evaluate([]Policy{gitlab}, unprotected); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unprotected ref matched: %v", err)
	}
}

func TestConditionTypeHandling(t *testing.T) {
	claims := decodeClaims(t, `{"sub":"s","n":5,"big":9007199254740993,"neg":-3,"frac":5.5,"exp":1e3,"t":true,"f":false,"obj":{"a":"b"},"arr":["x"],"nil":null,"s":"Value"}`)
	for _, tc := range []struct {
		claim, op, value string
		ok               bool
		reason           string
	}{
		{"n", "eq", "5", true, ""},
		{"neg", "eq", "-3", true, ""},
		{"big", "eq", "9007199254740993", true, ""},
		{"t", "eq", "true", true, ""},
		{"f", "eq", "false", true, ""},
		{"n", "eq", "5.0", false, "claim value does not match"},
		{"frac", "eq", "5.5", false, unsupportedType},
		{"exp", "eq", "1000", false, unsupportedType},
		{"obj", "eq", "b", false, unsupportedType},
		{"arr", "in", "x", false, unsupportedType},
		{"nil", "eq", "null", false, unsupportedType},
		{"s", "eq", "value", false, "claim value does not match"},
		{"s", "eq", "Value", true, ""},
		{"s", "prefix", "Val", true, ""},
		{"s", "prefix", "val", false, "claim value does not match"},
		{"s", "glob", "V*e", true, ""},
		{"s", "glob", "v*e", false, "claim value does not match"},
		{"absent", "eq", "x", false, "claim is missing"},
		{"sub.deeper", "eq", "x", false, "claim is missing"},
	} {
		raw := `[{"claim":["` + strings.Join(strings.Split(tc.claim, "."), `","`) + `"],"op":"` + tc.op + `","values":["` + tc.value + `"]}]`
		conds := conditions(t, KindGeneric, raw)
		ok, reason := conds[0].match(claims)
		if ok != tc.ok || reason != tc.reason {
			t.Errorf("%s %s %s: got %v %q, want %v %q", tc.claim, tc.op, tc.value, ok, reason, tc.ok, tc.reason)
		}
	}
	// Claim values supplied as native Go numbers (not decoded with UseNumber) work too.
	native := map[string]any{"sub": "s", "i": 7, "i64": int64(8), "f": float64(9), "frac": 1.5}
	for claim, want := range map[string]string{"i": "7", "i64": "8", "f": "9"} {
		conds := conditions(t, KindGeneric, `[{"claim":"`+claim+`","op":"eq","values":["`+want+`"]}]`)
		if ok, reason := conds[0].match(native); !ok {
			t.Errorf("native %s: %s", claim, reason)
		}
	}
	if ok, _ := conditions(t, KindGeneric, `[{"claim":"frac","op":"eq","values":["1.5"]}]`)[0].match(native); ok {
		t.Error("a fractional native number matched")
	}
}

func TestEvaluateRequiresSubject(t *testing.T) {
	policy, _ := NewPolicy(KindGeneric, "p", "p", "sa", []byte(`[{"claim":"email","op":"eq","values":["ci@example.test"]}]`))
	if _, err := Evaluate([]Policy{policy}, map[string]any{"email": "ci@example.test"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a token without sub matched: %v", err)
	}
	if _, err := Evaluate(nil, map[string]any{"sub": "x"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("no policies err = %v", err)
	}
}

func TestEvaluateAmbiguity(t *testing.T) {
	mk := func(id, name, account, sub string) Policy {
		p, err := NewPolicy(KindGeneric, id, name, account, []byte(`[{"claim":"sub","op":"glob","values":["`+sub+`"]}]`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	claims := map[string]any{"sub": "ci:app:main"}
	// Same service account: the first by name wins, independent of input order.
	same := []Policy{mk("2", "zeta", "sa-1", "ci:*:main"), mk("1", "alpha", "sa-1", "ci:app:*"), mk("3", "mid", "sa-1", "ci:**")}
	for range 3 {
		got, err := Evaluate(same, claims)
		if err != nil || got.Name != "alpha" {
			t.Fatalf("Evaluate = %+v, %v; want alpha", got, err)
		}
		same = append(same[1:], same[0])
	}
	// Equal names fall back to the id.
	tied := []Policy{mk("b", "dup", "sa-1", "ci:**"), mk("a", "dup", "sa-1", "ci:**")}
	if got, _ := Evaluate(tied, claims); got.ID != "a" {
		t.Fatalf("tie broke to %s, want a", got.ID)
	}
	// Different service accounts: reject, never pick.
	mixed := []Policy{mk("1", "alpha", "sa-1", "ci:**"), mk("2", "beta", "sa-2", "ci:app:*"), mk("3", "gamma", "sa-1", "other:**")}
	_, err := Evaluate(mixed, claims)
	var ambiguous *AmbiguousError
	if !errors.Is(err, ErrAmbiguous) || !errors.As(err, &ambiguous) || strings.Join(ambiguous.Policies, ",") != "alpha,beta" {
		t.Fatalf("err = %v (%+v)", err, ambiguous)
	}
	if !strings.Contains(err.Error(), "alpha, beta") {
		t.Fatalf("error text = %q", err)
	}
	if errors.Is(err, ErrNoMatch) {
		t.Fatal("ambiguity must not look like a non-match")
	}
}

func TestNewPolicyRejectsBadStoredConditions(t *testing.T) {
	if _, err := NewPolicy(KindGeneric, "1", "p", "sa", []byte("nope")); err == nil || !strings.Contains(err.Error(), "policy p") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckCanonical(t *testing.T) {
	canonical, _, err := Validate(KindGeneric, []byte(`[{"claim":"sub","op":"glob","values":["ci:*"]}]`), ValidateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCanonical(canonical); err != nil {
		t.Fatalf("canonical form rejected: %v", err)
	}
	spaced := strings.ReplaceAll(string(canonical), ",", ", ")
	if err := CheckCanonical([]byte(spaced)); err != nil {
		t.Fatalf("whitespace must not matter: %v", err)
	}
	for _, raw := range []string{`[{"claim":"sub","op":"eq","values":["a"]}]`, `[null]`, `[]`, `nope`, `[{"claim":["sub"],"values":["a"],"op":"eq"}]`} {
		if err := CheckCanonical([]byte(raw)); err == nil {
			t.Errorf("CheckCanonical(%s) accepted", raw)
		}
	}
}

func TestErrorTexts(t *testing.T) {
	if (&NoMatchError{}).Error() != ErrNoMatch.Error() || (&ValidationError{Reason: "x"}).Error() != "invalid workload trust policy: x" {
		t.Fatal("unexpected error text")
	}
}
