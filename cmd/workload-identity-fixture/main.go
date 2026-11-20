// workload-identity-fixture is the self-contained fixture of "make workload-identity-e2e" (ADR 0019). It runs
// a notarx gateway on an in-memory store, with workload federation on, next to two mock token issuers:
//
//   - a GitHub-Actions-style issuer over HTTPS with OIDC discovery, a JWKS and a token endpoint shaped like
//     ACTIONS_ID_TOKEN_REQUEST_URL (GET /token?audience=...), signed with a throwaway key and a self-signed
//     certificate whose CA it writes to the work directory;
//   - a Kubernetes-style issuer that is only a static JWKS file and a projected service account token, the
//     way a private API server is registered (kubectl get --raw /openid/v1/jwks).
//
// Deployment uses PostgreSQL; this process exists so the client-visible flows (docker login, token exchange,
// notarxctl login) run against the real HTTP stack without containers.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/app"
	"github.com/mlops-ca/notarx/internal/apptest"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	adminToken         = "fixture-admin"
	requestToken       = "fixture-actions-request-token"
	kubernetesIssuer   = "https://kubernetes.default.svc.cluster.local"
	kubernetesSubject  = "system:serviceaccount:team-a:builder"
	githubRepositoryID = "1296269"
)

func main() {
	workdir := os.Getenv("FIXTURE_DIR")
	gatewayAddress := envOr("LISTEN_ADDR", "127.0.0.1:18082")
	issuerAddress := envOr("ISSUER_ADDR", "127.0.0.1:18083")
	if workdir == "" {
		log.Fatal("FIXTURE_DIR is required")
	}
	githubIssuer := "https://" + issuerAddress
	githubSigner, githubJWKS := newSigner("github-key")
	kubernetesSigner, kubernetesJWKS := newSigner("kubernetes-key")

	ca, certificate := selfSigned()
	must(os.WriteFile(filepath.Join(workdir, "ca.pem"), ca, 0o600))
	must(os.WriteFile(filepath.Join(workdir, "issuer.url"), []byte(githubIssuer), 0o600))
	must(os.WriteFile(filepath.Join(workdir, "k8s-jwks.json"), kubernetesJWKS, 0o600))
	must(os.MkdirAll(filepath.Join(workdir, "k8s"), 0o700))
	audience := os.Getenv("NOTARX_AUDIENCE")
	if audience == "" {
		audience = "http://" + gatewayAddress
	}
	// The projected service account token of a pod: aud, a 10 minute lifetime and the kubernetes.io claim.
	now := time.Now()
	projected, err := kubernetesSigner.Sign(map[string]any{
		"iss": kubernetesIssuer, "sub": kubernetesSubject, "aud": []string{audience}, "jti": uuid.NewString(),
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"kubernetes.io": map[string]any{
			"namespace": "team-a", "serviceaccount": map[string]any{"name": "builder", "uid": uuid.NewString()},
			"pod": map[string]any{"name": "builder-7d9f6c-x2", "uid": uuid.NewString()},
		},
	})
	must(err)
	must(os.WriteFile(filepath.Join(workdir, "k8s", "token"), []byte(projected), 0o600))

	go serveGitHubIssuer(issuerAddress, githubIssuer, githubSigner, githubJWKS, certificate)

	store := repository.NewMemoryStore()
	if _, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "team", Format: repository.FormatOCI}); err != nil {
		log.Fatal(err)
	}
	verifier, err := jose.NewVerifier(jose.DefaultOptions())
	must(err)
	workload, err := authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{Store: store, Accounts: store, Verifier: verifier, ClockSkew: time.Minute})
	must(err)
	authenticator := app.Authenticator{AdminToken: adminToken, AdminActor: "admin", ResolverToken: "fixture-secret", Workload: workload}
	handler := apptest.NewGateway(log.Default(), app.GatewayOptions{
		Store: store, NPMMetadata: store, NPMRestore: store, Storage: apptest.MemoryObjectStores(nil), Adapter: app.TestAdapter{},
		Authenticator: authenticator, WorkloadExchange: app.WorkloadExchangeOptions{MaxTTL: 15 * time.Minute, Rate: 600},
	}, app.NewGatewayHandler)
	log.Printf("workload identity fixture: gateway on %s, GitHub-style issuer on %s", gatewayAddress, githubIssuer)
	log.Fatal(http.ListenAndServe(gatewayAddress, handler))
}

func serveGitHubIssuer(address, issuer string, signer *jose.Signer, jwks []byte, certificate tls.Certificate) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/.well-known/jwks"})
	})
	mux.HandleFunc("GET /.well-known/jwks", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) })
	// ACTIONS_ID_TOKEN_REQUEST_URL: GET with the request token and an audience answers {"value": "<jwt>"}.
	// repository_id and environment may be overridden to mint the token of a workflow the policy must refuse.
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer "+requestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		repositoryID, environment := githubRepositoryID, "packages-publish"
		if value := r.URL.Query().Get("repository_id"); value != "" {
			repositoryID = value
		}
		if value := r.URL.Query().Get("environment"); value != "" {
			environment = value
		}
		now := time.Now()
		token, err := signer.Sign(map[string]any{
			"iss": issuer, "sub": "repo:acme/app:environment:" + environment, "aud": r.URL.Query().Get("audience"), "jti": uuid.NewString(),
			"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
			"repository": "acme/app", "repository_id": repositoryID, "repository_owner": "acme", "repository_owner_id": "9919",
			"ref": "refs/heads/main", "ref_type": "branch", "environment": environment, "event_name": "push",
			"job_workflow_ref": "acme/app/.github/workflows/release.yml@refs/heads/main", "run_attempt": "1",
		})
		if err != nil {
			http.Error(w, "sign failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"value": token})
	})
	listener, err := tls.Listen("tcp", address, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	must(err)
	log.Fatal((&http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}).Serve(listener))
}

func newSigner(kid string) (*jose.Signer, []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	signer, err := jose.NewSigner(jose.ES256, key, kid)
	must(err)
	public, err := jose.PublicKey(key.Public(), kid, jose.ES256)
	must(err)
	doc, err := jose.MarshalJWKS(public)
	must(err)
	return signer, doc
}

// selfSigned returns the PEM of a throwaway CA certificate for 127.0.0.1 and the certificate to serve it with.
func selfSigned() ([]byte, tls.Certificate) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "workload-identity-fixture"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	must(err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
