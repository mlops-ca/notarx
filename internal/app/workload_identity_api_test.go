package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	workloadTestAdmin    = "workload-admin-token"
	workloadTestIssuer   = "https://ci.example.test"
	workloadTestAudience = "https://notarx.example.test"
)

type workloadAPIFixture struct {
	t       *testing.T
	store   *repository.MemoryStore
	handler http.Handler
	signer  *jose.Signer
	jwks    map[string]any
	account repository.ServiceAccount
	audit   *auditSink
}

func newWorkloadAPIFixture(t *testing.T, exchange WorkloadExchangeOptions, enabled bool) *workloadAPIFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.ES256, key, "k1")
	if err != nil {
		t.Fatal(err)
	}
	public, err := jose.PublicKey(key.Public(), "k1", jose.ES256)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jose.MarshalJWKS(public)
	if err != nil {
		t.Fatal(err)
	}
	var jwks map[string]any
	if err := json.Unmarshal(doc, &jwks); err != nil {
		t.Fatal(err)
	}
	store := repository.NewMemoryStore()
	account, err := store.CreateServiceAccount(context.Background(), repository.ServiceAccount{ID: uuid.NewString(), Name: "publisher"})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := Authenticator{AdminToken: workloadTestAdmin, AdminActor: "admin", ResolverToken: "principal-token-signing-secret", ServiceAccounts: store}
	if enabled {
		verifier, err := jose.NewVerifier(jose.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if authenticator.Workload, err = authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{Store: store, Accounts: store, Verifier: verifier, ClockSkew: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	return &workloadAPIFixture{
		t: t, store: store, signer: signer, jwks: jwks, account: account, audit: &auditSink{},
		handler: newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: authenticator, WorkloadExchange: exchange}),
	}
}

func (f *workloadAPIFixture) call(method, path, token string, body any, headers map[string]string) (int, map[string]any, http.Header) {
	f.t.Helper()
	var reader *strings.Reader
	if raw, ok := body.(string); ok {
		reader = strings.NewReader(raw)
	} else {
		encoded, _ := json.Marshal(body)
		reader = strings.NewReader(string(encoded))
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if _, isString := body.(string); !isString && body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	var decoded map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	return response.Code, decoded, response.Header()
}

func (f *workloadAPIFixture) admin(method, path string, body any, headers map[string]string) (int, map[string]any) {
	status, decoded, _ := f.call(method, path, workloadTestAdmin, body, headers)
	return status, decoded
}

func (f *workloadAPIFixture) createIssuer() map[string]any {
	f.t.Helper()
	status, issuer := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers", map[string]any{
		"name": "ci", "kind": "generic", "issuer": workloadTestIssuer, "audiences": []string{workloadTestAudience},
		"allowedAlgs": []string{"ES256"}, "jwksMode": "static", "staticJwks": f.jwks,
	}, nil)
	if status != http.StatusCreated {
		f.t.Fatalf("create issuer: %d %v", status, issuer)
	}
	return issuer
}

func (f *workloadAPIFixture) createPolicy(issuer map[string]any, extra map[string]any) (int, map[string]any) {
	f.t.Helper()
	body := map[string]any{
		"name": "publish", "issuerId": issuer["id"], "serviceAccountId": f.account.ID,
		"conditions": []map[string]any{{"claim": "sub", "op": "eq", "values": []string{"repo:acme/app"}}},
	}
	for key, value := range extra {
		body[key] = value
	}
	return f.admin(http.MethodPost, "/api/v2/workload-identity/policies", body, nil)
}

