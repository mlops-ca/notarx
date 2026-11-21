//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/mlops-ca/notarx/internal/ids"
)

func TestPostgresRemoteInstanceStore(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	exerciseRemoteInstanceStore(t, store)
}

// The sealed token is stored as TEXT, a workload_identity instance stores NULL, and the table's check refuses a token
// on a workload_identity instance and a static_token instance without one.
func TestPostgresRemoteInstanceTokenColumn(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, name := ids.New(), "token-column-"+ids.New()
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM remote_instances WHERE name LIKE 'token-column-%'`)
		_ = store.Close()
	})
	if _, err = store.CreateRemoteInstance(ctx, RemoteInstance{ID: id, Name: name, BaseURL: "https://peer.example.test", InstanceID: ids.New(),
		AuthKind: RemoteInstanceAuthStaticToken, TokenSealed: "v1:kid:sealed", State: RemoteInstanceStateActive}); err != nil {
		t.Fatal(err)
	}
	var dataType string
	if err = store.db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns WHERE table_name='remote_instances' AND column_name='token_sealed'`).Scan(&dataType); err != nil || dataType != "text" {
		t.Fatalf("token_sealed type = %q err=%v", dataType, err)
	}
	for _, bad := range []RemoteInstance{
		{ID: ids.New(), Name: "token-column-a-" + ids.New(), BaseURL: "https://peer.example.test", InstanceID: ids.New(), AuthKind: RemoteInstanceAuthWorkloadIdentity, Audience: "aud", TokenSealed: "v1:kid:sealed", State: RemoteInstanceStateActive},
		{ID: ids.New(), Name: "token-column-b-" + ids.New(), BaseURL: "https://peer.example.test", InstanceID: ids.New(), AuthKind: RemoteInstanceAuthStaticToken, State: RemoteInstanceStateActive},
		{ID: ids.New(), Name: "token-column-c-" + ids.New(), BaseURL: "https://peer.example.test", InstanceID: ids.New(), AuthKind: RemoteInstanceAuthWorkloadIdentity, State: RemoteInstanceStateActive},
	} {
		if _, err = store.CreateRemoteInstance(ctx, bad); err == nil {
			t.Fatalf("accepted a row the token or audience check must refuse: %#v", bad)
		}
	}
}
