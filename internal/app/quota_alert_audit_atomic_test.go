package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestQuotaAlertRuleMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the quota
// alert rule handlers on the memory store: a failing audit fails the change itself.
func TestQuotaAlertRuleMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runQuotaAlertAuditRollback(t, store, func() {
		store.SetAuditFault(func(record repository.AuditRecord) error {
			if strings.HasPrefix(record.Operation, "quota.rule.") {
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) }, func(string) {})
}

// quotaAlertRollbackStore is what the handlers under test and the assertions need.
type quotaAlertRollbackStore interface {
	GatewayStore
	repository.RepositoryQuotaAlertStore
}

// runQuotaAlertAuditRollback drives the create, update and delete handlers of the quota alert
// rules while audits fail and asserts the change did not take effect and the answer is never
// 500 audit_failed, then lets audits through and proves the same requests succeed and write
// exactly one audit each. purge removes the fixture repository and its rule from a shared database.
func runQuotaAlertAuditRollback(t *testing.T, store quotaAlertRollbackStore, fail, clear func(), purge func(repositoryID string)) {
	t.Helper()
	ctx := context.Background()
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "quota-audit-" + uuid.NewString()[:8], Format: repository.FormatRaw, Type: repository.RepositoryTypeHosted, State: repository.RepositoryActive})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { purge(repo.ID) })
	target, err := store.CreateEmailTarget(ctx, repository.EmailTarget{ID: uuid.NewString(), Name: "quota-audit-" + uuid.NewString()[:8], Locale: "en-CA", RecipientCiphertext: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	body := func(warning int) string {
		return fmt.Sprintf(`{"repositoryId":%q,"targetId":%q,"policy":{"warningBasisPoints":%d,"criticalBasisPoints":9500,"recoveryBelowBasisPoints":8000,"warningForSeconds":300,"criticalForSeconds":120,"recoveryForSeconds":180,"maxSampleAgeSeconds":60}}`, repo.ID, target.ID, warning)
	}
	activeRule := func() (repository.RepositoryQuotaAlertRule, bool) {
		rules, listErr := store.ListRepositoryQuotaAlertRules(ctx)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, rule := range rules {
			if rule.RepositoryID == repo.ID && !rule.Deleted {
				return rule, true
			}
		}
		return repository.RepositoryQuotaAlertRule{}, false
	}
	notAuditFailed := func(name string, status int, response string) {
		t.Helper()
		if status < 400 || strings.Contains(response, "audit_failed") {
			t.Fatalf("%s: status %d, want a failure that is not audit_failed: %s", name, status, response)
		}
	}

	fail()
	response := quotaRequest(handler, http.MethodPost, "/api/v2/repository-quota-alert-rules", body(8500), "")
	notAuditFailed("create", response.Code, response.Body.String())
	if _, found := activeRule(); found {
		t.Fatal("quota alert rule created although its audit failed")
	}
	clear()
	if response = quotaRequest(handler, http.MethodPost, "/api/v2/repository-quota-alert-rules", body(8500), ""); response.Code != http.StatusCreated {
		t.Fatalf("create after audits work again: %d %s", response.Code, response.Body.String())
	}
	var created struct{ ID string }
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	before, _ := activeRule()
	path := "/api/v2/repository-quota-alert-rules/" + created.ID

	fail()
	response = quotaRequest(handler, http.MethodPut, path, body(8600), before.Version)
	notAuditFailed("update", response.Code, response.Body.String())
	if got, _ := activeRule(); got.Version != before.Version || got.Policy != before.Policy {
		t.Fatalf("quota alert rule updated although its audit failed: %+v", got)
	}
	response = quotaRequest(handler, http.MethodDelete, path, "", before.Version)
	notAuditFailed("delete", response.Code, response.Body.String())
	if got, found := activeRule(); !found || got.Version != before.Version {
		t.Fatalf("quota alert rule deleted although its audit failed: %+v", got)
	}
	clear()

	if response = quotaRequest(handler, http.MethodPut, path, body(8600), before.Version); response.Code != http.StatusOK {
		t.Fatalf("update after audits work again: %d %s", response.Code, response.Body.String())
	}
	updated, _ := activeRule()
	if response = quotaRequest(handler, http.MethodDelete, path, "", updated.Version); response.Code != http.StatusNoContent {
		t.Fatalf("delete after audits work again: %d %s", response.Code, response.Body.String())
	}
	if _, found := activeRule(); found {
		t.Fatal("quota alert rule still active after delete")
	}
	records, err := store.ListAudits(ctx, repository.AuditQuery{Actor: "alice", Format: "management", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, record := range records {
		if record.Resource == created.ID {
			counts[record.Operation]++
		}
	}
	for _, operation := range []string{"quota.rule.create", "quota.rule.update", "quota.rule.delete"} {
		if counts[operation] != 1 {
			t.Fatalf("%s audits=%d, want 1 (%v)", operation, counts[operation], counts)
		}
	}
}