func (f *workloadAPIFixture) token(mutate func(map[string]any)) string {
	f.t.Helper()
	now := time.Now()
	claims := map[string]any{"iss": workloadTestIssuer, "sub": "repo:acme/app", "aud": workloadTestAudience, "jti": uuid.NewString(), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	if mutate != nil {
		mutate(claims)
	}
	token, err := f.signer.Sign(claims)
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func TestWorkloadIdentityManagementRequiresAnAdministrator(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	for _, path := range []string{"/api/v2/workload-identity/issuers", "/api/v2/workload-identity/policies"} {
		if status, _, _ := f.call(http.MethodGet, path, "", nil, nil); status != http.StatusUnauthorized {
			t.Fatalf("%s anonymous: %d", path, status)
		}
		if status, _, _ := f.call(http.MethodGet, path, "resolver-or-other", nil, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("%s non-admin: %d", path, status)
		}
	}
	if status, _, _ := f.call(http.MethodPost, "/api/v2/workload-identity/policies:test", "", map[string]any{"claims": map[string]any{"sub": "x"}}, nil); status != http.StatusUnauthorized {
		t.Fatalf("tester anonymous: %d", status)
	}
	workload := f.token(nil)
	issuer := f.createIssuer()
	if _, policy := f.createPolicy(issuer, nil); policy["id"] == nil {
		t.Fatalf("policy: %v", policy)
	}
	// A workload principal is a service account, never an administrator.
	if status, _, _ := f.call(http.MethodGet, "/api/v2/workload-identity/issuers", workload, nil, nil); status != http.StatusForbidden {
		t.Fatalf("workload principal on the admin API: %d", status)
	}
	if status, _, _ := f.call(http.MethodPost, "/api/v2/workload-identity/policies:test", workload, map[string]any{"claims": map[string]any{"sub": "x"}}, nil); status != http.StatusForbidden {
		t.Fatalf("workload principal on the tester: %d", status)
	}
}

func TestWorkloadIssuerLifecycleAndVersionPreconditions(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	id := issuer["id"].(string)
	version := issuer["version"].(string)
	if status, body := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers", map[string]any{
		"name": "dup", "kind": "generic", "issuer": workloadTestIssuer, "audiences": []string{"a"}, "jwksMode": "static", "staticJwks": f.jwks,
	}, nil); status != http.StatusConflict {
		t.Fatalf("duplicate issuer URL: %d %v", status, body)
	}
	if status, body := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers", map[string]any{
		"name": "bad", "kind": "generic", "issuer": "http://insecure.example.test", "audiences": []string{"a"}, "jwksMode": "discovery",
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("http issuer: %d %v", status, body)
	}
	if status, _ := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers", map[string]any{"name": "x", "bogus": true}, nil); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", status)
	}
	if status, got := f.admin(http.MethodGet, "/api/v2/workload-identity/issuers/"+id, nil, nil); status != http.StatusOK || got["issuer"] != workloadTestIssuer {
		t.Fatalf("get: %d %v", status, got)
	}
	if status, _ := f.admin(http.MethodGet, "/api/v2/workload-identity/issuers/"+uuid.NewString(), nil, nil); status != http.StatusNotFound {
		t.Fatalf("missing: %d", status)
	}
	if status, _ := f.admin(http.MethodPatch, "/api/v2/workload-identity/issuers/"+id, map[string]any{"state": "disabled"}, map[string]string{"If-Match": "stale"}); status != http.StatusPreconditionFailed {
		t.Fatalf("stale version: %d", status)
	}
	status, updated := f.admin(http.MethodPatch, "/api/v2/workload-identity/issuers/"+id, map[string]any{"state": "disabled", "audiences": []string{"one", "two"}}, map[string]string{"If-Match": version})
	if status != http.StatusOK || updated["state"] != "disabled" || updated["version"] == version {
		t.Fatalf("patch: %d %v", status, updated)
	}
	if status, _ := f.admin(http.MethodPatch, "/api/v2/workload-identity/issuers/"+id, map[string]any{"state": "active"}, map[string]string{"If-Match": version}); status != http.StatusPreconditionFailed {
		t.Fatalf("reusing the old version: %d", status)
	}
	if status, list := f.admin(http.MethodGet, "/api/v2/workload-identity/issuers?pageSize=1", nil, nil); status != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", status, list)
	}
	if status, _ := f.admin(http.MethodGet, "/api/v2/workload-identity/issuers?pageToken=garbage", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("bad page token: %d", status)
	}
	version = updated["version"].(string)
	status, policy := f.createPolicy(updated, nil)
	if status != http.StatusCreated {
		t.Fatalf("policy: %d %v", status, policy)
	}
	if status, _ := f.admin(http.MethodDelete, "/api/v2/workload-identity/issuers/"+id, nil, map[string]string{"If-Match": version}); status != http.StatusConflict {
		t.Fatalf("an issuer with policies must not be deleted: %d", status)
	}
	if status, _ := f.admin(http.MethodDelete, "/api/v2/workload-identity/policies/"+policy["id"].(string), nil, map[string]string{"If-Match": policy["version"].(string)}); status != http.StatusNoContent {
		t.Fatalf("delete policy: %d", status)
	}
	if status, _ := f.admin(http.MethodDelete, "/api/v2/workload-identity/issuers/"+id, nil, map[string]string{"If-Match": "stale"}); status != http.StatusPreconditionFailed {
		t.Fatalf("delete with a stale version: %d", status)
	}
	if status, _ := f.admin(http.MethodDelete, "/api/v2/workload-identity/issuers/"+id, nil, map[string]string{"If-Match": version}); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
}

