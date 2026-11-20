package repository_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

// rawVersionContractStore is what the versioned Raw contract needs from a store: the file surface, the version
// surface and the repository calls that choose and keep the layout.
type rawVersionContractStore interface {
	repository.NativeRawStore
	repository.NativeRawVersionStore
	repository.HostedRepositoryStore
	SetRawMaxFilesPerVersion(int)
}

type rawVersionContractFactory func(*testing.T) rawVersionContractStore

func TestRawVersionMemoryStoreContract(t *testing.T) {
	runRawVersionStoreContract(t, func(t *testing.T) rawVersionContractStore {
		t.Helper()
		return repository.NewMemoryStore()
	})
}

// The same assertions run against PostgreSQL under the integration build tag. Every case creates its own
// repository and unique content, so no backend's maps or schema are used to make one implementation pass.
func runRawVersionStoreContract(t *testing.T, newStore rawVersionContractFactory) {
	t.Helper()
	ctx := context.Background()
	// A deleted file leaves its object unreferenced, and a database shared with other suites must not keep it: a case
	// marks the objects it left collected when it ends, as the conditional-write suite does.
	factory := func(t *testing.T) rawVersionContractStore {
		t.Helper()
		store := newStore(t)
		known := map[string]bool{}
		before, err := store.ListUnreferencedRawObjects(ctx, time.Now().Add(time.Hour), 100000)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range before {
			known[object.Digest] = true
		}
		t.Cleanup(func() {
			left, err := store.ListUnreferencedRawObjects(ctx, time.Now().Add(time.Hour), 100000)
			if err != nil {
				t.Error(err)
				return
			}
			for _, object := range left {
				if !known[object.Digest] {
					_ = store.MarkRawObjectCollected(ctx, object.Digest)
				}
			}
		})
		return store
	}
	newRepo := func(t *testing.T, store rawVersionContractStore, mutate func(*repository.HostedRepository)) repository.HostedRepository {
		t.Helper()
		repo := repository.HostedRepository{ID: uuid.NewString(), Name: "raw-ver-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16], Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned}
		if mutate != nil {
			mutate(&repo)
		}
		created, err := store.CreateHostedRepository(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	asset := func(repoID, path, content string) repository.RawAsset {
		sum := sha256.Sum256([]byte(repoID + ":" + content))
		return repository.RawAsset{RepositoryID: repoID, Path: path, Digest: fmt.Sprintf("sha256:%x", sum), ObjectKey: fmt.Sprintf("native/raw/sha256/%x", sum), Size: int64(len(content)), ContentType: "text/plain"}
	}
	create := func(path, content string) repository.RawWriteCondition {
		return repository.RawWriteCondition{RequireAbsent: true}
	}
	put := func(t *testing.T, store rawVersionContractStore, repoID, path, content string) (repository.RawAsset, error) {
		t.Helper()
		return store.PutRawAssetIf(repository.WithRawWriteActor(ctx, "ci"), asset(repoID, path, content), create(path, content))
	}
	mustPut := func(t *testing.T, store rawVersionContractStore, repoID, path, content string) repository.RawAsset {
		t.Helper()
		result, err := put(t, store, repoID, path, content)
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		return result
	}
	version := func(t *testing.T, store rawVersionContractStore, repoID, name, ver string) repository.RawVersion {
		t.Helper()
		found, err := store.GetRawVersion(ctx, repoID, name, ver)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}

	t.Run("layout_is_chosen_at_creation_and_shape_checked", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, func(r *repository.HostedRepository) { r.RawStrict = true; r.SealMode = repository.RawSealIdle })
		got, err := store.GetHostedRepository(ctx, repo.ID)
		if err != nil || got.RawLayout != repository.RawLayoutVersioned || got.SealMode != repository.RawSealIdle || !got.RawStrict {
			t.Fatalf("layout not stored: %#v %v", got, err)
		}
		plain, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "raw-path-" + uuid.NewString()[:8], Format: repository.FormatRaw})
		if err != nil || plain.RawLayout != repository.RawLayoutPath || plain.SealMode != repository.RawSealExplicit || plain.RawStrict {
			t.Fatalf("defaults: %#v %v", plain, err)
		}
		for name, bad := range map[string]repository.HostedRepository{
			"layout on npm":      {Format: repository.FormatNPM, RawLayout: repository.RawLayoutVersioned},
			"strict on npm":      {Format: repository.FormatNPM, RawStrict: true},
			"layout on proxy":    {Format: repository.FormatRaw, Type: repository.RepositoryTypeProxy, Endpoint: "https://example.test", RawLayout: repository.RawLayoutVersioned},
			"strict on path":     {Format: repository.FormatRaw, RawStrict: true},
			"idle on path":       {Format: repository.FormatRaw, SealMode: repository.RawSealIdle},
			"unknown layout":     {Format: repository.FormatRaw, RawLayout: "tree"},
			"unknown seal mode":  {Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned, SealMode: "never"},
			"seal mode on maven": {Format: repository.FormatMaven, SealMode: repository.RawSealExplicit},
		} {
			bad.ID, bad.Name = uuid.NewString(), "raw-bad-"+uuid.NewString()[:8]
			if _, err := store.CreateHostedRepository(ctx, bad); !errors.Is(err, repository.ErrRawLayoutMismatch) {
				t.Errorf("%s: got %v, want ErrRawLayoutMismatch", name, err)
			}
		}
	})
	t.Run("layout_survives_a_repository_update", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, func(r *repository.HostedRepository) { r.RawStrict = true })
		updated, err := store.UpdateHostedRepository(ctx, repository.HostedRepository{ID: repo.ID, Visibility: repository.VisibilityAuthenticated}, repo.Version)
		if err != nil {
			t.Fatal(err)
		}
		if updated.RawLayout != repository.RawLayoutVersioned || !updated.RawStrict || updated.SealMode != repository.RawSealExplicit {
			t.Fatalf("update changed the layout: %#v", updated)
		}
	})
	t.Run("path_validation", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		for _, path := range []string{
			"tool", "tool/1.0.0", "Tool/1.0.0/file", "-tool/1.0.0/file", "to ol/1.0.0/file", "tool/-1.0.0/file", "tool/1 0/file",
			"tool/1.0.0/.notarx/x", "tool/1.0.0/.notarx", strings.Repeat("a", 129) + "/1.0.0/f", "tool/" + strings.Repeat("1", 129) + "/f",
			"tool/1.0.0/a/b/c/d/e/f/g/h/i", "t\u00e9st/1.0.0/f", "tool/1.0.0\u00e9/f",
		} {
			if _, err := put(t, store, repo.ID, path, path); !errors.Is(err, repository.ErrInvalidGenericPath) {
				t.Errorf("%q: got %v, want ErrInvalidGenericPath", path, err)
			}
		}
		for _, path := range []string{"tool/1.0.0/file", "a/1/f", "tool_x.y-z/1.0.0-RC1+build~1/bin/linux/tool", "tool/1.0.0/a/b/c/d/e/f/g/h"} {
			if _, err := put(t, store, repo.ID, path, path); err != nil {
				t.Errorf("%q: %v", path, err)
			}
		}
		if _, err := store.ListRawVersionFiles(ctx, repo.ID, "tool", "1.0.0"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("files_are_create_only", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		first := mustPut(t, store, repo.ID, "tool/1.0.0/bin", "one")
		if first.Replayed {
			t.Fatal("a first write is not a replay")
		}
		again, err := put(t, store, repo.ID, "tool/1.0.0/bin", "one")
		if err != nil || !again.Replayed || again.Digest != first.Digest {
			t.Fatalf("same digest must replay: %#v %v", again, err)
		}
		other, err := put(t, store, repo.ID, "tool/1.0.0/bin", "two")
		if !errors.Is(err, repository.ErrVersionFileExists) || other.Digest != first.Digest {
			t.Fatalf("different digest must conflict and report the current file: %#v %v", other, err)
		}
		// An unconditional write is create-only too.
		if _, err = store.PutRawAsset(ctx, asset(repo.ID, "tool/1.0.0/bin", "three")); !errors.Is(err, repository.ErrVersionFileExists) {
			t.Fatalf("unconditional overwrite: %v", err)
		}
		current, err := store.GetRawAsset(ctx, repo.ID, "tool/1.0.0/bin")
		if err != nil || current.Digest != first.Digest {
			t.Fatalf("a refused write mutated the file: %#v %v", current, err)
		}
		if _, err = store.PutRawAssetIf(ctx, asset(repo.ID, "tool/1.0.0/bin", "one"), repository.RawWriteCondition{RequirePresent: true}); !errors.Is(err, repository.ErrPreconditionFailed) {
			t.Fatalf("If-Match style write: %v", err)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.FileCount != 1 || v.SizeBytes != 3 || v.State != repository.RawVersionOpen || v.CreatedBy != "ci" {
			t.Fatalf("version row after replay and conflicts: %#v", v)
		}
	})
	t.Run("concurrent_writers_have_one_winner", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		const writers = 8
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = put(t, store, repo.ID, "tool/1.0.0/race", fmt.Sprint("content-", i))
			}()
		}
		wg.Wait()
		winners := 0
		for _, err := range errs {
			switch {
			case err == nil:
				winners++
			case !errors.Is(err, repository.ErrVersionFileExists):
				t.Errorf("loser failed with %v", err)
			}
		}
		if winners != 1 {
			t.Fatalf("winners = %d, want 1", winners)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.FileCount != 1 {
			t.Fatalf("file_count = %d after the race", v.FileCount)
		}
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = put(t, store, repo.ID, fmt.Sprintf("tool/2.0.0/file-%d", i), fmt.Sprint("distinct-", i))
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if v := version(t, store, repo.ID, "tool", "2.0.0"); v.FileCount != writers {
			t.Fatalf("file_count = %d, want %d", v.FileCount, writers)
		}
	})
	t.Run("seal_serialises_against_concurrent_uploads", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		mustPut(t, store, repo.ID, "tool/1.0.0/seed", "seed")
		const writers = 12
		results := make([]error, writers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, results[i] = put(t, store, repo.ID, fmt.Sprintf("tool/1.0.0/late-%02d", i), fmt.Sprint("late-", i))
			}()
		}
		var sealed repository.RawVersion
		var sealErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sealed, sealErr = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"})
		}()
		close(start)
		wg.Wait()
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		files, err := store.ListRawVersionFiles(ctx, repo.ID, "tool", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		accepted := 1
		for i, err := range results {
			switch {
			case err == nil:
				accepted++
			case !errors.Is(err, repository.ErrVersionSealed):
				t.Errorf("writer %d: %v", i, err)
			}
		}
		// Every accepted upload is in the sealed manifest and no refused one is: the seal saw one consistent set.
		if len(files) != accepted {
			t.Fatalf("%d files stored, %d uploads accepted", len(files), accepted)
		}
		if _, digest := repository.RawVersionManifest("tool", "1.0.0", files, nil); digest != sealed.ManifestDigest {
			t.Fatalf("the manifest digest %s does not describe the stored files %s", sealed.ManifestDigest, digest)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.FileCount != accepted || v.State != repository.RawVersionSealed {
			t.Fatalf("version after the race: %#v, accepted %d", v, accepted)
		}
	})
	t.Run("identical_bytes_in_concurrent_writers_and_deleters_do_not_deadlock", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		for round := 0; round < 25; round++ {
			version := fmt.Sprintf("1.0.%d", round)
			mustPut(t, store, repo.ID, "tool/"+version+"/first", "shared bytes")
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				errs <- store.DeleteRawAsset(ctx, repo.ID, "tool/"+version+"/first")
			}()
			go func() {
				defer wg.Done()
				_, err := put(t, store, repo.ID, "tool/"+version+"/second", "shared bytes")
				errs <- err
			}()
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
			}
		}
	})
	t.Run("files_per_version_cap", func(t *testing.T) {
		store := factory(t)
		store.SetRawMaxFilesPerVersion(2)
		t.Cleanup(func() { store.SetRawMaxFilesPerVersion(0) })
		repo := newRepo(t, store, nil)
		mustPut(t, store, repo.ID, "tool/1.0.0/a", "a")
		mustPut(t, store, repo.ID, "tool/1.0.0/b", "b")
		if _, err := put(t, store, repo.ID, "tool/1.0.0/c", "c"); !errors.Is(err, repository.ErrVersionFileLimit) {
			t.Fatalf("third file: %v", err)
		}
		if again, err := put(t, store, repo.ID, "tool/1.0.0/a", "a"); err != nil || !again.Replayed {
			t.Fatalf("a replay at the cap: %#v %v", again, err)
		}
		mustPut(t, store, repo.ID, "tool/1.0.1/a", "a")
	})
	t.Run("delete_repairs_only_an_open_version", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		mustPut(t, store, repo.ID, "tool/1.0.0/a", "a")
		mustPut(t, store, repo.ID, "tool/1.0.0/b", "bb")
		if err := store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/missing"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing file: %v", err)
		}
		if err := store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/b"); err != nil {
			t.Fatal(err)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.FileCount != 1 || v.SizeBytes != 1 {
			t.Fatalf("counts after delete: %#v", v)
		}
		mustPut(t, store, repo.ID, "tool/1.0.0/b", "different after repair")
		sealed, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Actor: "ci"})
		if err != nil || sealed.State != repository.RawVersionSealed {
			t.Fatalf("seal: %#v %v", sealed, err)
		}
		if err := store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/a"); !errors.Is(err, repository.ErrVersionSealed) {
			t.Fatalf("delete after seal: %v", err)
		}
		if _, err := put(t, store, repo.ID, "tool/1.0.0/new", "new"); !errors.Is(err, repository.ErrVersionSealed) {
			t.Fatalf("new file after seal: %v", err)
		}
		if _, err := put(t, store, repo.ID, "tool/1.0.0/a", "other"); !errors.Is(err, repository.ErrVersionFileExists) {
			t.Fatalf("overwrite after seal: %v", err)
		}
		if again, err := put(t, store, repo.ID, "tool/1.0.0/a", "a"); err != nil || !again.Replayed {
			t.Fatalf("replay after seal: %#v %v", again, err)
		}
		if _, err := store.RestoreRawAsset(ctx, repo.ID, "tool/1.0.0/a"); !errors.Is(err, repository.ErrRawLayoutMismatch) {
			t.Fatalf("restore in a versioned repository: %v", err)
		}
	})
	t.Run("strict_repository_never_deletes_a_file", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, func(r *repository.HostedRepository) { r.RawStrict = true })
		mustPut(t, store, repo.ID, "tool/1.0.0/a", "a")
		if err := store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/a"); !errors.Is(err, repository.ErrVersionFileImmutable) {
			t.Fatalf("strict delete of an open version: %v", err)
		}
		if err := store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/gone"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("strict delete of a missing file: %v", err)
		}
		if _, err := store.GetRawAsset(ctx, repo.ID, "tool/1.0.0/a"); err != nil {
			t.Fatalf("the file must survive: %v", err)
		}
	})
	t.Run("resumable_completion_obeys_the_guard", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		existing := mustPut(t, store, repo.ID, "tool/1.0.0/a", "a")
		complete := func(path, content string) (repository.RawAsset, error) {
			upload, err := store.CreateRawUpload(ctx, repository.RawUpload{ID: uuid.NewString(), RepositoryID: repo.ID, Path: path, ObjectKey: "native/raw/uploads/" + uuid.NewString(), State: "open", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			return store.CompleteRawUploadIf(ctx, upload.ID, asset(repo.ID, path, content), repository.RawWriteCondition{})
		}
		if _, err := complete("tool/1.0.0/a", "other"); !errors.Is(err, repository.ErrVersionFileExists) {
			t.Fatalf("resumable overwrite: %v", err)
		}
		if replay, err := complete("tool/1.0.0/a", "a"); err != nil || !replay.Replayed || replay.Digest != existing.Digest {
			t.Fatalf("resumable replay: %#v %v", replay, err)
		}
		if _, err := complete("Bad/1.0.0/a", "x"); !errors.Is(err, repository.ErrInvalidGenericPath) {
			t.Fatalf("resumable invalid path: %v", err)
		}
		if _, err := complete("tool/1.0.0/b", "b"); err != nil {
			t.Fatal(err)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.FileCount != 2 {
			t.Fatalf("file_count = %d", v.FileCount)
		}
	})
	t.Run("seal_computes_the_manifest_digest_once", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		if _, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "9.9.9"}); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("seal of an unknown version: %v", err)
		}
		created, isNew, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", CreatedBy: "ci", Properties: map[string]string{"git.sha": "abc"}})
		if err != nil || !isNew || created.State != repository.RawVersionOpen || created.Properties["git.sha"] != "abc" {
			t.Fatalf("create: %#v %v %v", created, isNew, err)
		}
		if _, err = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, repository.ErrVersionEmpty) {
			t.Fatalf("seal of an empty version: %v", err)
		}
		a := mustPut(t, store, repo.ID, "tool/1.0.0/a.txt", "alpha")
		b := mustPut(t, store, repo.ID, "tool/1.0.0/bin/b.txt", "beta")
		sealed, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Actor: "ci", Audit: &repository.AuditRecord{Actor: "ci", Operation: "raw.version.seal"}})
		if err != nil {
			t.Fatal(err)
		}
		_, want := repository.RawVersionManifest("tool", "1.0.0", []repository.RawAsset{b, a}, map[string]string{"git.sha": "abc"})
		if sealed.State != repository.RawVersionSealed || sealed.ManifestDigest != want || sealed.SealedAt.IsZero() {
			t.Fatalf("sealed: %#v want digest %s", sealed, want)
		}
		again, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"})
		if err != nil || again.ManifestDigest != want {
			t.Fatalf("second seal: %#v %v", again, err)
		}
		hexOf := func(digest string) string { return strings.TrimPrefix(digest, "sha256:") }
		matching := []repository.RawVersionFileRef{{Path: "a.txt", SHA256: hexOf(a.Digest)}, {Path: "bin/b.txt", SHA256: hexOf(b.Digest)}}
		if repeated, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Expected: matching}); err != nil || repeated.ManifestDigest != want {
			t.Fatalf("a repeated seal that agrees with the sealed files: %#v %v", repeated, err)
		}
		// A repeated seal is verified against the files that were sealed, not waved through.
		if _, err = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Expected: matching[:1]}); !errors.Is(err, repository.ErrChecksumMismatch) {
			t.Fatalf("a repeated seal with a file set that differs from the sealed one: %v", err)
		}
		if _, err = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", ChecksumsFile: "missing", ChecksumsDigest: a.Digest}); !errors.Is(err, repository.ErrChecksumsChanged) {
			t.Fatalf("a repeated seal naming a checksums file the version does not hold: %v", err)
		}
		if _, err = store.SetRawVersionProperties(ctx, repo.ID, "tool", "1.0.0", map[string]string{"k": "v"}); !errors.Is(err, repository.ErrVersionSealed) {
			t.Fatalf("properties after seal: %v", err)
		}
		again2, isNew, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"})
		if err != nil || isNew || again2.ManifestDigest != want {
			t.Fatalf("a create that repeats a sealed version must answer it unchanged: %#v %v %v", again2, isNew, err)
		}
		if same, _, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Properties: map[string]string{"git.sha": "abc"}}); err != nil || same.State != repository.RawVersionSealed {
			t.Fatalf("a create with the sealed properties: %#v %v", same, err)
		}
		if _, _, err = store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", Properties: map[string]string{"git.sha": "other"}}); !errors.Is(err, repository.ErrVersionSealed) {
			t.Fatalf("a create that would change a sealed version: %v", err)
		}
		files, err := store.ListRawVersionFiles(ctx, repo.ID, "tool", "1.0.0")
		if err != nil || len(files) != 2 || files[0].Path != "tool/1.0.0/a.txt" || files[1].Path != "tool/1.0.0/bin/b.txt" {
			t.Fatalf("files: %#v %v", files, err)
		}
	})
	t.Run("seal_verifies_expected_files_and_checksums", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		a := mustPut(t, store, repo.ID, "tool/1.0.0/a", "alpha")
		sums := mustPut(t, store, repo.ID, "tool/1.0.0/SHA256SUMS", "sums")
		hexOf := func(digest string) string { return strings.TrimPrefix(digest, "sha256:") }
		base := repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}
		for name, input := range map[string]repository.SealRawVersionInput{
			"missing expected file": {Expected: []repository.RawVersionFileRef{{Path: "a", SHA256: hexOf(a.Digest)}}},
			"wrong expected digest": {Expected: []repository.RawVersionFileRef{{Path: "a", SHA256: strings.Repeat("0", 64)}, {Path: "SHA256SUMS", SHA256: hexOf(sums.Digest)}}},
			"unexpected file":       {Expected: []repository.RawVersionFileRef{{Path: "a", SHA256: hexOf(a.Digest)}, {Path: "SHA256SUMS", SHA256: hexOf(sums.Digest)}, {Path: "c", SHA256: hexOf(a.Digest)}}},
			"duplicate expected":    {Expected: []repository.RawVersionFileRef{{Path: "a", SHA256: hexOf(a.Digest)}, {Path: "a", SHA256: hexOf(a.Digest)}}},
			"checksums disagree":    {ChecksumsFile: "SHA256SUMS", ChecksumsDigest: sums.Digest, Checksums: map[string]string{"a": strings.Repeat("1", 64)}},
			"checksums name a gone": {ChecksumsFile: "SHA256SUMS", ChecksumsDigest: sums.Digest, Checksums: map[string]string{"zzz": hexOf(a.Digest)}},
		} {
			input.RepositoryID, input.Name, input.Version = base.RepositoryID, base.Name, base.Version
			if _, err := store.SealRawVersion(ctx, input); !errors.Is(err, repository.ErrChecksumMismatch) {
				t.Errorf("%s: got %v, want ErrChecksumMismatch", name, err)
			}
		}
		stale := base
		stale.ChecksumsFile, stale.ChecksumsDigest = "SHA256SUMS", a.Digest
		if _, err := store.SealRawVersion(ctx, stale); !errors.Is(err, repository.ErrChecksumsChanged) {
			t.Errorf("a changed checksums file: %v", err)
		}
		absent := base
		absent.ChecksumsFile, absent.ChecksumsDigest = "NOPE", a.Digest
		if _, err := store.SealRawVersion(ctx, absent); !errors.Is(err, repository.ErrChecksumsChanged) {
			t.Errorf("an absent checksums file: %v", err)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.State != repository.RawVersionOpen {
			t.Fatalf("a failed seal changed the state: %#v", v)
		}
		ok := base
		ok.Expected = []repository.RawVersionFileRef{{Path: "SHA256SUMS", SHA256: strings.ToUpper(hexOf(sums.Digest))}, {Path: "a", SHA256: hexOf(a.Digest)}}
		ok.ChecksumsFile, ok.ChecksumsDigest, ok.Checksums = "SHA256SUMS", sums.Digest, map[string]string{"a": hexOf(a.Digest)}
		ok.Properties = map[string]string{"channel": "stable"}
		sealed, err := store.SealRawVersion(ctx, ok)
		if err != nil || sealed.Properties["channel"] != "stable" {
			t.Fatalf("seal with every check: %#v %v", sealed, err)
		}
		changed := base
		changed.Properties = map[string]string{"channel": "beta"}
		if _, err = store.SealRawVersion(ctx, changed); !errors.Is(err, repository.ErrVersionSealed) {
			t.Fatalf("a second seal that changes properties: %v", err)
		}
	})
	t.Run("properties_are_bounded_and_filterable", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		many := map[string]string{}
		for i := 0; i <= repository.RawVersionMaxProperties; i++ {
			many[fmt.Sprintf("k%02d", i)] = "v"
		}
		for name, props := range map[string]map[string]string{
			"too many keys": many, "uppercase key": {"Key": "v"}, "digit first": {"1k": "v"}, "long value": {"k": strings.Repeat("v", 257)},
			"control value": {"k": "a\x00b"}, "bidi value": {"k": "a\u202eb"}, "invalid utf8": {"k": "\xff"}, "long key": {strings.Repeat("k", 65): "v"},
		} {
			if _, _, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "0.0.1", Properties: props}); !errors.Is(err, repository.ErrInvalidVersionProperties) {
				t.Errorf("%s: got %v", name, err)
			}
		}
		total := map[string]string{}
		for i := 0; i < repository.RawVersionMaxProperties; i++ {
			total[fmt.Sprintf("k%02d", i)] = strings.Repeat("v", 256)
		}
		if _, _, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "0.0.1", Properties: total}); !errors.Is(err, repository.ErrInvalidVersionProperties) {
			t.Errorf("8 KiB total: got %v", err)
		}
		for _, ver := range []string{"1.0.0", "1.1.0", "2.0.0"} {
			channel := "stable"
			if ver == "2.0.0" {
				channel = "beta"
			}
			mustPut(t, store, repo.ID, "tool/"+ver+"/f", ver)
			if _, err := store.SetRawVersionProperties(ctx, repo.ID, "tool", ver, map[string]string{"channel": channel, "git.sha": "sha-" + ver}); err != nil {
				t.Fatal(err)
			}
		}
		list := func(query repository.RawVersionQuery) []string {
			query.RepositoryID, query.Name = repo.ID, "tool"
			versions, err := store.ListRawVersions(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]string, 0, len(versions))
			for _, v := range versions {
				out = append(out, v.Version)
			}
			slices.Sort(out)
			return out
		}
		if got := list(repository.RawVersionQuery{Properties: []repository.RawVersionProperty{{Key: "channel", Value: "stable"}}}); !slices.Equal(got, []string{"1.0.0", "1.1.0"}) {
			t.Fatalf("property filter: %v", got)
		}
		if got := list(repository.RawVersionQuery{Properties: []repository.RawVersionProperty{{Key: "channel", Value: "stable"}, {Key: "git.sha", Value: "sha-1.1.0"}}}); !slices.Equal(got, []string{"1.1.0"}) {
			t.Fatalf("AND of two properties: %v", got)
		}
		if got := list(repository.RawVersionQuery{Properties: []repository.RawVersionProperty{{Key: "channel", Value: "nightly"}}}); len(got) != 0 {
			t.Fatalf("no match: %v", got)
		}
		replaced, err := store.SetRawVersionProperties(ctx, repo.ID, "tool", "1.0.0", map[string]string{"only": "one"})
		if err != nil || len(replaced.Properties) != 1 || version(t, store, repo.ID, "tool", "1.0.0").Properties["channel"] != "" {
			t.Fatalf("properties are a full replace: %#v %v", replaced, err)
		}
		if _, err = store.SetRawVersionProperties(ctx, repo.ID, "tool", "7.7.7", nil); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("properties of an unknown version: %v", err)
		}
	})
	t.Run("create_is_idempotent_and_needs_the_versioned_layout", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		first, created, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", CreatedBy: "a"})
		if err != nil || !created {
			t.Fatal(created, err)
		}
		second, created, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0", CreatedBy: "b"})
		if err != nil || created || second.ID != first.ID || second.CreatedBy != "a" {
			t.Fatalf("replay: %#v %v %v", second, created, err)
		}
		if _, _, err = store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: repo.ID, Name: "Tool", Version: "1.0.0"}); !errors.Is(err, repository.ErrInvalidGenericPath) {
			t.Fatalf("invalid name: %v", err)
		}
		path, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "raw-path-" + uuid.NewString()[:8], Format: repository.FormatRaw})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: path.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, repository.ErrRawLayoutMismatch) {
			t.Fatalf("version in a path repository: %v", err)
		}
	})
	t.Run("path_repositories_keep_overwrite_semantics", func(t *testing.T) {
		store := factory(t)
		path, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "raw-path-" + uuid.NewString()[:8], Format: repository.FormatRaw})
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range []string{"one", "two"} {
			if _, err := store.PutRawAsset(ctx, asset(path.ID, "Any/Path", content)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.ListRawPackages(ctx, path.ID, "", 10); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("packages_versions_and_keyset_pages", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		seal := func(name, ver string) repository.RawVersion {
			mustPut(t, store, repo.ID, name+"/"+ver+"/f", name+ver)
			sealed, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: name, Version: ver})
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(3 * time.Millisecond)
			return sealed
		}
		seal("alpha", "1.0.0")
		seal("alpha", "3.0.0")
		newest := seal("alpha", "2.0.0")
		mustPut(t, store, repo.ID, "alpha/4.0.0-rc1/f", "open")
		seal("beta", "0.1.0")
		mustPut(t, store, repo.ID, "gamma/1.0.0/f", "only open")
		packages, err := store.ListRawPackages(ctx, repo.ID, "", 10)
		if err != nil || len(packages) != 3 {
			t.Fatalf("packages: %#v %v", packages, err)
		}
		if packages[0] != (repository.RawPackage{Name: "alpha", LatestVersion: "2.0.0", VersionCount: 4}) || packages[1] != (repository.RawPackage{Name: "beta", LatestVersion: "0.1.0", VersionCount: 1}) || packages[2] != (repository.RawPackage{Name: "gamma", VersionCount: 1}) {
			t.Fatalf("package summaries: %#v", packages)
		}
		if page, err := store.ListRawPackages(ctx, repo.ID, "alpha", 1); err != nil || len(page) != 1 || page[0].Name != "beta" {
			t.Fatalf("package keyset page: %#v %v", page, err)
		}
		if newest.SealedAt.IsZero() {
			t.Fatal("sealed_at must be set")
		}
		var order []string
		var after *repository.RawVersionCursor
		for {
			page, err := store.ListRawVersions(ctx, repository.RawVersionQuery{RepositoryID: repo.ID, Name: "alpha", Limit: 2, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range page {
				order = append(order, v.Version)
			}
			if len(page) < 2 {
				break
			}
			last := page[len(page)-1]
			after = &repository.RawVersionCursor{SealedAt: last.SealedAt, ID: last.ID}
		}
		if !slices.Equal(order, []string{"2.0.0", "3.0.0", "1.0.0", "4.0.0-rc1"}) {
			t.Fatalf("newest seal first, unsealed last: %v", order)
		}
		sealedOnly, err := store.ListRawVersions(ctx, repository.RawVersionQuery{RepositoryID: repo.ID, Name: "alpha", State: repository.RawVersionSealed})
		if err != nil || len(sealedOnly) != 3 {
			t.Fatalf("state filter: %#v %v", sealedOnly, err)
		}
		if _, err = store.GetRawVersion(ctx, repo.ID, "alpha", "0.0.0"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("stale_open_versions_are_reported_never_removed", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, nil)
		mustPut(t, store, repo.ID, "tool/1.0.0/f", "stale")
		mustPut(t, store, repo.ID, "tool/2.0.0/f", "stale too")
		if _, err := store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "2.0.0"}); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		stale, err := store.ListRawVersions(ctx, repository.RawVersionQuery{RepositoryID: repo.ID, Name: "tool", State: repository.RawVersionOpen, OpenCreatedBefore: future})
		if err != nil || len(stale) != 1 || stale[0].Version != "1.0.0" {
			t.Fatalf("stale filter: %#v %v", stale, err)
		}
		if none, err := store.ListRawVersions(ctx, repository.RawVersionQuery{RepositoryID: repo.ID, Name: "tool", State: repository.RawVersionOpen, OpenCreatedBefore: time.Now().Add(-time.Hour)}); err != nil || len(none) != 0 {
			t.Fatalf("nothing is that old: %#v %v", none, err)
		}
		stats, err := store.RawOpenVersionStats(ctx, future)
		if err != nil || stats.Stale < 1 || stats.OldestCreatedAt.IsZero() {
			t.Fatalf("stats: %#v %v", stats, err)
		}
		if sealed, err := store.SealIdleRawVersions(ctx, future, 100); err != nil || len(sealed) != 0 {
			t.Fatalf("an explicit-mode repository is never swept: %#v %v", sealed, err)
		}
		if v := version(t, store, repo.ID, "tool", "1.0.0"); v.State != repository.RawVersionOpen {
			t.Fatalf("the stale version must stay open: %#v", v)
		}
	})
	t.Run("idle_sweep_seals_only_idle_versions_of_idle_repositories", func(t *testing.T) {
		store := factory(t)
		idle := newRepo(t, store, func(r *repository.HostedRepository) { r.SealMode = repository.RawSealIdle })
		mustPut(t, store, idle.ID, "tool/1.0.0/f", "idle")
		mustPut(t, store, idle.ID, "tool/2.0.0/f", "idle two")
		if _, _, err := store.CreateRawVersion(ctx, repository.RawVersion{RepositoryID: idle.ID, Name: "tool", Version: "3.0.0"}); err != nil {
			t.Fatal(err)
		}
		if sealed, err := store.SealIdleRawVersions(ctx, time.Now().Add(-time.Hour), 100); err != nil || len(sealed) != 0 {
			t.Fatalf("nothing was idle an hour ago: %#v %v", sealed, err)
		}
		sealed, err := store.SealIdleRawVersions(ctx, time.Now().Add(time.Minute), 1)
		if err != nil || len(sealed) != 1 {
			t.Fatalf("the limit bounds one sweep: %#v %v", sealed, err)
		}
		rest, err := store.SealIdleRawVersions(ctx, time.Now().Add(time.Minute), 100)
		if err != nil || len(rest) != 1 {
			t.Fatalf("second sweep: %#v %v", rest, err)
		}
		for _, v := range append(sealed, rest...) {
			if v.State != repository.RawVersionSealed || v.ManifestDigest == "" {
				t.Fatalf("swept version: %#v", v)
			}
		}
		if empty := version(t, store, idle.ID, "tool", "3.0.0"); empty.State != repository.RawVersionOpen {
			t.Fatalf("an empty version is never sealed: %#v", empty)
		}
	})
	t.Run("idle_sweep_leaves_a_version_with_an_open_upload", func(t *testing.T) {
		store := factory(t)
		repo := newRepo(t, store, func(r *repository.HostedRepository) { r.SealMode = repository.RawSealIdle })
		mustPut(t, store, repo.ID, "tool/1.0.0/first", "first")
		upload, err := store.CreateRawUpload(ctx, repository.RawUpload{ID: uuid.NewString(), RepositoryID: repo.ID, Path: "tool/1.0.0/slow", ObjectKey: "native/raw/uploads/" + uuid.NewString(), State: "open", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Minute)
		if sealed, err := store.SealIdleRawVersions(ctx, future, 10); err != nil || len(sealed) != 0 {
			t.Fatalf("a version whose upload has not finished must stay open: %#v %v", sealed, err)
		}
		if _, err = store.CancelRawUpload(ctx, upload.ID); err != nil {
			t.Fatal(err)
		}
		if sealed, err := store.SealIdleRawVersions(ctx, future, 10); err != nil || len(sealed) != 1 {
			t.Fatalf("with the upload gone the version is idle: %#v %v", sealed, err)
		}
	})
	t.Run("manifest_is_canonical", func(t *testing.T) {
		files := []repository.RawAsset{
			{Path: "tool/1.0.0/b <&>", Digest: "sha256:" + strings.Repeat("b", 64), Size: 2},
			{Path: "tool/1.0.0/a", Digest: "sha256:" + strings.Repeat("a", 64), Size: 1},
		}
		manifest, digest := repository.RawVersionManifest("tool", "1.0.0", files, map[string]string{"z": "1", "a": "2"})
		want := `{"files":[{"path":"a","sha256":"` + strings.Repeat("a", 64) + `","size":1},{"path":"b <&>","sha256":"` + strings.Repeat("b", 64) + `","size":2}],"name":"tool","properties":{"a":"2","z":"1"},"version":"1.0.0"}`
		if string(manifest) != want {
			t.Fatalf("manifest:\n%s\nwant:\n%s", manifest, want)
		}
		sum := sha256.Sum256([]byte(want))
		if digest != fmt.Sprintf("sha256:%x", sum) {
			t.Fatalf("digest %s", digest)
		}
		reordered, again := repository.RawVersionManifest("tool", "1.0.0", []repository.RawAsset{files[1], files[0]}, map[string]string{"a": "2", "z": "1"})
		if string(reordered) != want || again != digest {
			t.Fatal("input order must not change the manifest")
		}
		if _, other := repository.RawVersionManifest("tool", "1.0.0", files, map[string]string{"a": "2", "z": "2"}); other == digest {
			t.Fatal("a property change must change the digest")
		}
		if checksums := repository.RawVersionChecksums("tool", "1.0.0", files); checksums != strings.Repeat("a", 64)+"  a\n"+strings.Repeat("b", 64)+"  b <&>\n" {
			t.Fatalf("checksums: %q", checksums)
		}
	})
}
