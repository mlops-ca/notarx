package testsupport

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/ids"
)

// FailSecurityAudits makes every insert into security_audit_log by the actor on the
// repository (use "" for a management audit, which names no repository) fail until the
// returned restore function runs or the test ends, so a test can prove a mutation rolls
// back with its audit (ADR 0023 section 1). Invalid UTF-8 no longer fails an audit
// insert: audit text is sanitised.
func FailSecurityAudits(t testing.TB, db *sql.DB, repositoryName, actor string) (restore func()) {
	t.Helper()
	name := "fail_audit_" + strings.ReplaceAll(ids.New(), "-", "")
	create := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.repository='%s' AND NEW.actor='%s' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON security_audit_log FOR EACH ROW EXECUTE FUNCTION %s()`,
		name, strings.ReplaceAll(repositoryName, "'", "''"), strings.ReplaceAll(actor, "'", "''"), name, name)
	if _, err := db.ExecContext(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		if _, err := db.Exec(`DROP TRIGGER ` + name + ` ON security_audit_log; DROP FUNCTION ` + name + `()`); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restore)
	return restore
}