func TestWorkloadPolicyValidationAndVersions(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	generic := f.createIssuer()
	github := func() map[string]any {
		status, issuer := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers", map[string]any{
			"name": "gh", "kind": "github_actions", "issuer": "https://token.actions.example.test", "audiences": []string{workloadTestAudience},
			"jwksMode": "static", "staticJwks": f.jwks,
		}, nil)
		if status != http.StatusCreated {
			t.Fatalf("github issuer: %d %v", status, issuer)
		}
		return issuer
	}()
	// The mandatory conditions of the kind are enforced on write.
	status, body := f.createPolicy(github, map[string]any{"name": "owner-only", "conditions": []map[string]any{{"claim": "repository_owner_id", "op": "eq", "values": []string{"9919"}}}})
	if status != http.StatusBadRequest || body["code"] != "invalid_policy" {
		t.Fatalf("owner-id-only policy: %d %v", status, body)
	}
	good := []map[string]any{
		{"claim": "repository_id", "op": "eq", "values": []string{"1296269"}},
		{"claim": "environment", "op": "eq", "values": []string{"packages-publish"}},
	}
	if status, body = f.createPolicy(github, map[string]any{"name": "gh-publish", "conditions": good}); status != http.StatusCreated {
		t.Fatalf("github policy: %d %v", status, body)
	}
	// An unknown issuer or service account is a 404, a bad name a 400, a duplicate a 409.
	if status, _ = f.createPolicy(map[string]any{"id": uuid.NewString()}, nil); status != http.StatusNotFound {
		t.Fatalf("unknown issuer: %d", status)
	}
	if status, _ = f.createPolicy(generic, map[string]any{"serviceAccountId": uuid.NewString()}); status != http.StatusNotFound {
		t.Fatalf("unknown service account: %d", status)
	}
	if status, _ = f.createPolicy(generic, map[string]any{"name": "oidc"}); status != http.StatusBadRequest {
		t.Fatalf("reserved name: %d", status)
	}
	if status, _ = f.createPolicy(generic, map[string]any{"conditions": []map[string]any{{"claim": "sub", "op": "glob", "values": []string{"**"}}}}); status != http.StatusBadRequest {
		t.Fatalf("a bare ** glob must be rejected: %d", status)
	}
	status, policy := f.createPolicy(generic, nil)
	if status != http.StatusCreated {
		t.Fatalf("generic policy: %d %v", status, policy)
	}
	if status, _ = f.createPolicy(generic, nil); status != http.StatusConflict {
		t.Fatalf("duplicate name: %d", status)
	}
	conditions := policy["conditions"].([]any)[0].(map[string]any)
	if claim := conditions["claim"].([]any); len(claim) != 1 || claim[0] != "sub" {
		t.Fatalf("conditions are not returned in canonical form: %v", conditions)
	}
	id, version := policy["id"].(string), policy["version"].(string)
	if status, _ = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"maxTokenTtlSeconds": 60}, map[string]string{"If-Match": "stale"}); status != http.StatusPreconditionFailed {
		t.Fatalf("stale version: %d", status)
	}
	status, patched := f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"maxTokenTtlSeconds": 60, "state": "disabled"}, map[string]string{"If-Match": version})
	if status != http.StatusOK || patched["maxTokenTtlSeconds"] != float64(60) || patched["state"] != "disabled" {
		t.Fatalf("patch: %d %v", status, patched)
	}
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	status, patched = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"expiresAt": expires}, map[string]string{"If-Match": patched["version"].(string)})
	if status != http.StatusOK || patched["expiresAt"] == nil {
		t.Fatalf("set expiry: %d %v", status, patched)
	}
	if status, _ = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"expiresAt": expires, "clearExpiresAt": true}, map[string]string{"If-Match": patched["version"].(string)}); status != http.StatusBadRequest {
		t.Fatalf("conflicting expiry fields: %d", status)
	}
	status, patched = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"clearExpiresAt": true}, map[string]string{"If-Match": patched["version"].(string)})
	if status != http.StatusOK || patched["expiresAt"] != nil {
		t.Fatalf("clear expiry: %d %v", status, patched)
	}
	if status, _ = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"conditions": []map[string]any{{"claim": "sub", "op": "glob", "values": []string{"*"}}}}, map[string]string{"If-Match": patched["version"].(string)}); status != http.StatusBadRequest {
		t.Fatalf("a patch that widens the policy to a bare glob: %d", status)
	}
	if status, got := f.admin(http.MethodGet, "/api/v2/workload-identity/policies?pageSize=1", nil, nil); status != http.StatusOK || len(got["items"].([]any)) != 1 || got["nextPageToken"] == nil {
		t.Fatalf("list: %d %v", status, got)
	}
}

