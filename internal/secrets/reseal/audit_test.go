package reseal

import (
	"errors"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/secrets"
)

func TestAuditRecordHoldsKeyIDsAndCountsNeverKeyMaterial(t *testing.T) {
	const keyA, keyB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	t.Setenv(secrets.KeyEnv, keyB)
	t.Setenv(secrets.KeyPreviousEnv, keyA)
	current, previous, err := secrets.KeyIDs()
	if err != nil || len(previous) != 1 {
		t.Fatalf("%v %v", previous, err)
	}
	record, err := auditRecord([]Result{{Target: "a.b", Resealed: 2, Unchanged: 1, Failed: 1}}, ErrRowsFailed)
	if err != nil {
		t.Fatal(err)
	}
	if record.Actor != Actor || record.Operation != Operation {
		t.Fatalf("%+v", record)
	}
	want := map[string]string{"current_key_id": current, "previous_key_ids": previous[0], "status": "rows_failed", "table:a.b": "resealed=2 skipped=2 failed=1"}
	for key, value := range want {
		if record.Evidence[key] != value {
			t.Fatalf("%s=%q want %q", key, record.Evidence[key], value)
		}
	}
	if record.Evidence["os_user"] == "" || record.Evidence["host"] == "" {
		t.Fatalf("%v", record.Evidence)
	}
	for _, value := range record.Evidence {
		if strings.Contains(value, keyA) || strings.Contains(value, keyB) {
			t.Fatal("key material in audit")
		}
	}
	aborted, _ := auditRecord(nil, errors.New("boom"))
	if aborted.Evidence["status"] != "aborted" {
		t.Fatalf("%v", aborted.Evidence)
	}
}

func TestSigningKeyScopeNamesItsColumnsAndItsAuditRecord(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	for name, want := range map[string]bool{
		"token_signing_keys.private_sealed":        true,
		macTargetName:                              true,
		"identity_providers.secrets_sealed":        false,
		"hosted_repositories.upstream_auth.secret": false,
	} {
		if SigningKeys.Includes(name) != want {
			t.Errorf("SigningKeys.Includes(%q) = %v, want %v", name, !want, want)
		}
		if !Everything.Includes(name) {
			t.Errorf("Everything must include %q", name)
		}
	}
	record, err := scopedAuditRecord(SigningKeys, []Result{{Target: "token_signing_keys.private_sealed", Resealed: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if record.Actor != "cli:issuer-reseal" || record.Operation != "issuer.reseal" || record.Resource != "issuer" || record.Evidence["status"] != "complete" {
		t.Fatalf("%+v", record)
	}
	for _, target := range Targets() {
		if SigningKeys.Includes(target.Name) != strings.HasPrefix(target.Name, "token_signing_keys.") {
			t.Fatalf("scope disagrees about %s", target.Name)
		}
	}
}
