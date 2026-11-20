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
// member table of the baseline carries a visibility or anonymous column and no
// later migration may add one.
func TestNoDerivedMemberVisibilityIsStored(t *testing.T) {
	files, err := filepath.Glob("../../migrations/postgres/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	memberColumn := regexp.MustCompile(`(?is)alter\s+table\s+(?:only\s+)?(?:public\.)?(\w+_members)\s+add\s+column\s+(?:if\s+not\s+exists\s+)?(anonymous|visibility\w*)`)
	memberTable := regexp.MustCompile(`(?s)CREATE TABLE public\.(\w+_members) \((.*?)\n\);`)
	derivedColumn := regexp.MustCompile(`(?m)^\s+(anonymous|visibility\w*)\s`)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if match := memberColumn.FindStringSubmatch(text); match != nil {
			t.Errorf("%s adds %s to %s: group member visibility must stay computed", filepath.Base(file), match[2], match[1])
		}
		for _, table := range memberTable.FindAllStringSubmatch(text, -1) {
			if column := derivedColumn.FindStringSubmatch(table[2]); column != nil {
				t.Errorf("%s: %s stores %s: group member visibility must stay computed", filepath.Base(file), table[1], column[1])
			}
		}
		if strings.Contains(filepath.Base(file), "000001_baseline") && !memberTable.MatchString(text) {
			t.Error("the baseline has no member table: the guard no longer matches the dump format")
		}
	}
}