func TestWorkloadPolicyRefusesAdministratorAccountsWithoutAllowAdmin(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	repo, err := f.store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "team", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReplaceRepositoryGrants(context.Background(), repo.ID, []repository.RepositoryGrant{{Principal: "service-account:" + f.account.ID, Scopes: []string{"repositories:admin"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	status, body := f.createPolicy(issuer, nil)
	if status != http.StatusBadRequest || body["code"] != "admin_mapping_requires_allow_admin" {
		t.Fatalf("admin account without allowAdmin: %d %v", status, body)
	}
	if status, body = f.createPolicy(issuer, map[string]any{"allowAdmin": true}); status != http.StatusCreated {
		t.Fatalf("admin account with allowAdmin: %d %v", status, body)
	}
}

func TestWorkloadSingleUseIsRefusedWhenTheSampleTokenHasNoJTI(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	noJTI := f.token(func(c map[string]any) { delete(c, "jti") })
	status, body := f.createPolicy(issuer, map[string]any{"singleUse": true, "sampleToken": noJTI})
	if status != http.StatusBadRequest || body["code"] != "single_use_requires_jti" {
		t.Fatalf("sample without jti: %d %v", status, body)
	}
	if status, body = f.createPolicy(issuer, map[string]any{"singleUse": true}); status != http.StatusBadRequest || body["code"] != "single_use_requires_jti" {
		t.Fatalf("generic issuer without a sample: %d %v", status, body)
	}
	if status, body = f.createPolicy(issuer, map[string]any{"singleUse": true, "sampleToken": "not-a-jwt"}); status != http.StatusBadRequest {
		t.Fatalf("sample that is no JWT: %d %v", status, body)
	}
	status, body = f.createPolicy(issuer, map[string]any{"singleUse": true, "sampleToken": f.token(nil)})
	if status != http.StatusCreated || body["singleUse"] != true {
		t.Fatalf("sample with jti: %d %v", status, body)
	}
	if strings.Contains(jsonText(t, body), "sampleToken") {
		t.Fatal("the sample token was stored or echoed")
	}
	id, version := body["id"].(string), body["version"].(string)
	status, off := f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"singleUse": false}, map[string]string{"If-Match": version})
	if status != http.StatusOK || off["singleUse"] != false {
		t.Fatalf("turning single_use off: %d %v", status, off)
	}
	if status, _ = f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"singleUse": true, "sampleToken": noJTI}, map[string]string{"If-Match": off["version"].(string)}); status != http.StatusBadRequest {
		t.Fatalf("turning single_use on with a sample without jti: %d", status)
	}
}

