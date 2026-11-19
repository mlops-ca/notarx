package nuget

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

// TestNuGetPreviewRevalidationRollsBackWithItsAudit proves ADR 0023 section 1 for the source
// revalidation handler: a failing audit fails the revalidation itself and the authority keeps its
// repository version, and the same request succeeds once audits work again.
func TestNuGetPreviewRevalidationRollsBackWithItsAudit(t *testing.T) {
	body := packageFixture(t, "Acme.Audit", "1.0.0", "")
	feed := newTestFeed(t, body)
	f, proxy := proxyFixture(t, feed)
	group := testNuGetGroup(t, f, proxy, f.repo)
	if w := nativeGet(t, f.module, group.Name+"/v3/flat/acme.audit/1.0.0/acme.audit.1.0.0.nupkg"); w.Code != 200 {
		t.Fatalf("cold authority: %d %s", w.Code, w.Body.String())
	}
	prior := proxy.Version
	proxy, err := f.store.UpdateHostedRepository(t.Context(), proxy, proxy.Version)
	if err != nil {
		t.Fatal(err)
	}
	revalidate := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://gateway.test/api/v2/previews/nuget/groups/"+group.ID+"/sources/acme.audit/1.0.0/revalidate", strings.NewReader(fmt.Sprintf(`{"groupVersion":%q,"repositoryVersion":%q}`, group.Version, prior)))
		r.Header.Set("Authorization", "Bearer nuget-test-admin")
		w := httptest.NewRecorder()
		f.module.Preview().ServeHTTP(w, r)
		return w
	}
	f.store.SetAuditFault(func(record repository.AuditRecord) error {
		if record.Operation == "nuget.preview.source.revalidate" {
			return errors.New("audit store unavailable")
		}
		return nil
	})
	w := revalidate()
	if w.Code < 400 || strings.Contains(w.Body.String(), "audit_failed") {
		t.Fatalf("revalidation with a failing audit: %d %s", w.Code, w.Body.String())
	}
	owner, err := f.store.GetNuGetGroupAuthority(t.Context(), group.ID, "acme.audit", "1.0.0")
	if err != nil || owner.RepositoryVersion != prior {
		t.Fatalf("failed revalidation moved the authority: %+v %v", owner, err)
	}
	f.store.SetAuditFault(nil)
	if w = revalidate(); w.Code != 200 {
		t.Fatalf("revalidation with a working audit: %d %s", w.Code, w.Body.String())
	}
	if owner, err = f.store.GetNuGetGroupAuthority(t.Context(), group.ID, "acme.audit", "1.0.0"); err != nil || owner.RepositoryVersion != proxy.Version {
		t.Fatalf("revalidation did not move the authority: %+v %v", owner, err)
	}
}
