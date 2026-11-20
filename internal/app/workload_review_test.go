package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

func TestSingleUseTokenIsNotBurnedByARefusedExchange(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{Rate: 1000}, true)
	issuer := f.createIssuer()
	status, policy := f.createPolicy(issuer, map[string]any{"singleUse": true, "sampleToken": f.token(nil)})
	if status != http.StatusCreated {
		t.Fatalf("policy: %d %v", status, policy)
	}
	token := f.token(nil)
	// A wrong audience is refused after the token verified, and must leave the jti unconsumed.
	if status, body, _ := f.exchange(exchangeForm(token, map[string]string{"audience": "https://elsewhere.example.test"})); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("audience: %d %v", status, body)
	}
	if status, body, _ := f.exchange(exchangeForm(token, map[string]string{"audience": workloadTestAudience})); status != http.StatusOK {
		t.Fatalf("the corrected retry was refused: %d %v", status, body)
	}
	if status, body, _ := f.exchange(exchangeForm(token, nil)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("a replay of the exchanged token: %d %v", status, body)
	}
}

func TestForgedTokensDoNotDrainTheIssuerBudget(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{Rate: 3}, true)
	issuer := f.createIssuer()
	f.createPolicy(issuer, nil)
	forged := f.token(nil)
	forged = forged[:len(forged)-20] + "AAAAAAAAAAAAAAAAAAAA"
	send := func(address, subject string) int {
		request := httptest.NewRequest(http.MethodPost, workloadExchangePath, strings.NewReader(exchangeForm(subject, nil)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = address
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		return response.Code
	}
	// An attacker naming the registered issuer from many addresses charges nothing to the issuer.
	for i := range 20 {
		if code := send(fmt.Sprintf("198.51.100.%d:4000", i+1), forged); code != http.StatusBadRequest {
			t.Fatalf("forged token %d: %d", i, code)
		}
	}
	for i := range 3 {
		if code := send(fmt.Sprintf("203.0.113.%d:4000", i+1), f.token(nil)); code != http.StatusOK {
			t.Fatalf("legitimate exchange %d was limited: %d", i, code)
		}
	}
	// Verified tokens do spend the issuer budget.
	if code := send("203.0.113.200:4000", f.token(nil)); code != http.StatusTooManyRequests {
		t.Fatalf("the per-issuer budget did not apply to verified tokens: %d", code)
	}
}

func TestWorkloadFailureAuditIsThrottledAndSuccessIsNot(t *testing.T) {
	metrics, audit := &Metrics{}, &auditSink{}
	observer := NewWorkloadObserver(metrics, audit)
	now := time.Unix(1_800_000_000, 0)
	observer.Now = func() time.Time { return now }
	for range 500 {
		observer.ObserveWorkload(context.Background(), authorization.WorkloadEvent{Reason: authorization.WorkloadVerifyFailed, Issuer: "https://ci.example.test"})
	}
	for range 5 {
		observer.ObserveWorkload(context.Background(), authorization.WorkloadEvent{Success: true, Reason: authorization.WorkloadAuthenticated, Issuer: "https://ci.example.test"})
	}
	var failures, successes int
	for _, record := range audit.snapshot() {
		if record.Outcome == repository.AuditResolved {
			successes++
		} else {
			failures++
		}
	}
	if failures > workloadFailureAuditBurst+1 || failures < workloadFailureAuditBurst || successes != 5 {
		t.Fatalf("failure records = %d, success records = %d", failures, successes)
	}
	if got := metrics.workload.suppressed.Load(); got != uint64(500-failures) {
		t.Fatalf("suppressed = %d, want %d", got, 500-failures)
	}
	if got := metrics.workload.counts[1].Load() + metrics.workload.counts[2].Load(); got != 500 {
		t.Fatalf("every failure must still be counted, got %d", got)
	}
	// Another issuer has its own budget.
	before := len(audit.snapshot())
	observer.ObserveWorkload(context.Background(), authorization.WorkloadEvent{Reason: authorization.WorkloadVerifyFailed, Issuer: "https://other.example.test"})
	if len(audit.snapshot()) != before+1 {
		t.Fatal("a second issuer shares the throttle")
	}
}

func TestReenablingAPolicyRechecksTheAdministratorMapping(t *testing.T) {
	f := newWorkloadAPIFixture(t, WorkloadExchangeOptions{}, true)
	issuer := f.createIssuer()
	status, policy := f.createPolicy(issuer, nil)
	if status != http.StatusCreated {
		t.Fatalf("policy: %d %v", status, policy)
	}
	id := policy["id"].(string)
	status, disabled := f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"state": "disabled"}, map[string]string{"If-Match": policy["version"].(string)})
	if status != http.StatusOK {
		t.Fatalf("disable: %d %v", status, disabled)
	}
	// While disabled, the account gains an administrator grant.
	repo, err := f.store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "team", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReplaceRepositoryGrants(context.Background(), repo.ID, []repository.RepositoryGrant{{Principal: "service-account:" + f.account.ID, Scopes: []string{"repositories:admin"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	version := disabled["version"].(string)
	for name, body := range map[string]map[string]any{
		"plain":                   {"state": "active"},
		"allowAdmin false":        {"state": "active", "allowAdmin": false},
		"same service account id": {"state": "active", "serviceAccountId": f.account.ID},
	} {
		if status, got := f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, body, map[string]string{"If-Match": version}); status != http.StatusBadRequest || got["code"] != "admin_mapping_requires_allow_admin" {
			t.Fatalf("%s: %d %v", name, status, got)
		}
	}
	if status, got := f.admin(http.MethodPatch, "/api/v2/workload-identity/policies/"+id, map[string]any{"state": "active", "allowAdmin": true}, map[string]string{"If-Match": version}); status != http.StatusOK {
		t.Fatalf("acknowledged re-enable: %d %v", status, got)
	}
}

func TestIdentityProviderCannotUseAWorkloadIssuerURL(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	if _, err := store.CreateWorkloadIssuer(context.Background(), repository.WorkloadIssuer{
		Name: "ci", Kind: repository.WorkloadKindGeneric, Issuer: "https://ci.example.test", Audiences: []string{"a"},
		JWKSMode: repository.WorkloadJWKSDiscovery,
	}); err != nil {
		t.Fatal(err)
	}
	provider := discoveryServer(t)
	if _, err := store.CreateWorkloadIssuer(context.Background(), repository.WorkloadIssuer{
		Name: "local", Kind: repository.WorkloadKindGeneric, Issuer: strings.Replace(provider.URL, "http://", "https://", 1), Audiences: []string{"a"},
		JWKSMode: repository.WorkloadJWKSDiscovery,
	}); err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: NewIdentityRuntime(store)}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	create := func(issuer string) int {
		body := `{"slug":"corp","kind":"oidc","displayName":"Corp","enabled":true,"oidc":{"issuer":"` + issuer + `","audience":"notarx-api"}}`
		request := httptest.NewRequest(http.MethodPost, "/api/v2/authentication/providers", strings.NewReader(body))
		authorize(request, "admin-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	for _, issuer := range []string{"https://ci.example.test", "https://ci.example.test/"} {
		if code := create(issuer); code != http.StatusConflict {
			t.Fatalf("provider on workload issuer %q: %d", issuer, code)
		}
	}
	if code := create(provider.URL); code != http.StatusCreated {
		t.Fatalf("a provider of another issuer: %d", code)
	}
}