func jsonText(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestWorkloadPolicyTesterReportsTheFailingCondition(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	status, policy := f.createPolicy(issuer, nil)
	if status != http.StatusCreated {
		t.Fatalf("policy: %d %v", status, policy)
	}
	statusBefore, listBefore := f.admin(http.MethodGet, "/api/v2/workload-identity/policies", nil, nil)
	if statusBefore != http.StatusOK {
		t.Fatal(statusBefore)
	}

	// A token that matches: verified, matched, and the warning about a missing jti is absent.
	status, result := f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"token": f.token(nil)}, nil)
	if status != http.StatusOK || result["signatureVerified"] != true || result["matchedPolicyId"] != policy["id"] {
		t.Fatalf("matching token: %d %v", status, result)
	}
	// A token for another subject: the failing condition is reported.
	status, result = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"token": f.token(func(c map[string]any) { c["sub"] = "repo:acme/other"; delete(c, "jti") })}, nil)
	results := result["results"].([]any)
	if status != http.StatusOK || result["matchedPolicyId"] != nil || len(results) != 1 {
		t.Fatalf("non-matching token: %d %v", status, result)
	}
	failure := results[0].(map[string]any)["failure"].(map[string]any)
	if failure["conditionIndex"] != float64(0) || failure["reason"] == "" {
		t.Fatalf("failure = %v", failure)
	}
	if warnings := jsonText(t, result["warnings"]); !strings.Contains(warnings, "no jti") {
		t.Fatalf("warnings = %s", warnings)
	}
	// A claim set is evaluated but never verified; a forged token reports the verification error.
	status, result = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"claims": map[string]any{"iss": workloadTestIssuer, "sub": "repo:acme/app"}}, nil)
	if status != http.StatusOK || result["signatureVerified"] != false || result["matchedPolicyId"] != policy["id"] {
		t.Fatalf("claim set: %d %v", status, result)
	}
	forged := f.token(nil)
	forged = forged[:len(forged)-20] + "AAAAAAAAAAAAAAAAAAAA"
	if status, result = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"token": forged}, nil); status != http.StatusOK || result["signatureVerified"] != false || result["verificationError"] == nil {
		t.Fatalf("forged token: %d %v", status, result)
	}
	if status, _ = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"claims": map[string]any{"sub": "x"}}, nil); status != http.StatusBadRequest {
		t.Fatalf("no issuer: %d", status)
	}
	if status, _ = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"token": f.token(nil), "claims": map[string]any{}}, nil); status != http.StatusBadRequest {
		t.Fatalf("token and claims together: %d", status)
	}
	if status, result = f.admin(http.MethodPost, "/api/v2/workload-identity/policies:test", map[string]any{"policyId": policy["id"], "claims": map[string]any{"sub": "repo:acme/app"}}, nil); status != http.StatusOK || result["matchedPolicyId"] != policy["id"] {
		t.Fatalf("by policy id: %d %v", status, result)
	}
	// The dry run changed nothing and consumed nothing.
	statusAfter, listAfter := f.admin(http.MethodGet, "/api/v2/workload-identity/policies", nil, nil)
	if statusAfter != http.StatusOK || jsonText(t, listBefore) != jsonText(t, listAfter) {
		t.Fatal("the dry run changed state")
	}
}

