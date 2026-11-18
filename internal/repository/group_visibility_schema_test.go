package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoDerivedMemberVisibilityIsStored guards ADR 0029: a group's member
// visibility is computed at request time, so no migration may store it. The
// legacy per-member anonymous flag is dropped by 000007 and no member table may
// gain a visibility or anonymous column afterwards.
func TestNoDerivedMemberVisibilityIsStored(t *testing.T) {
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	memberColumn := regexp.MustCompile(`(?is)alter\s+table\s+(\w+_members)\s+add\s+column\s+(?:if\s+not\s+exists\s+)?(anonymous|visibility\w*)`)
	var drops strings.Builder
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(filepath.Base(file), "000001_baseline") {
			continue
		}
		if match := memberColumn.FindStringSubmatch(text); match != nil {
			t.Errorf("%s adds %s to %s: group member visibility must stay computed", filepath.Base(file), match[2], match[1])
		}
		if strings.HasPrefix(filepath.Base(file), "000007_") {
			drops.WriteString(text)
		}
	}
	// A mixed legacy group must not widen when the per-member flag is dropped.
	for _, table := range []string{"oci", "maven", "raw", "conan"} {
		backfill := "UPDATE " + table + "_groups SET anonymous = false"
		if !strings.Contains(drops.String(), backfill) || strings.Index(drops.String(), backfill) > strings.Index(drops.String(), "ALTER TABLE "+table+"_group_members DROP COLUMN anonymous;") {
			t.Errorf("migration 000007 must turn a mixed %s group private before dropping the member flag", table)
		}
	}
	for _, table := range []string{"oci_group_members", "maven_group_members", "raw_group_members", "conan_group_members"} {
		if !strings.Contains(drops.String(), "ALTER TABLE "+table+" DROP COLUMN anonymous;") {
			t.Errorf("migration 000007 must drop the stored anonymous copy from %s", table)
		}
	}
}
