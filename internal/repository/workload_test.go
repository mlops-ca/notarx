package repository

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/jose"
)

type workloadContractStore interface {
	WorkloadStore
	ServiceAccountStore
}

func TestWorkloadMemoryStoreContract(t *testing.T) { testWorkloadStoreContract(t, NewMemoryStore()) }

func workloadSuffix() string { return strings.ReplaceAll(ids.New(), "-", "")[20:] }

func staticJWKS(t testing.TB) json.RawMessage {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := jose.PublicKey(key.Public(), "", jose.ES256)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jose.MarshalJWKS(jwk)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func newWorkloadIssuer(suffix string) WorkloadIssuer {
	return WorkloadIssuer{
		Name: "gh-" + suffix, Kind: WorkloadKindGitHubActions, Issuer: "https://token.actions.example.test/" + suffix,
		Audiences: []string{"https://notarx.example.test"}, JWKSMode: WorkloadJWKSDiscovery,
	}
}

const workloadConditions = `[{"claim":["repository_id"],"op":"eq","values":["1296269"]},{"claim":["ref"],"op":"eq","values":["refs/heads/main"]}]`

func testWorkloadStoreContract(t *testing.T, store workloadContractStore) {
	t.Helper()
	ctx := t.Context()
	suffix := workloadSuffix()
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: ids.New(), Name: "wl-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: ids.New(), Name: "wl2-" + suffix})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("issuer validation", func(t *testing.T) {
		valid := newWorkloadIssuer(suffix + "-v")
		mutations := map[string]func(*WorkloadIssuer){
			"bad name":       func(i *WorkloadIssuer) { i.Name = "Not A Slug" },
			"unknown kind":   func(i *WorkloadIssuer) { i.Kind = "ldap" },
			"http issuer":    func(i *WorkloadIssuer) { i.Issuer = "http://token.example.test" },
			"no audience":    func(i *WorkloadIssuer) { i.Audiences = nil },
			"empty audience": func(i *WorkloadIssuer) { i.Audiences = []string{" "} },
			"hmac algorithm": func(i *WorkloadIssuer) { i.AllowedAlgs = []string{"HS256"} },
			"unknown mode":   func(i *WorkloadIssuer) { i.JWKSMode = "inline" },
			"static without": func(i *WorkloadIssuer) { i.JWKSMode = WorkloadJWKSStatic },
			"static with uri": func(i *WorkloadIssuer) {
				i.JWKSMode, i.StaticJWKS, i.JWKSURI = WorkloadJWKSStatic, staticJWKS(t), "https://x.example.test/jwks"
			},
			"discovery static": func(i *WorkloadIssuer) { i.StaticJWKS = staticJWKS(t) },
			"http jwks uri":    func(i *WorkloadIssuer) { i.JWKSURI = "http://x.example.test/jwks" },
			"bad ca":           func(i *WorkloadIssuer) { i.CABundlePEM = "nope" },
			"unknown state":    func(i *WorkloadIssuer) { i.State = "paused" },
			"too many audiences": func(i *WorkloadIssuer) {
				i.Audiences = make([]string, 17)
				for n := range i.Audiences {
					i.Audiences[n] = fmt.Sprint("a", n)
				}
			},
		}
		for name, mutate := range mutations {
			candidate := valid
			mutate(&candidate)
			if _, err := store.CreateWorkloadIssuer(ctx, candidate); !errors.Is(err, ErrWorkloadInvalid) {
				t.Errorf("%s: err = %v, want ErrWorkloadInvalid", name, err)
			}
		}
	})

	issuer, err := store.CreateWorkloadIssuer(ctx, newWorkloadIssuer(suffix))
	if err != nil {
		t.Fatal(err)
	}
	if issuer.ID == "" || issuer.Version != "1" || issuer.State != WorkloadActive || len(issuer.AllowedAlgs) != 3 || issuer.CreatedAt.IsZero() {
		t.Fatalf("created issuer = %+v", issuer)
	}
	t.Run("issuer uniqueness and lookup", func(t *testing.T) {
		duplicate := newWorkloadIssuer(suffix + "-other")
		duplicate.Name = issuer.Name
		if _, err := store.CreateWorkloadIssuer(ctx, duplicate); !errors.Is(err, ErrNameExists) {
			t.Fatalf("duplicate name err = %v", err)
		}
		duplicate = newWorkloadIssuer(suffix + "-other")
		duplicate.Issuer = issuer.Issuer
		if _, err := store.CreateWorkloadIssuer(ctx, duplicate); !errors.Is(err, ErrWorkloadIssuerExists) {
			t.Fatalf("duplicate issuer err = %v", err)
		}
		byID, err := store.GetWorkloadIssuer(ctx, issuer.ID)
		if err != nil || byID.Issuer != issuer.Issuer || len(byID.Audiences) != 1 {
			t.Fatalf("GetWorkloadIssuer = %+v, %v", byID, err)
		}
		byIss, err := store.GetWorkloadIssuerByIssuer(ctx, issuer.Issuer)
		if err != nil || byIss.ID != issuer.ID {
			t.Fatalf("GetWorkloadIssuerByIssuer = %+v, %v", byIss, err)
		}
		for _, missing := range []string{ids.New(), "not-a-uuid"} {
			if _, err := store.GetWorkloadIssuer(ctx, missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get(%q) err = %v", missing, err)
			}
		}
		if _, err := store.GetWorkloadIssuerByIssuer(ctx, "https://unknown.example.test"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown iss err = %v", err)
		}
	})

	t.Run("issuer update needs the current version", func(t *testing.T) {
		changed := issuer
		changed.AllowPrivateNetwork = true
		changed.Audiences = []string{"https://notarx.example.test", "https://other.example.test"}
		changed.JWKSMode, changed.StaticJWKS = WorkloadJWKSStatic, staticJWKS(t)
		updated, err := store.UpdateWorkloadIssuer(ctx, changed, issuer.Version)
		if err != nil || updated.Version != "2" || !updated.AllowPrivateNetwork || updated.JWKSMode != WorkloadJWKSStatic || len(updated.Audiences) != 2 || updated.StaticJWKS == nil {
			t.Fatalf("update = %+v, %v", updated, err)
		}
		if _, err := store.UpdateWorkloadIssuer(ctx, changed, issuer.Version); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("stale update err = %v", err)
		}
		if _, err := store.UpdateWorkloadIssuer(ctx, changed, "garbage"); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("garbage version err = %v", err)
		}
		missing := changed
		missing.ID = ids.New()
		if _, err := store.UpdateWorkloadIssuer(ctx, missing, "1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing update err = %v", err)
		}
		back := updated
		back.JWKSMode, back.StaticJWKS = WorkloadJWKSDiscovery, nil
		restored, err := store.UpdateWorkloadIssuer(ctx, back, updated.Version)
		if err != nil || restored.Version != "3" || len(restored.StaticJWKS) != 0 {
			t.Fatalf("restore = %+v, %v", restored, err)
		}
		issuer = restored
	})

	var policy WorkloadTrustPolicy
	t.Run("policy validation and references", func(t *testing.T) {
		valid := WorkloadTrustPolicy{Name: "p-" + suffix, IssuerID: issuer.ID, ServiceAccountID: account.ID, Conditions: json.RawMessage(workloadConditions)}
		mutations := map[string]func(*WorkloadTrustPolicy){
			"bad name":          func(p *WorkloadTrustPolicy) { p.Name = "UPPER" },
			"reserved name":     func(p *WorkloadTrustPolicy) { p.Name = WorkloadBasicUsernameAlias },
			"bad issuer id":     func(p *WorkloadTrustPolicy) { p.IssuerID = "x" },
			"bad account id":    func(p *WorkloadTrustPolicy) { p.ServiceAccountID = "x" },
			"no conditions":     func(p *WorkloadTrustPolicy) { p.Conditions = json.RawMessage("[]") },
			"conditions object": func(p *WorkloadTrustPolicy) { p.Conditions = json.RawMessage("{}") },
			"ttl too large":     func(p *WorkloadTrustPolicy) { p.MaxTokenTTLSeconds = 86401 },
			"negative ttl":      func(p *WorkloadTrustPolicy) { p.MaxTokenTTLSeconds = -1 },
			"unknown state":     func(p *WorkloadTrustPolicy) { p.State = "paused" },
		}
		for name, mutate := range mutations {
			candidate := valid
			mutate(&candidate)
			if _, err := store.CreateWorkloadTrustPolicy(ctx, candidate); !errors.Is(err, ErrWorkloadInvalid) {
				t.Errorf("%s: err = %v, want ErrWorkloadInvalid", name, err)
			}
		}
		for name, mutate := range map[string]func(*WorkloadTrustPolicy){
			"unknown issuer":  func(p *WorkloadTrustPolicy) { p.IssuerID = ids.New() },
			"unknown account": func(p *WorkloadTrustPolicy) { p.ServiceAccountID = ids.New() },
		} {
			candidate := valid
			mutate(&candidate)
			if _, err := store.CreateWorkloadTrustPolicy(ctx, candidate); !errors.Is(err, ErrWorkloadReference) {
				t.Errorf("%s: err = %v, want ErrWorkloadReference", name, err)
			}
		}
		created, err := store.CreateWorkloadTrustPolicy(ctx, valid)
		if err != nil {
			t.Fatal(err)
		}
		if created.ID == "" || created.Version != "1" || created.State != WorkloadActive || created.MaxTokenTTLSeconds != DefaultWorkloadTokenTTLSeconds || created.SingleUse {
			t.Fatalf("created policy = %+v", created)
		}
		var conditions []map[string]any
		if err := json.Unmarshal(created.Conditions, &conditions); err != nil || len(conditions) != 2 {
			t.Fatalf("conditions round trip = %s, %v", created.Conditions, err)
		}
		if _, err := store.CreateWorkloadTrustPolicy(ctx, valid); !errors.Is(err, ErrNameExists) {
			t.Fatalf("duplicate policy name err = %v", err)
		}
		policy = created
	})

	t.Run("many policies map to one service account", func(t *testing.T) {
		now := time.Now().UTC()
		past, future := now.Add(-time.Hour), now.Add(time.Hour)
		for name, spec := range map[string]struct {
			state   WorkloadState
			expires *time.Time
			account string
		}{
			"b-" + suffix: {WorkloadActive, nil, account.ID},
			"a-" + suffix: {WorkloadActive, &future, account.ID},
			"c-" + suffix: {WorkloadActive, &past, account.ID},
			"d-" + suffix: {WorkloadDisabled, nil, account.ID},
			"e-" + suffix: {WorkloadActive, nil, other.ID},
		} {
			if _, err := store.CreateWorkloadTrustPolicy(ctx, WorkloadTrustPolicy{Name: name, IssuerID: issuer.ID, ServiceAccountID: spec.account,
				Conditions: json.RawMessage(workloadConditions), State: spec.state, ExpiresAt: spec.expires, SingleUse: true, MaxTokenTTLSeconds: 60}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		active, err := store.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(active))
		for _, candidate := range active {
			names = append(names, strings.TrimSuffix(candidate.Name, "-"+suffix))
		}
		if got := strings.Join(names, ","); got != "a,b,e,p" {
			t.Fatalf("active policies = %s, want a,b,e,p (disabled and expired are excluded, ordered by name)", got)
		}
		if active[0].ExpiresAt == nil || !active[0].SingleUse || active[0].MaxTokenTTLSeconds != 60 {
			t.Fatalf("policy fields lost: %+v", active[0])
		}
		if none, err := store.ListActiveWorkloadTrustPolicies(ctx, ids.New(), now); err != nil || len(none) != 0 {
			t.Fatalf("other issuer policies = %v, %v", none, err)
		}
	})

	t.Run("policy list paging and lookup", func(t *testing.T) {
		first, err := store.ListWorkloadTrustPolicies(ctx, 2, "")
		if err != nil || len(first) != 2 || first[0].ID >= first[1].ID {
			t.Fatalf("first page = %v, %v", first, err)
		}
		next, err := store.ListWorkloadTrustPolicies(ctx, 100, first[1].ID)
		if err != nil || len(next) == 0 || next[0].ID <= first[1].ID {
			t.Fatalf("next page = %v, %v", next, err)
		}
		listed, err := store.ListWorkloadIssuers(ctx, 100, "")
		if err != nil || len(listed) == 0 {
			t.Fatalf("issuers = %v, %v", listed, err)
		}
		if page, err := store.ListWorkloadIssuers(ctx, 1, listed[0].ID); err != nil || (len(page) == 1 && page[0].ID <= listed[0].ID) {
			t.Fatalf("issuer page = %v, %v", page, err)
		}
		got, err := store.GetWorkloadTrustPolicy(ctx, policy.ID)
		if err != nil || got.Name != policy.Name {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		for _, missing := range []string{ids.New(), "nope"} {
			if _, err := store.GetWorkloadTrustPolicy(ctx, missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get(%q) err = %v", missing, err)
			}
		}
	})

	t.Run("policy update needs the current version", func(t *testing.T) {
		changed := policy
		changed.SingleUse, changed.State, changed.ServiceAccountID = true, WorkloadDisabled, other.ID
		updated, err := store.UpdateWorkloadTrustPolicy(ctx, changed, policy.Version)
		if err != nil || updated.Version != "2" || !updated.SingleUse || updated.State != WorkloadDisabled || updated.ServiceAccountID != other.ID {
			t.Fatalf("update = %+v, %v", updated, err)
		}
		if _, err := store.UpdateWorkloadTrustPolicy(ctx, changed, policy.Version); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("stale update err = %v", err)
		}
		missing := changed
		missing.ID = ids.New()
		if _, err := store.UpdateWorkloadTrustPolicy(ctx, missing, "1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing update err = %v", err)
		}
		bad := updated
		bad.IssuerID = ids.New()
		if _, err := store.UpdateWorkloadTrustPolicy(ctx, bad, updated.Version); !errors.Is(err, ErrWorkloadReference) {
			t.Fatalf("unknown issuer update err = %v", err)
		}
		policy = updated
	})

	t.Run("deletes respect versions and references", func(t *testing.T) {
		if err := store.DeleteWorkloadIssuer(ctx, issuer.ID, issuer.Version); !errors.Is(err, ErrWorkloadIssuerInUse) {
			t.Fatalf("delete in-use issuer err = %v", err)
		}
		if err := store.DeleteWorkloadIssuer(ctx, issuer.ID, "99"); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("delete stale issuer err = %v", err)
		}
		if err := store.DeleteWorkloadTrustPolicy(ctx, policy.ID, "99"); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("delete stale policy err = %v", err)
		}
		if err := store.DeleteWorkloadTrustPolicy(ctx, ids.New(), "1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete missing policy err = %v", err)
		}
		if err := store.DeleteWorkloadTrustPolicy(ctx, policy.ID, policy.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetWorkloadTrustPolicy(ctx, policy.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted policy still found: %v", err)
		}
		all, err := store.ListWorkloadTrustPolicies(ctx, 1000, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range all {
			if candidate.IssuerID == issuer.ID {
				if err := store.DeleteWorkloadTrustPolicy(ctx, candidate.ID, candidate.Version); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := store.DeleteWorkloadIssuer(ctx, ids.New(), "1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete missing issuer err = %v", err)
		}
		if err := store.DeleteWorkloadIssuer(ctx, issuer.ID, issuer.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetWorkloadIssuer(ctx, issuer.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted issuer still found: %v", err)
		}
	})

	t.Run("jti is recorded once atomically", func(t *testing.T) {
		issuerID, otherIssuer := ids.New(), ids.New()
		digest := sha256.Sum256([]byte("jti-" + suffix))
		expires := time.Now().Add(time.Hour)
		var inserted atomic.Int64
		var wg sync.WaitGroup
		failures := make(chan error, 32)
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := store.RecordWorkloadJTI(context.Background(), issuerID, digest[:], expires)
				if err != nil {
					failures <- err
					return
				}
				if ok {
					inserted.Add(1)
				}
			}()
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Fatal(err)
		}
		if inserted.Load() != 1 {
			t.Fatalf("concurrent inserts of one jti succeeded %d times, want exactly 1", inserted.Load())
		}
		if ok, err := store.RecordWorkloadJTI(ctx, issuerID, digest[:], expires); err != nil || ok {
			t.Fatalf("replay = %t, %v", ok, err)
		}
		if ok, err := store.RecordWorkloadJTI(ctx, otherIssuer, digest[:], expires); err != nil || !ok {
			t.Fatalf("same jti under another issuer = %t, %v", ok, err)
		}
		other := sha256.Sum256([]byte("another"))
		if ok, err := store.RecordWorkloadJTI(ctx, issuerID, other[:], expires); err != nil || !ok {
			t.Fatalf("different jti = %t, %v", ok, err)
		}
		if _, err := store.RecordWorkloadJTI(ctx, "x", digest[:], expires); !errors.Is(err, ErrWorkloadInvalid) {
			t.Fatalf("bad issuer id err = %v", err)
		}
		if _, err := store.RecordWorkloadJTI(ctx, issuerID, []byte("short"), expires); !errors.Is(err, ErrWorkloadInvalid) {
			t.Fatalf("bad hash err = %v", err)
		}
	})
}