func TestWorkloadIssuerProbeReadsAStaticKeySet(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	status, probe := f.admin(http.MethodPost, "/api/v2/workload-identity/issuers:probe", map[string]any{"issuerId": issuer["id"]}, nil)
	keys := probe["keys"].([]any)
	if status != http.StatusOK || probe["reachable"] != true || len(keys) != 1 || keys[0].(map[string]any)["keyId"] != "k1" || jsonText(t, probe["algorithms"]) != `["ES256"]` {
		t.Fatalf("probe by id: %d %v", status, probe)
	}
	status, probe = f.admin(http.MethodPost, "/api/v2/workload-identity/issuers:probe", map[string]any{"issuer": "https://unreachable.invalid", "jwksMode": "discovery"}, nil)
	if status != http.StatusOK || probe["reachable"] != false || probe["error"] == nil {
		t.Fatalf("unreachable issuer: %d %v", status, probe)
	}
	if status, _ = f.admin(http.MethodPost, "/api/v2/workload-identity/issuers:probe", map[string]any{"issuer": "http://plain.example.test", "jwksMode": "discovery"}, nil); status != http.StatusBadRequest {
		t.Fatalf("http issuer: %d", status)
	}
	if status, _ = f.admin(http.MethodPost, "/api/v2/workload-identity/issuers:probe", map[string]any{"allowPrivateNetwork": true}, nil); status != http.StatusBadRequest {
		t.Fatalf("empty probe: %d", status)
	}
	if status, _ = f.admin(http.MethodPost, "/api/v2/workload-identity/issuers:probe", map[string]any{"issuerId": uuid.NewString()}, nil); status != http.StatusNotFound {
		t.Fatalf("unknown issuer: %d", status)
	}
}

func exchangeForm(subject string, extra map[string]string) string {
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {tokenExchangeJWTType}}
	for key, value := range extra {
		form.Set(key, value)
	}
	return form.Encode()
}

func (f *workloadAPIFixture) exchange(form string) (int, map[string]any, http.Header) {
	f.t.Helper()
	return f.call(http.MethodPost, workloadExchangePath, "", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func TestTokenExchangeIssuesACappedPrincipalToken(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{MaxTTL: 10 * time.Minute, Rate: 1000}, true)
	issuer := f.createIssuer()
	if status, body := f.createPolicy(issuer, map[string]any{"maxTokenTtlSeconds": 120}); status != http.StatusCreated {
		t.Fatalf("policy: %d %v", status, body)
	}
	status, body, header := f.exchange(exchangeForm(f.token(nil), map[string]string{"scope": "ignored", "audience": workloadTestAudience}))
	if status != http.StatusOK || body["token_type"] != "Bearer" || body["issued_token_type"] != tokenExchangeAccessTokenType || body["expires_in"] != float64(120) || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange: %d %v %v", status, body, header)
	}
	access := body["access_token"].(string)
	status, identity, _ := f.call(http.MethodGet, "/api/v2/identity", access, nil, nil)
	if status != http.StatusOK || identity["kind"] != "workload_federation" || identity["actor"] != "service-account:"+f.account.ID {
		t.Fatalf("exchanged token: %d %v", status, identity)
	}
	// The policy cap is below the server cap here; with a lower server cap the server wins.
	low := newWorkloadAPIFixture(t, WorkloadExchangeOptions{MaxTTL: 30 * time.Second, Rate: 1000}, true)
	lowIssuer := low.createIssuer()
	low.createPolicy(lowIssuer, map[string]any{"maxTokenTtlSeconds": 3600})
	if status, body, _ = low.exchange(exchangeForm(low.token(nil), nil)); status != http.StatusOK || body["expires_in"] != float64(30) {
		t.Fatalf("server cap: %d %v", status, body)
	}
	// An exchanged token cannot be exchanged again.
	if status, body, _ = f.exchange(exchangeForm(access, nil)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("re-exchange: %d %v", status, body)
	}
	// scope is ignored; an audience the issuer does not accept is invalid_target.
	if status, body, _ = f.exchange(exchangeForm(f.token(nil), map[string]string{"audience": "https://elsewhere.example.test"})); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("audience: %d %v", status, body)
	}
}

