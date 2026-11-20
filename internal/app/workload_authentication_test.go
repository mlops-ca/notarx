package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
)

type auditSink struct {
	mu      sync.Mutex
	records []repository.AuditRecord
}

func (s *auditSink) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *auditSink) snapshot() []repository.AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]repository.AuditRecord(nil), s.records...)
}

func TestWorkloadFederationLoginAndRevalidation(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
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
	jwks, err := jose.MarshalJWKS(public)
	if err != nil {
		t.Fatal(err)
	}
	const issuerURL, audience = "https://ci.example.test", "https://notarx.example.test"
	issuer, err := store.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{
		Name: "ci", Kind: repository.WorkloadKindGeneric, Issuer: issuerURL, Audiences: []string{audience},
		AllowedAlgs: []string{jose.ES256}, JWKSMode: repository.WorkloadJWKSStatic, StaticJWKS: jwks,
	})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "publisher"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "publish", IssuerID: issuer.ID, ServiceAccountID: account.ID,
		Conditions: []byte(`[{"claim":["sub"],"op":"eq","values":["repo:acme/app"]}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := jose.NewVerifier(jose.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	metrics, audit := &Metrics{}, &auditSink{}
	workload, err := authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{
		Store: store, Accounts: store, Verifier: verifier, ClockSkew: time.Minute, Observer: WorkloadObserver{Metrics: metrics, Audit: audit},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: Authenticator{ResolverToken: "principal-token-signing-secret", Workload: workload}})
	now := time.Now()
	jwt, err := signer.Sign(map[string]any{"iss": issuerURL, "sub": "repo:acme/app", "aud": audience, "jti": "run-1", "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	identity := func(token string) (int, struct{ Actor, Kind string }) {
		request := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body struct{ Actor, Kind string }
		_ = json.NewDecoder(response.Body).Decode(&body)
		return response.Code, body
	}
	login := func(username, password string) (int, string) {
		request := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
		request.SetBasicAuth(username, password)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var issued struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(response.Body).Decode(&issued)
		return response.Code, issued.Token
	}

	if code, body := identity(jwt); code != http.StatusOK || body.Actor != "service-account:"+account.ID || body.Kind != "workload_federation" {
		t.Fatalf("bearer workload JWT: %d %+v", code, body)
	}
	code, token := login("oidc", jwt)
	if code != http.StatusOK || token == "" || strings.Count(token, ".") != 3 {
		t.Fatalf("docker login with the workload JWT: %d %q", code, token)
	}
	if code, body := identity(token); code != http.StatusOK || body.Kind != "workload_federation" {
		t.Fatalf("issued principal token: %d %+v", code, body)
	}
	if code, _ := login("publish", jwt); code != http.StatusOK {
		t.Fatalf("policy-name username: %d", code)
	}
	if code, _ := login("unknown-policy", jwt); code == http.StatusOK {
		t.Fatal("a username that names no policy logged in")
	}

	policy.State = repository.WorkloadDisabled
	if _, err := store.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	if code, _ := identity(token); code != http.StatusUnauthorized {
		t.Fatalf("an issued token outlived its disabled policy: %d", code)
	}
	if code, _ := login("oidc", jwt); code == http.StatusOK {
		t.Fatal("a disabled policy still logged in")
	}

	records := audit.snapshot()
	var succeeded, failed int
	for _, record := range records {
		if strings.Contains(record.Evidence["detail"]+record.Resource+record.Actor, jwt) {
			t.Fatal("an audit record carries the raw token")
		}
		if record.Operation != "workload.authenticate" || record.Evidence["issuer"] != issuerURL {
			t.Fatalf("record = %+v", record)
		}
		if record.Outcome == repository.AuditResolved {
			succeeded++
			if record.Evidence["policyId"] != policy.ID || record.Evidence["serviceAccount"] != account.ID || record.Evidence["jtiHash"] == "" || record.Evidence["subject"] != "repo:acme/app" || record.Evidence["claim.[\"sub\"]"] != "repo:acme/app" {
				t.Fatalf("success evidence = %+v", record.Evidence)
			}
		} else {
			failed++
			if record.Class != repository.AuditClassSync || record.Status != http.StatusUnauthorized {
				t.Fatalf("failure record = %+v", record)
			}
		}
	}
	if succeeded == 0 || failed == 0 {
		t.Fatalf("audit: %d successes, %d failures", succeeded, failed)
	}
	rendered := httptest.NewRecorder()
	metrics.writeWorkloadMetrics(rendered)
	if !strings.Contains(rendered.Body.String(), `notarx_workload_auth_total{outcome="authenticated"}`) || strings.Contains(rendered.Body.String(), `outcome="authenticated"} 0`) {
		t.Fatalf("metrics = %s", rendered.Body.String())
	}
}

func TestWorkloadCountersBoundTheLabelSet(t *testing.T) {
	var counters workloadCounters
	counters.record("not-a-known-reason")
	if counters.counts[len(workloadReasons)-1].Load() != 1 {
		t.Fatal("an unknown reason must be counted as other")
	}
}
