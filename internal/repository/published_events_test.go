package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
)

func publishedKeys(store *MemoryStore) []string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	var keys []string
	for _, event := range store.webhookEvents {
		if event.Type == events.ArtifactPublished {
			keys = append(keys, event.domainEvent().DedupeKey())
		}
	}
	sort.Strings(keys)
	return keys
}

func memoryMavenCommit(t *testing.T, store *MemoryStore, repositoryID, coordinate string) MavenArtifact {
	t.Helper()
	ctx := context.Background()
	session := MavenPublishSession{ID: "session-" + coordinate, RepositoryID: repositoryID, Coordinate: coordinate, Publisher: "ci", PomObject: "app.pom", State: "open", ExpiresAt: time.Now().Add(time.Hour),
		Objects: []MavenDeclaredObject{{Name: "app.pom", Digest: "sha256:" + coordinate, Size: 10}}}
	if _, err := store.CreateMavenPublishSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMavenPublishObject(ctx, session.ID, "app.pom", "native/maven/"+coordinate); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.CommitMavenPublishSession(ctx, session.ID, []MavenAsset{{RepositoryID: repositoryID, Path: mavenArtifactPathPrefix(coordinate) + "app.pom", ObjectKey: "native/maven/" + coordinate, Digest: "sha256:" + coordinate, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

// A publish on each of the three formats writes one artifact.published event with no rule and no subscription, a
// write of the digest a Raw path already holds writes none, and the payload names the format and the actor.
func TestMemoryPublishEmitsArtifactPublished(t *testing.T) {
	store := NewMemoryStore()
	ctx := WithWriteActor(context.Background(), "alice")
	asset := RawAsset{RepositoryID: "raw-repo", Path: "a/b.txt", Digest: "sha256:r1", ObjectKey: "raw/r1", Size: 3, ContentType: "text/plain"}
	for range 2 {
		if _, err := store.PutRawAsset(ctx, asset); err != nil {
			t.Fatal(err)
		}
	}
	changed := asset
	changed.Digest, changed.ObjectKey = "sha256:r2", "raw/r2"
	if _, err := store.PutRawAsset(ctx, changed); err != nil {
		t.Fatal(err)
	}
	manifest := OCIManifest{RepositoryID: "oci-repo", Name: "app", Digest: "sha256:o1", ObjectKey: "oci/o1", MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 5}
	for _, reference := range []string{"latest", "v1"} {
		if _, err := store.PutOCIManifest(ctx, manifest, reference); err != nil {
			t.Fatal(err)
		}
	}
	artifact := memoryMavenCommit(t, store, "maven-repo", "org.example:app:1.0")
	want := []string{
		"artifact.published|maven-repo|org.example:app:1.0|" + artifact.Digest + "|",
		"artifact.published|oci-repo|app|sha256:o1|",
		"artifact.published|raw-repo|a/b.txt|sha256:r1|",
		"artifact.published|raw-repo|a/b.txt|sha256:r2|",
	}
	got := publishedKeys(store)
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
	for _, event := range store.webhookEvents {
		var payload map[string]string
		if err := json.Unmarshal(event.Data, &payload); err != nil || payload["format"] != event.Format || (event.Format != "maven" && payload["actor"] != "alice") {
			t.Fatalf("payload of %s = %s (%v)", event.Format, event.Data, err)
		}
	}
}

func TestMemoryOCIReplicationAndRawVersionedWritesEmit(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	source := OCIManifest{RepositoryID: "src", Name: "app", Digest: "sha256:o1", ObjectKey: "oci/o1", MediaType: "m", Size: 5}
	if _, err := store.PutOCIManifest(ctx, source, "latest"); err != nil {
		t.Fatal(err)
	}
	target := source
	target.RepositoryID = "dst"
	if _, err := store.PublishReplicatedOCIManifest(ctx, OCIReplicationPublication{SourceRepositoryID: "src", SourceObjectKey: "oci/o1", TargetRepositoryID: "dst", Manifest: target}); err != nil {
		t.Fatal(err)
	}
	// A replay of the replication is not a new publication.
	if _, err := store.PublishReplicatedOCIManifest(ctx, OCIReplicationPublication{SourceRepositoryID: "src", SourceObjectKey: "oci/o1", TargetRepositoryID: "dst", Manifest: target}); err != nil {
		t.Fatal(err)
	}
	got := publishedKeys(store)
	if len(got) != 2 || got[0] != "artifact.published|dst|app|sha256:o1|" || got[1] != "artifact.published|src|app|sha256:o1|" {
		t.Fatalf("keys = %v", got)
	}
}

// The reconciler emits the events a visible publication lacks, once, honours the window and the per-format limit,
// and leaves a deleted Maven artifact alone.
func TestMemoryReconcilePublishedEvents(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	store.mu.Lock()
	store.rawAssets[rawAssetKey("raw-repo", "a.txt")] = RawAsset{RepositoryID: "raw-repo", Path: "a.txt", Digest: "sha256:r1", UpdatedAt: now}
	store.rawAssets[rawAssetKey("raw-repo", "old.txt")] = RawAsset{RepositoryID: "raw-repo", Path: "old.txt", Digest: "sha256:r0", UpdatedAt: now.Add(-48 * time.Hour)}
	store.ociManifests[ociManifestKey("oci-repo", "app", "sha256:o1")] = OCIManifest{RepositoryID: "oci-repo", Name: "app", Digest: "sha256:o1", CreatedAt: now}
	store.mavenArtifacts["m1"] = MavenArtifact{ID: "m1", RepositoryID: "maven-repo", Coordinate: "g:a:1", Digest: "sha256:m1", State: "visible", CreatedAt: now}
	store.mavenArtifacts["m2"] = MavenArtifact{ID: "m2", RepositoryID: "maven-repo", Coordinate: "g:a:2", Digest: "sha256:m2", State: "deleted", CreatedAt: now}
	store.mu.Unlock()
	floor := now.Add(-time.Hour)
	written, err := store.ReconcilePublishedEvents(ctx, floor, 100)
	if err != nil || written != 3 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	want := []string{"artifact.published|maven-repo|g:a:1|sha256:m1|", "artifact.published|oci-repo|app|sha256:o1|", "artifact.published|raw-repo|a.txt|sha256:r1|"}
	got := publishedKeys(store)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if again, err := store.ReconcilePublishedEvents(ctx, floor, 100); err != nil || again != 0 {
		t.Fatalf("a second reconcile wrote %d (%v)", again, err)
	}
	// Without a floor the older Raw path is reconciled too; an event older than the floor does not count.
	if all, err := store.ReconcilePublishedEvents(ctx, time.Time{}, 100); err != nil || all != 1 {
		t.Fatalf("an unbounded reconcile wrote %d (%v)", all, err)
	}
	store.mu.Lock()
	for id, event := range store.webhookEvents {
		event.OccurredAt = now.Add(-72 * time.Hour)
		store.webhookEvents[id] = event
	}
	store.mu.Unlock()
	if stale, err := store.ReconcilePublishedEvents(ctx, floor, 100); err != nil || stale != 3 {
		t.Fatalf("publications whose events left the window wrote %d (%v)", stale, err)
	}
}

func TestMemoryReconcileHonoursThePerFormatLimit(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	store.mu.Lock()
	for _, path := range []string{"a", "b", "c"} {
		store.rawAssets[rawAssetKey("r", path)] = RawAsset{RepositoryID: "r", Path: path, Digest: "sha256:" + path, UpdatedAt: now}
	}
	store.mu.Unlock()
	if written, err := store.ReconcilePublishedEvents(context.Background(), now.Add(-time.Hour), 2); err != nil || written != 2 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if written, err := store.ReconcilePublishedEvents(context.Background(), now.Add(-time.Hour), 2); err != nil || written != 1 {
		t.Fatalf("written=%d err=%v", written, err)
	}
}

// Writing the digest and content type a Raw path already holds keeps its update time, so an old publication does not look
// new to the reconciler; a change of either moves it.
func TestMemoryRawRewriteOfTheSameReferenceKeepsTheUpdateTime(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	asset := RawAsset{RepositoryID: "r", Path: "a.txt", Digest: "sha256:r1", ObjectKey: "raw/r1", Size: 3, ContentType: "text/plain"}
	first, err := store.PutRawAsset(ctx, asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutRawAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRawAsset(ctx, "r", "a.txt")
	if err != nil || !stored.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("an identical rewrite moved the update time: %v -> %v (%v)", first.UpdatedAt, stored.UpdatedAt, err)
	}
	asset.ContentType = "application/json"
	if _, err = store.PutRawAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	if stored, err = store.GetRawAsset(ctx, "r", "a.txt"); err != nil || !stored.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("a content type change kept the update time: %v (%v)", stored.UpdatedAt, err)
	}
}

// A publication that keeps failing stays at the head of its candidate query; the pass pages past it so the publications
// behind it are still reconciled.
func TestDrainPublishedCandidatesPagesPastFailingCandidates(t *testing.T) {
	const limit = 3
	all := make([]publishedCandidate, 0, 8)
	for _, name := range []string{"bad1", "bad2", "bad3", "good1", "good2", "good3", "good4"} {
		all = append(all, publishedCandidate{RepositoryID: "r", Format: "raw", Coordinate: name, Digest: "sha256:" + name})
	}
	emitted := map[string]bool{}
	fetch := func(pageSize int) ([]publishedCandidate, error) {
		var pending []publishedCandidate
		for _, candidate := range all {
			if !emitted[candidate.Coordinate] {
				pending = append(pending, candidate)
			}
		}
		if len(pending) > pageSize {
			pending = pending[:pageSize]
		}
		return pending, nil
	}
	emit := func(candidate publishedCandidate) (bool, error) {
		if len(candidate.Coordinate) > 3 && candidate.Coordinate[:3] == "bad" {
			return false, errors.New("boom")
		}
		emitted[candidate.Coordinate] = true
		return true, nil
	}
	written, failures := drainPublishedCandidates(context.Background(), limit, fetch, emit)
	if written != limit || len(failures) != 3 || !emitted["good1"] || !emitted["good2"] || !emitted["good3"] || emitted["good4"] {
		t.Fatalf("written=%d failures=%v emitted=%v, want %d writes behind 3 failing candidates and no more", written, failures, emitted, limit)
	}
	// Nothing left but failures: the pass ends instead of looping.
	written, failures = drainPublishedCandidates(context.Background(), 10, fetch, emit)
	if written != 1 || len(failures) != 3 {
		t.Fatalf("second pass written=%d failures=%v", written, failures)
	}
}

// A format whose publications keep failing ends the pass at the cap instead of widening its pages without bound.
func TestDrainPublishedCandidatesStopsAtTheFailureCap(t *testing.T) {
	var fetches int
	fetch := func(pageSize int) ([]publishedCandidate, error) {
		fetches++
		out := make([]publishedCandidate, pageSize)
		for i := range out {
			out[i] = publishedCandidate{RepositoryID: "r", Format: "raw", Coordinate: "bad" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)), Digest: "d"}
		}
		return out, nil
	}
	emit := func(publishedCandidate) (bool, error) { return false, errors.New("boom") }
	written, failures := drainPublishedCandidates(context.Background(), 5, fetch, emit)
	if written != 0 || len(failures) <= maxFailedPublicationsPerPass || fetches > maxFailedPublicationsPerPass {
		t.Fatalf("written=%d failures=%d fetches=%d", written, len(failures), fetches)
	}
}
