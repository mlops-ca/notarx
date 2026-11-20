package repository

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The mutating node evicts its own read caches synchronously only through the store methods that run
// invalidateLocal (ADR 0023 section 6). A new writer of a cached table outside them would leave the writing
// node serving stale entries until its own NOTIFY arrives, so every writer is listed here with the reason it
// needs no local eviction.
func TestCachedTablesHaveNoOtherWriters(t *testing.T) {
	allowed := []string{
		"internal/repository/postgres_hosted.go",        // management methods wrapped with invalidateLocal
		"internal/repository/postgres_public_access.go", // ReplacePublicAccessPolicy evicts the policy scope
		"internal/repositorypreset/pgstore/store.go",    // first-install presets run before the caches start
		"internal/secrets/reseal/reseal.go",             // notarxctl secrets reseal runs in its own process
		"internal/testsupport/purge.go",                 // test support
	}
	writes := regexp.MustCompile(`(?i)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(public\.)?(hosted_repositories|hosted_groups|hosted_group_members|public_access_policy)\b`)
	root := filepath.Join("..", "..")
	var found []string
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if writes.Match(body) {
				rel, _ := filepath.Rel(root, path)
				found = append(found, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(found, "internal/repository/postgres_hosted.go") {
		t.Fatalf("the scan found no write in postgres_hosted.go; the pattern no longer matches: %v", found)
	}
	for _, path := range found {
		if !slices.Contains(allowed, path) {
			t.Errorf("%s writes a read-cached table outside the store methods that evict locally", path)
		}
	}
}
