//go:build integration

package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

func TestPostgresAPIKeyRevocation(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	token := "agk_postgres-test-token-" + uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	key, err := store.CreateAPIKey(context.Background(), repository.APIKey{ID: uuid.NewString(), Name: "integration", SecretHash: authorization.HashAPIKey(token), Roles: []string{"admin"}, ExpiresAt: &expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := store.FindActiveAPIKeyByHash(context.Background(), authorization.HashAPIKey(token)); err != nil || found.ID != key.ID || found.LastUsedAt != nil {
		t.Fatalf("find active key=%#v err=%v", found, err)
	}
	authenticator := authorization.Authenticator{APIKeys: store}
	if principal, ok := authenticator.Authenticate("Bearer " + token); !ok || !principal.Admin {
		t.Fatalf("principal=%#v authenticated=%t", principal, ok)
	}
	usedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.TouchAPIKeys(context.Background(), []repository.APIKeyUse{{ID: key.ID, At: usedAt}}, usedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if found, err := store.FindActiveAPIKeyByHash(context.Background(), authorization.HashAPIKey(token)); err != nil || found.LastUsedAt == nil || !found.LastUsedAt.Equal(usedAt) {
		t.Fatalf("last used after touch=%v err=%v", found.LastUsedAt, err)
	}
	if err := store.TouchAPIKeys(context.Background(), []repository.APIKeyUse{{ID: key.ID, At: usedAt.Add(time.Second)}}, usedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if found, _ := store.FindActiveAPIKeyByHash(context.Background(), authorization.HashAPIKey(token)); !found.LastUsedAt.Equal(usedAt) {
		t.Fatalf("a touch inside the interval moved last_used_at to %v", found.LastUsedAt)
	}
	if _, err := store.RevokeAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := authenticator.Authenticate("Bearer " + token); ok {
		t.Fatal("revoked PostgreSQL API key authenticated")
	}

	expiredToken := "agk_postgres-expired-" + uuid.NewString()
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := store.CreateAPIKey(context.Background(), repository.APIKey{ID: uuid.NewString(), Name: "expired", SecretHash: authorization.HashAPIKey(expiredToken), Roles: []string{"member"}, ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindActiveAPIKeyByHash(context.Background(), authorization.HashAPIKey(expiredToken)); err != repository.ErrNotFound {
		t.Fatalf("expired key lookup error=%v want ErrNotFound", err)
	}
}

func TestPostgresServiceAccountCredentialRotationAndDisable(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	account, err := store.CreateServiceAccount(context.Background(), repository.ServiceAccount{
		ID: uuid.NewString(), Name: "postgres-ci-" + uuid.NewString(), Description: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "agc_postgres-service-account-" + uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	credential, err := store.CreateServiceAccountCredential(context.Background(), repository.APIKey{
		ID: uuid.NewString(), ServiceAccountID: account.ID, Name: "blue",
		SecretHash: authorization.HashAPIKey(token), Roles: []string{}, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A warm 5s cache: the disable below must evict synchronously on this node.
	registry := readcache.NewRegistry(5 * time.Second)
	store.SetCacheInvalidator(registry.Invalidate)
	keys := authorization.NewCachedAPIKeys(store, registry, store, time.Minute, nil)
	authenticator := authorization.Authenticator{APIKeys: keys, ServiceAccounts: authorization.NewCachedServiceAccounts(store, registry)}
	principal, ok := authenticator.AuthenticateContext(context.Background(), "Bearer "+token)
	if !ok || principal.Actor != "service-account:"+account.ID || principal.AuthenticationKind != authorization.AuthenticationServiceAccountCredential {
		t.Fatalf("principal=%#v authenticated=%t", principal, ok)
	}
	standalone, err := store.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range standalone {
		if key.ID == credential.ID {
			t.Fatalf("service-account credential leaked into standalone keys: %#v", key)
		}
	}
	if _, err := store.RevokeAPIKey(context.Background(), credential.ID); err != repository.ErrNotFound {
		t.Fatalf("generic revoke error=%v want ErrNotFound", err)
	}

	disabled := repository.ServiceAccountDisabled
	if _, err := store.UpdateServiceAccount(context.Background(), repository.ServiceAccountUpdate{ID: account.ID, State: &disabled}, account.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok := authenticator.AuthenticateContext(context.Background(), "Bearer "+token); ok {
		t.Fatal("disabled PostgreSQL service account credential authenticated")
	}
	if _, err := store.CreateServiceAccountCredential(context.Background(), repository.APIKey{
		ID: uuid.NewString(), ServiceAccountID: account.ID, Name: "green",
		SecretHash: authorization.HashAPIKey("must-not-issue"), Roles: []string{}, ExpiresAt: &expiresAt,
	}); !errors.Is(err, repository.ErrServiceAccountDisabled) {
		t.Fatalf("disabled credential creation error=%v want ErrServiceAccountDisabled", err)
	}
	if err := keys.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	credentials, err := store.ListServiceAccountCredentials(context.Background(), account.ID, 200, "")
	if err != nil || len(credentials) != 1 || credentials[0].LastUsedAt == nil {
		t.Fatalf("credentials=%#v err=%v", credentials, err)
	}
}

// TestPostgresRevocationReachesOtherNodeThroughNotify revokes a key on one store
// and expects a second store, with its own caches and a listener on the
// notarx_cache channel, to stop accepting it long before the authorization TTL.
func TestPostgresRevocationReachesOtherNodeThroughNotify(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	nodeA, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nodeA.Close() }()
	nodeB, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nodeB.Close() }()
	notifications, err := database.OpenPostgres(databaseURL, database.NotificationPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = notifications.Close() }()

	registry := readcache.NewRegistry(30 * time.Second) // far above the test deadline: only NOTIFY can evict
	keys := authorization.NewCachedAPIKeys(nodeB, registry, nodeB, time.Minute, nil)
	nodeB.SetCacheInvalidator(registry.Invalidate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{})
	connect := readcache.PostgresConnect(notifications)
	listener := &readcache.Listener{Registry: registry, Connect: func(ctx context.Context) (readcache.Stream, error) {
		stream, err := connect(ctx)
		if err == nil {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
		return stream, err
	}}
	go listener.Run(ctx)
	<-connected

	token := "agk_notify-test-token-" + uuid.NewString()
	key, err := nodeA.CreateAPIKey(ctx, repository.APIKey{ID: uuid.NewString(), Name: "notify", SecretHash: authorization.HashAPIKey(token), Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := authorization.Authenticator{APIKeys: keys}
	if _, ok := authenticator.AuthenticateContext(ctx, "Bearer "+token); !ok {
		t.Fatal("node B rejected a valid key")
	}
	if _, err := nodeA.RevokeAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := authenticator.AuthenticateContext(ctx, "Bearer "+token); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("node B still accepts the revoked key: the notification did not evict it")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
