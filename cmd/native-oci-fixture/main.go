// native-oci-fixture starts a self-contained Registry V2 endpoint for the
// protocol E2E fixture. Deployment uses PostgreSQL and S3-compatible storage; this process
// exists solely to exercise client-visible HTTP behaviour without an external
// package service.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/app"
	"github.com/mlops-ca/notarx/internal/apptest"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	workloadIssuerURL = "https://token.actions.fixture.test"
	workloadAudience  = "https://notarx.fixture.test"
	workloadSubject   = "repo:acme/app:ref:refs/heads/main"
)

func main() {
	store := repository.NewMemoryStore()
	if _, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "team", Format: repository.FormatOCI}); err != nil {
		log.Fatal(err)
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:18081"
	}
	authenticator := app.Authenticator{ResolverToken: "fixture-secret"}
	// WORKLOAD_TOKEN_FILE turns on the workload identity fixture of ADR 0019: an issuer with a static
	// JWKS (no network), a trust policy and a signed token, which the E2E script uses as a docker login password.
	if path := os.Getenv("WORKLOAD_TOKEN_FILE"); path != "" {
		workload, err := workloadFixture(store, path)
		if err != nil {
			log.Fatal(err)
		}
		authenticator.Workload = workload
	}
	handler := apptest.NewGateway(log.Default(), app.GatewayOptions{Store: store, NPMMetadata: store, NPMRestore: store, Storage: apptest.MemoryObjectStores(nil), Adapter: app.TestAdapter{}, Authenticator: authenticator}, app.NewGatewayHandler)
	log.Printf("Native OCI fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, handler))
}

// workloadFixture registers the issuer, service account and policy "publish" and writes a token that
// the policy accepts to path. A second policy, "deploy", matches a different subject, so the E2E script
// can pin the wrong policy by username.
func workloadFixture(store *repository.MemoryStore, path string) (*authorization.WorkloadAuthenticator, error) {
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(jose.ES256, key, "fixture-key")
	if err != nil {
		return nil, err
	}
	public, err := jose.PublicKey(key.Public(), "fixture-key", jose.ES256)
	if err != nil {
		return nil, err
	}
	jwks, err := jose.MarshalJWKS(public)
	if err != nil {
		return nil, err
	}
	issuer, err := store.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{
		Name: "fixture", Kind: repository.WorkloadKindGeneric, Issuer: workloadIssuerURL, Audiences: []string{workloadAudience},
		AllowedAlgs: []string{jose.ES256}, JWKSMode: repository.WorkloadJWKSStatic, StaticJWKS: jwks,
	})
	if err != nil {
		return nil, err
	}
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "feeds-publisher"})
	if err != nil {
		return nil, err
	}
	for name, subject := range map[string]string{"publish": workloadSubject, "deploy": "repo:acme/other"} {
		condition := fmt.Sprintf(`[{"claim":["sub"],"op":"eq","values":[%q]}]`, subject)
		if _, err := store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{Name: name, IssuerID: issuer.ID, ServiceAccountID: account.ID, Conditions: []byte(condition)}); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	token, err := signer.Sign(map[string]any{
		"iss": workloadIssuerURL, "sub": workloadSubject, "aud": workloadAudience, "jti": uuid.NewString(),
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return nil, err
	}
	verifier, err := jose.NewVerifier(jose.DefaultOptions())
	if err != nil {
		return nil, err
	}
	return authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{Store: store, Accounts: store, Verifier: verifier, ClockSkew: time.Minute})
}
