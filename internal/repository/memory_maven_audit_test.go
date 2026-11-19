package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMemoryMavenCommitWritesItsAuditOnlyForAPublicationThatTakesEffect proves the memory store
// mirrors the PostgreSQL rollback: a commit that fails validation keeps no audit record.
func TestMemoryMavenCommitWritesItsAuditOnlyForAPublicationThatTakesEffect(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	session := MavenPublishSession{ID: "session", RepositoryID: "repo", Coordinate: "org.example:widget:1.0.0", Publisher: "alice", State: "open", ExpiresAt: time.Now().Add(time.Hour), Objects: []MavenDeclaredObject{{Name: "widget.jar", Digest: "sha256:widget", Size: 1}}}
	if _, err := store.CreateMavenPublishSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMavenPublishObject(ctx, "session", "widget.jar", "native/maven/widget"); err != nil {
		t.Fatal(err)
	}
	asset := MavenAsset{RepositoryID: "repo", Path: "org/example/widget/1.0.0/widget.jar", ObjectKey: "native/maven/widget", Digest: "sha256:widget", Size: 1}
	audit := AuditRecord{Actor: "alice", Operation: "nexus.migration.publish", Format: "maven"}
	// The same path twice cannot be published: nothing changes and nothing is audited.
	if _, _, err := store.CommitMavenPublishSessionIdempotently(ctx, "session", "key", "payload", []MavenAsset{asset, asset}, audit); !errors.Is(err, ErrNameExists) {
		t.Fatalf("duplicate path err=%v, want ErrNameExists", err)
	}
	if len(store.Audits) != 0 {
		t.Fatalf("a failed publication kept its audit: %+v", store.Audits)
	}
	if _, _, err := store.CommitMavenPublishSessionIdempotently(ctx, "session", "key", "payload", []MavenAsset{asset}, audit); err != nil {
		t.Fatal(err)
	}
	if len(store.Audits) != 1 || store.Audits[0].Operation != "nexus.migration.publish" {
		t.Fatalf("audits=%+v, want the one of the publication", store.Audits)
	}
	// A replay of the commit changes nothing and writes no second audit.
	if _, replayed, err := store.CommitMavenPublishSessionIdempotently(ctx, "session", "key", "payload", []MavenAsset{asset}, audit); err != nil || !replayed || len(store.Audits) != 1 {
		t.Fatalf("replay=%t err=%v audits=%d", replayed, err, len(store.Audits))
	}
}
