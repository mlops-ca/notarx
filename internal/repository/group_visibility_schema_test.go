package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoDerivedMemberVisibilityIsStored guards ADR 0029: a group's member
// visibility is computed at request time, so no migration may store it. No
// member table may gain a visibility or anonymous column after the baseline.
func TestNoDerivedMemberVisibilityIsStored(t *testing.T) {
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	memberColumn := regexp.MustCompile(`(?is)alter\s+table\s+(\w+_members)\s+add\s+column\s+(?:if\s+not\s+exists\s+)?(anonymous|visibility\w*)`)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(filepath.Base(file), "000001_baseline") {
			continue
		}
		if match := memberColumn.FindStringSubmatch(string(data)); match != nil {
			t.Errorf("%s adds %s to %s: group member visibility must stay computed", filepath.Base(file), match[2], match[1])
		}
	}
}