func TestWorkloadMemoryStoreAuditsInTheSameStep(t *testing.T) {
	store := NewMemoryStore()
	ctx := t.Context()
	store.SetAuditFault(func(AuditRecord) error { return errors.New("audit sink down") })
	audit := ManagementAudit("admin", "workload.issuer.create", "workload-issuer", 201)
	if _, err := store.CreateWorkloadIssuer(ctx, newWorkloadIssuer(workloadSuffix()), audit); err == nil {
		t.Fatal("issuer created although its audit failed")
	}
	if issuers, _ := store.ListWorkloadIssuers(ctx, 10, ""); len(issuers) != 0 {
		t.Fatalf("a failed audit left %d issuers", len(issuers))
	}
	store.SetAuditFault(nil)
	issuer, err := store.CreateWorkloadIssuer(ctx, newWorkloadIssuer(workloadSuffix()), audit)
	if err != nil {
		t.Fatal(err)
	}
	store.SetAuditFault(func(AuditRecord) error { return errors.New("audit sink down") })
	if _, err := store.UpdateWorkloadIssuer(ctx, issuer, issuer.Version, audit); err == nil {
		t.Fatal("issuer updated although its audit failed")
	}
	if err := store.DeleteWorkloadIssuer(ctx, issuer.ID, issuer.Version, audit); err == nil {
		t.Fatal("issuer deleted although its audit failed")
	}
	if got, _ := store.GetWorkloadIssuer(ctx, issuer.ID); got.Version != "1" {
		t.Fatalf("issuer changed by a failed audit: %+v", got)
	}
}

func TestWorkloadStoreReturnsCopies(t *testing.T) {
	store := NewMemoryStore()
	issuer, err := store.CreateWorkloadIssuer(t.Context(), newWorkloadIssuer(workloadSuffix()))
	if err != nil {
		t.Fatal(err)
	}
	issuer.Audiences[0] = "mutated"
	again, _ := store.GetWorkloadIssuer(t.Context(), issuer.ID)
	if again.Audiences[0] == "mutated" {
		t.Fatal("the store shares slices with its callers")
	}
}
