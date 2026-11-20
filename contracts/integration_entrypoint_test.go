package contracts

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Integration-tagged persistence tests must run in the same required CI command
// as the existing database lifecycle suites, rather than only on manual invocations.
func TestRequiredIntegrationCommandCoversIntegrationTaggedPackages(t *testing.T) {
	// scripts/integration-test.sh (make integration-test, the core-integration lanes)
	// runs every package of scripts/integration-packages.txt, one shard per lane.
	script, err := os.ReadFile(filepath.Join("..", "scripts", "integration-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// scripts/integration-isolation-test.sh (make test) checks what the script runs.
	if !strings.Contains(string(script), "scripts/integration-packages.txt") {
		t.Fatal("scripts/integration-test.sh must run the packages of scripts/integration-packages.txt")
	}
	list, err := os.ReadFile(filepath.Join("..", "scripts", "integration-packages.txt"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]int{}
	shards := map[string]bool{}
	for number, line := range strings.Split(string(list), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimLeft(fields[0], "0123456789") != "" || !strings.HasPrefix(fields[1], "./") {
			t.Fatalf("scripts/integration-packages.txt line %d is not \"<shard> <package>\": %q", number+1, line)
		}
		shards[fields[0]] = true
		listed[fields[1]]++
	}
	for shard := 1; shard <= len(shards); shard++ {
		if !shards[strconv.Itoa(shard)] {
			t.Errorf("shards must be numbered 1 to %d without gaps; %d is missing", len(shards), shard)
		}
	}
	baseline := build.Default
	baseline.GOOS = "linux"
	baseline.GOARCH = "amd64"
	integration := baseline
	integration.BuildTags = []string{"integration"}
	required := map[string]bool{}
	walk := func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		enabled, err := integration.MatchFile(dir, entry.Name())
		if err != nil {
			return err
		}
		ordinary, err := baseline.MatchFile(dir, entry.Name())
		if err != nil {
			return err
		}
		if enabled && !ordinary {
			relative, err := filepath.Rel("..", dir)
			if err != nil {
				return err
			}
			required["./"+filepath.ToSlash(relative)] = true
		}
		return nil
	}
	for _, root := range []string{"cmd", "internal"} {
		if err := filepath.WalkDir(filepath.Join("..", root), walk); err != nil {
			t.Fatal(err)
		}
	}
	if len(required) == 0 {
		t.Fatal("integration-tagged packages were not discovered")
	}
	for pkg := range required {
		if listed[pkg] != 1 {
			t.Errorf("%s has integration-tagged tests and must be listed exactly once in scripts/integration-packages.txt, not %d times", pkg, listed[pkg])
		}
	}
	for pkg := range listed {
		if !required[pkg] {
			t.Errorf("scripts/integration-packages.txt lists %s, which has no integration-tagged tests", pkg)
		}
	}
}
