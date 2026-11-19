package app

import (
	"context"
	"errors"
	"testing"

	"github.com/mlops-ca/notarx/internal/nexusmigration"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestNexusMigrationPublicationRollsBackWithItsAudit proves ADR 0023 section 1 for the Raw and Maven
// publication on the memory store: the nexus.migration.publish audit is written with the publication,
// so a failing audit leaves the target unpublished, and the publication then succeeds with one audit.
func TestNexusMigrationPublicationRollsBackWithItsAudit(t *testing.T) {
	for _, format := range []repository.Format{repository.FormatRaw, repository.FormatMaven} {
		t.Run(string(format), func(t *testing.T) {
			store, _, publication, _, entry, bodies := nexusPublicationFixture(t, format)
			runNexusPublicationAuditRollback(t, publication, entry, bodies, func() {
				store.SetAuditFault(func(record repository.AuditRecord) error {
					if record.Operation == "nexus.migration.publish" {
						return errors.New("audit store unavailable")
					}
					return nil
				})
			}, func() { store.SetAuditFault(nil) })
			published := 0
			for _, record := range store.Audits {
				if record.Operation == "nexus.migration.publish" {
					published++
				}
			}
			if published != 1 {
				t.Fatalf("nexus.migration.publish audits=%d, want 1", published)
			}
		})
	}
}

// runNexusPublicationAuditRollback stages and verifies the entry, publishes it while audits fail and
// asserts nothing became visible, then publishes it with audits working.
func runNexusPublicationAuditRollback(t *testing.T, publication *NexusMigrationPublication, entry nexusmigration.Entry, bodies map[string][]byte, fail, clear func()) {
	t.Helper()
	ctx, release, err := publication.Lock(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	nexusPublicationStage(t, ctx, publication, &entry, bodies)
	if err = publication.Verify(ctx, entry); err != nil {
		t.Fatal(err)
	}
	fail()
	if err = publication.Publish(ctx, entry); err == nil {
		t.Fatal("published although its audit failed")
	}
	if state, readErr := publication.Readback(ctx, entry); readErr != nil || state != nexusmigration.Absent {
		t.Fatalf("publication took effect although its audit failed: state=%s err=%v", state, readErr)
	}
	clear()
	if err = publication.Publish(ctx, entry); err != nil {
		t.Fatalf("publish with a working audit: %v", err)
	}
	if state, readErr := publication.Readback(ctx, entry); readErr != nil || state != nexusmigration.Identical {
		t.Fatalf("published state=%s err=%v", state, readErr)
	}
}
