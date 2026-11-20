package notarxctl

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	workloadIssuerID = "00000000-0000-4000-e000-000000000001"
	workloadPolicyID = "00000000-0000-4000-e000-000000000002"
	workloadJWT      = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.SYNTHETIC-workload-token-marker"
	exchangedToken   = "v2.SYNTHETIC-exchanged-token-marker"
)

func workloadIssuerJSON(version, state string) string {
	return `{"id":"` + workloadIssuerID + `","name":"gh","kind":"github_actions","issuer":"https://token.actions.example.test","audiences":["https://notarx.example.test"],"allowedAlgs":["RS256"],"jwksMode":"discovery","allowPrivateNetwork":false,"state":"` + state + `","version":"` + version + `","createdAt":"2026-10-15T08:00:00Z","updatedAt":"2026-10-15T08:00:00Z"}`
}

func workloadPolicyJSON(version, state string) string {
	return `{"id":"` + workloadPolicyID + `","name":"publish","issuerId":"` + workloadIssuerID + `","serviceAccountId":"` + accountID + `","conditions":[{"claim":["repository_id"],"op":"eq","values":["1"]}],"maxTokenTtlSeconds":900,"singleUse":false,"state":"` + state + `","version":"` + version + `","createdAt":"2026-10-15T08:00:00Z","updatedAt":"2026-10-15T08:00:00Z"}`
}

func TestWorkloadIssuerCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"workload", "issuer", "create", "gh", "--issuer", "https://x.example.test", "--audience", "a"},
		[]string{"workload", "issuer", "create", "gh", "--kind", "generic", "--audience", "a"},
		[]string{"workload", "issuer", "create", "gh", "--kind", "generic", "--issuer", "https://x.example.test"},
		[]string{"workload", "issuer", "create", "gh", "--kind", "generic", "--issuer", "https://x.example.test", "--audience", "a", "--jwks-mode", "other"},
		[]string{"workload", "issuer", "create", "gh", "--kind", "generic", "--issuer", "https://x.example.test", "--audience", "a", "--alg", "HS256"},
		[]string{"workload", "issuer", "create", "gh", "--kind", "generic", "--issuer", "https://x.example.test", "--audience", "a", "--static-jwks-file", "/nonexistent"},
		[]string{"workload", "issuer", "update", workloadIssuerID},
		[]string{"workload", "issuer", "delete", workloadIssuerID},
		[]string{"workload", "issuer", "probe"},
	)
	api.on("GET", "/api/v2/workload-identity/issuers", 200, `{"items":[`+workloadIssuerJSON("1", "active")+`]}`)
	if code, stdout, _ := runCLI(t, "workload", "issuer", "list"); code != ExitOK || !strings.Contains(stdout, "github_actions") || !strings.Contains(stdout, workloadIssuerID) {
		t.Fatalf("list: %d %s", code, stdout)
	}
	api.on("POST", "/api/v2/workload-identity/issuers", 201, workloadIssuerJSON("1", "active"))
	dir := t.TempDir()
	jwks := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(jwks, []byte(`{"keys":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "workload", "issuer", "create", "gh", "--kind", "github_actions", "--issuer", "https://token.actions.example.test", "--audience", "https://notarx.example.test", "--audience", "second", "--alg", "RS256", "--static-jwks-file", jwks, "--jwks-mode", "static", "--json")
	if code != ExitOK {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	var sent map[string]any
	for _, request := range api.recorded() {
		if request.Method == "POST" {
			_ = json.Unmarshal([]byte(request.Body), &sent)
		}
	}
	if sent["name"] != "gh" || sent["kind"] != "github_actions" || sent["jwksMode"] != "static" || len(sent["audiences"].([]any)) != 2 || sent["staticJwks"] == nil {
		t.Fatalf("body = %v", sent)
	}
	if code, stdout, _ := runCLI(t, "workload", "issuer", "create", "gh", "--kind", "generic", "--issuer", "https://x.example.test", "--audience", "a", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "dry run") {
		t.Fatalf("dry run: %d %s", code, stdout)
	}

	path := "/api/v2/workload-identity/issuers/" + workloadIssuerID
	api.on("GET", path, 200, workloadIssuerJSON("7", "active"))
	api.on("PATCH", path, 200, workloadIssuerJSON("8", "disabled"))
	if code, stdout, stderr := runCLI(t, "workload", "issuer", "update", workloadIssuerID, "--state", "disabled", "--json"); code != ExitOK {
		t.Fatalf("update: %d %s %s", code, stdout, stderr)
	}
	patches := 0
	for _, request := range api.recorded() {
		if request.Method == "PATCH" {
			patches++
			if request.IfMatch != "7" || !strings.Contains(request.Body, `"state":"disabled"`) {
				t.Fatalf("patch = %+v", request)
			}
		}
	}
	if patches != 1 {
		t.Fatalf("patches = %d", patches)
	}
	api.on("PATCH", path, 412, `{"code":"version_conflict","message":"changed"}`)
	if code, _, stderr := runCLI(t, "workload", "issuer", "update", workloadIssuerID, "--state", "active", "--if-match", "1"); code == ExitOK || !strings.Contains(stderr, "retry") {
		t.Fatalf("stale version: %d %s", code, stderr)
	}
	api.on("DELETE", path, 204, "")
	if code, stdout, stderr := runCLI(t, "workload", "issuer", "delete", workloadIssuerID, "--yes", "--if-match", "8", "--json"); code != ExitOK {
		t.Fatalf("delete: %d %s %s", code, stdout, stderr)
	}
	if code, stdout, _ := runCLI(t, "workload", "issuer", "show", workloadIssuerID); code != ExitOK || !strings.Contains(stdout, "github_actions") {
		t.Fatalf("show: %d %s", code, stdout)
	}
	api.on("POST", "/api/v2/workload-identity/issuers:probe", 200, `{"reachable":true,"jwksUri":"https://x/jwks","keys":[{"keyId":"k1","keyType":"RSA","algorithm":"RS256"}],"algorithms":["RS256"]}`)
	if code, stdout, _ := runCLI(t, "workload", "issuer", "probe", "--id", workloadIssuerID); code != ExitOK || !strings.Contains(stdout, "k1 RSA RS256") {
		t.Fatalf("probe: %d %s", code, stdout)
	}
}

func TestWorkloadPolicyCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	base := []string{"workload", "policy", "create", "publish", "--issuer", workloadIssuerID, "--service-account", accountID}
	expectUsage(t, api,
		base,
		append(append([]string{}, base...), "--condition", "no-op"),
		append(append([]string{}, base...), "--condition", "ref:eq:refs/heads/main", "--max-token-ttl", "48h"),
		append(append([]string{}, base...), "--condition", "ref:eq:refs/heads/main", "--conditions-file", "x"),
		[]string{"workload", "policy", "create", "publish", "--issuer", "not-a-uuid", "--service-account", accountID, "--condition", "ref:eq:x"},
		[]string{"workload", "policy", "update", workloadPolicyID},
		[]string{"workload", "policy", "delete", workloadPolicyID},
		[]string{"workload", "policy", "test"},
		[]string{"workload", "policy", "test", "--token-file", "a", "--claims-file", "b"},
	)
	api.on("POST", "/api/v2/workload-identity/policies", 201, workloadPolicyJSON("1", "active"))
	sample := filepath.Join(t.TempDir(), "sample.jwt")
	if err := os.WriteFile(sample, []byte(workloadJWT+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{}, base...), "--condition", "repository_id:eq:1296269", "--condition", "ref:in:refs/heads/main,refs/heads/release", "--max-token-ttl", "10m", "--single-use", "--sample-token-file", sample, "--json")
	code, stdout, stderr := runCLI(t, args...)
	if code != ExitOK {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	noSecret(t, workloadJWT, stdout, stderr)
	var body struct {
		Conditions []struct {
			Claim  string   `json:"claim"`
			Op     string   `json:"op"`
			Values []string `json:"values"`
		} `json:"conditions"`
		MaxTokenTtlSeconds int    `json:"maxTokenTtlSeconds"`
		SingleUse          bool   `json:"singleUse"`
		SampleToken        string `json:"sampleToken"`
	}
	for _, request := range api.recorded() {
		if request.Method == "POST" {
			_ = json.Unmarshal([]byte(request.Body), &body)
		}
	}
	if len(body.Conditions) != 2 || body.Conditions[1].Op != "in" || len(body.Conditions[1].Values) != 2 || body.MaxTokenTtlSeconds != 600 || !body.SingleUse || body.SampleToken != workloadJWT {
		t.Fatalf("body = %+v", body)
	}
	// A kubernetes subject carries colons; the value is everything after the operator.
	if code, stdout, _ := runCLI(t, append(append([]string{}, base...), "--condition", "sub:eq:system:serviceaccount:team-a:builder", "--dry-run")...); code != ExitOK || !strings.Contains(stdout, "system:serviceaccount:team-a:builder") {
		t.Fatalf("dry run: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, append(append([]string{}, base...), "--condition", "ref:eq:x", "--single-use", "--sample-token-file", sample, "--dry-run")...); code != ExitOK || strings.Contains(stdout, workloadJWT) {
		t.Fatalf("a plan must not carry the sample token: %d %s", code, stdout)
	}

	path := "/api/v2/workload-identity/policies/" + workloadPolicyID
	api.on("GET", path, 200, workloadPolicyJSON("3", "active"))
	api.on("PATCH", path, 200, workloadPolicyJSON("4", "disabled"))
	if code, stdout, stderr := runCLI(t, "workload", "policy", "update", workloadPolicyID, "--state", "disabled", "--json"); code != ExitOK {
		t.Fatalf("update: %d %s %s", code, stdout, stderr)
	}
	api.on("DELETE", path, 204, "")
	if code, stdout, stderr := runCLI(t, "workload", "policy", "delete", workloadPolicyID, "--yes", "--json"); code != ExitOK {
		t.Fatalf("delete: %d %s %s", code, stdout, stderr)
	}
	if code, stdout, _ := runCLI(t, "workload", "policy", "show", workloadPolicyID); code != ExitOK || !strings.Contains(stdout, "repository_id eq 1") {
		t.Fatalf("show: %d %s", code, stdout)
	}
	api.on("GET", "/api/v2/workload-identity/policies", 200, `{"items":[`+workloadPolicyJSON("1", "active")+`]}`)
	if code, stdout, _ := runCLI(t, "workload", "policy", "list"); code != ExitOK || !strings.Contains(stdout, "service-account:"+accountID) {
		t.Fatalf("list: %d %s", code, stdout)
	}
}

func TestWorkloadPolicyTestReportsTheFailingCondition(t *testing.T) {
	api := newRouteAPI(t, "")
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(workloadJWT), 0o600); err != nil {
		t.Fatal(err)
	}
	api.on("POST", "/api/v2/workload-identity/policies:test", 200, `{"signatureVerified":false,"verificationError":"expired","results":[{"policyId":"`+workloadPolicyID+`","policyName":"publish","matched":false,"failure":{"conditionIndex":0,"claim":["repository_id"],"reason":"claim value does not match"}}],"warnings":["the token has no jti"]}`)
	code, stdout, stderr := runCLI(t, "workload", "policy", "test", "--token-file", token, "--issuer", workloadIssuerID)
	if code != ExitOK {
		t.Fatalf("exit %d %s %s", code, stdout, stderr)
	}
	noSecret(t, workloadJWT, stdout, stderr)
	for _, want := range []string{"not verified: expired", "condition 0 on repository_id failed", "warning: the token has no jti"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output lacks %q:\n%s", want, stdout)
		}
	}
	claims := filepath.Join(t.TempDir(), "claims.json")
	if err := os.WriteFile(claims, []byte(`{"sub":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI(t, "workload", "policy", "test", "--claims-file", claims, "--policy", workloadPolicyID); code != ExitOK {
		t.Fatalf("claims exit %d", code)
	}
}

func TestAuthLoginExchangesAWorkloadTokenIntoAFile(t *testing.T) {
	api := newRouteAPI(t, "")
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "workload.jwt")
	if err := os.WriteFile(tokenFile, []byte(workloadJWT+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "session")
	expectUsage(t, api,
		[]string{"auth", "login", "--workload-token-file", tokenFile, "--username", "alice", "--secret-output", output},
		[]string{"auth", "login", "--workload-policy", "publish", "--secret-output", output},
		[]string{"auth", "login", "--workload-token-file", filepath.Join(dir, "missing"), "--secret-output", output},
		[]string{"auth", "login", "--workload-token-file", tokenFile},
	)
	api.onFunc("POST", "/auth/token-exchange", func(*http.Request) (int, string) {
		return 200, `{"access_token":"` + exchangedToken + `","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":900}`
	})
	code, stdout, stderr := runCLI(t, "auth", "login", "--workload-token-file", tokenFile, "--workload-policy", "publish", "--secret-output", output, "--json")
	if code != ExitOK {
		t.Fatalf("exit %d %s %s", code, stdout, stderr)
	}
	noSecret(t, exchangedToken, stdout, stderr)
	noSecret(t, workloadJWT, stdout, stderr)
	var form url.Values
	for _, request := range api.recorded() {
		if request.Path == "/auth/token-exchange" {
			form, _ = url.ParseQuery(request.Body)
		}
	}
	if form.Get("grant_type") != tokenExchangeGrantType || form.Get("subject_token") != workloadJWT || form.Get("subject_token_type") != tokenExchangeSubjectJWT || form.Get("policy") != "publish" {
		t.Fatalf("form = %v", form)
	}
	if data, _ := os.ReadFile(output); string(data) != exchangedToken+"\n" {
		t.Fatalf("session file = %q", data)
	}
	var result authLoginResult
	_ = json.Unmarshal(decodeEnvelope(t, stdout).Data, &result)
	if result.AuthenticationKind != "workload_federation" || result.ExpiresIn != 900 || result.SecretOutput == nil || !result.SecretOutput.Delivered {
		t.Fatalf("result = %+v", result)
	}
}

func TestAuthLoginWorkloadFailureKeepsTheTokenOut(t *testing.T) {
	api := newRouteAPI(t, "")
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "workload.jwt")
	if err := os.WriteFile(tokenFile, []byte(workloadJWT), 0o600); err != nil {
		t.Fatal(err)
	}
	api.onFunc("POST", "/auth/token-exchange", func(*http.Request) (int, string) {
		return 400, `{"code":"invalid_grant","message":"the subject token is not valid for this service"}`
	})
	output := filepath.Join(dir, "session")
	code, stdout, stderr := runCLI(t, "auth", "login", "--workload-token-file", tokenFile, "--secret-output", output)
	if code == ExitOK {
		t.Fatalf("a failed exchange exited 0: %s", stdout)
	}
	noSecret(t, workloadJWT, stdout, stderr)
	if _, err := os.Stat(output); err == nil || len(leftoverTemps(t, dir)) != 0 {
		t.Fatal("a failed exchange left a file behind")
	}
}

func TestOAuthErrorAnswersAreParsed(t *testing.T) {
	api := parseAPIError(&apiResponse{StatusCode: 400, Header: http.Header{}, Body: []byte(`{"error":"invalid_grant","error_description":"the subject token is not valid"}`)})
	if api.Code != "invalid_grant" || api.Message != "the subject token is not valid" {
		t.Fatalf("error = %+v", api)
	}
}
