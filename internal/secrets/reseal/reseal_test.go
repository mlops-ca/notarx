package reseal

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTargetsCoverSealedColumns fails when the baseline schema gains a column
// that looks sealed but is not resealed, which would block dropping a key.
func TestTargetsCoverSealedColumns(t *testing.T) {
	baseline, err := os.ReadFile("../../../migrations/000001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	sealedColumn := regexp.MustCompile(`(?m)^\s+(\w*ciphertext|client_secret)\s+text`)
	found := sealedColumn.FindAllStringSubmatch(string(baseline), -1)
	if len(found) == 0 {
		t.Fatal("no sealed columns found in the baseline; the pattern is stale")
	}
	var names []string
	for _, target := range Targets() {
		names = append(names, target.Name)
	}
	joined := strings.Join(names, "\n")
	for _, match := range found {
		if !strings.Contains(joined, "."+match[1]) {
			t.Errorf("sealed column %s is not covered by reseal.Targets", match[1])
		}
	}
}

func TestPurposesMatchCallers(t *testing.T) {
	want := map[string]string{
		"email_targets.recipient_ciphertext":            "email-target:X",
		"email_test_deliveries.recipient_ciphertext":    "email-target:X",
		"nexus_migration_sources.credential_ciphertext": "nexus-source:X",
		"oidc_settings.client_secret":                   "oidc.client-secret",
		"webhook_subscriptions.secret_ciphertext":       "webhook-subscription:X",
		"hosted_repositories.upstream_auth.secret":      "repository-upstream-auth",
	}
	for _, target := range Targets() {
		if got := target.Purpose("X"); got != want[target.Name] {
			t.Errorf("%s purpose=%q want %q", target.Name, got, want[target.Name])
		}
		delete(want, target.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing targets: %v", want)
	}
}