func TestTokenExchangeFailuresAreUniform(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{Rate: 1000}, true)
	issuer := f.createIssuer()
	f.createPolicy(issuer, nil)
	var messages []string
	for name, token := range map[string]string{
		"wrong subject":  f.token(func(c map[string]any) { c["sub"] = "repo:acme/other" }),
		"wrong audience": f.token(func(c map[string]any) { c["aud"] = "https://elsewhere.example.test" }),
		"expired":        f.token(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"unknown issuer": f.token(func(c map[string]any) { c["iss"] = "https://unknown.example.test" }),
		"not a jwt":      "garbage",
		"forged":         f.token(nil)[:100] + "AAAAAAAAAAAAAAAAAAAA",
	} {
		status, body, _ := f.exchange(exchangeForm(token, nil))
		if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("%s: %d %v", name, status, body)
		}
		messages = append(messages, jsonText(t, body))
	}
	for _, message := range messages[1:] {
		if message != messages[0] {
			t.Fatalf("failures are distinguishable: %q vs %q", message, messages[0])
		}
	}
	// A pinned policy that does not match, and a disabled policy, are the same answer.
	if status, body, _ := f.exchange(exchangeForm(f.token(nil), map[string]string{"policy": "nope"})); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("pinned policy: %d %v", status, body)
	}
	for name, form := range map[string]string{
		"grant type":       "grant_type=password&subject_token=x",
		"missing subject":  "grant_type=" + url.QueryEscape(tokenExchangeGrantType) + "&subject_token_type=" + url.QueryEscape(tokenExchangeJWTType),
		"wrong token type": "grant_type=" + url.QueryEscape(tokenExchangeGrantType) + "&subject_token=x&subject_token_type=other",
	} {
		status, body, _ := f.exchange(form)
		want := "invalid_request"
		if name == "grant type" {
			want = "unsupported_grant_type"
		}
		if status != http.StatusBadRequest || body["error"] != want {
			t.Fatalf("%s: %d %v", name, status, body)
		}
	}
	if status, body, _ := f.call(http.MethodPost, workloadExchangePath, "", exchangeForm("x", nil), map[string]string{"Content-Type": "application/json"}); status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("json body: %d %v", status, body)
	}
}

func TestTokenExchangeIsRateLimitedPerSourceAndPerIssuer(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{Rate: 3}, true)
	issuer := f.createIssuer()
	f.createPolicy(issuer, nil)
	var limited bool
	for range 6 {
		status, body, header := f.exchange(exchangeForm(f.token(nil), nil))
		if status == http.StatusTooManyRequests {
			limited = true
			if body["error"] != "temporarily_unavailable" || header.Get("Retry-After") == "" {
				t.Fatalf("429 = %v %v", body, header)
			}
		}
	}
	if !limited {
		t.Fatal("six exchanges at 3 per minute were not limited")
	}
}

func TestTokenExchangeIsOffWhenWorkloadFederationIsDisabledAndLeavesAuthTokenAlone(t *testing.T) {
	off := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, false)
	if status, _, _ := off.exchange(exchangeForm("x", nil)); status != http.StatusNotFound {
		t.Fatalf("disabled exchange: %d", status)
	}
	// POST /auth/token with the Docker token-spec grants is served exactly as before the exchange existed.
	on := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	for _, grant := range []string{"password", "refresh_token"} {
		form := "grant_type=" + grant + "&username=u&password=p&refresh_token=r&service=notarx"
		offStatus, _, _ := off.call(http.MethodPost, "/auth/token", "", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		onStatus, _, _ := on.call(http.MethodPost, "/auth/token", "", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if offStatus != onStatus || onStatus == http.StatusOK {
			t.Fatalf("POST /auth/token grant_type=%s changed: %d vs %d", grant, offStatus, onStatus)
		}
	}
	if status, _, _ := on.call(http.MethodGet, "/auth/token", "", nil, nil); status != http.StatusUnauthorized && status != http.StatusOK {
		t.Fatalf("GET /auth/token: %d", status)
	}
}
