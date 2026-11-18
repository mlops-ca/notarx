package testsupport

import (
	"database/sql"
	"testing"
)

// PurgeRepository removes a test repository with everything that references it, so a test
// leaves no rows that other integration tests (artifact search, raw object listings)
// would see. Tables are cleared by repository_id in repeated passes because the foreign
// keys between them have no common order.
func PurgeRepository(t testing.TB, db *sql.DB, repositoryID string) {
	t.Helper()
	rows, err := db.Query(`SELECT table_name FROM information_schema.columns WHERE table_schema='public' AND column_name='repository_id' AND table_name NOT IN ('hosted_repositories') ORDER BY table_name`)
	if err != nil {
		t.Logf("purge %s: %v", repositoryID, err)
		return
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}
	_ = rows.Close()
	for pass := 0; pass < 8; pass++ {
		failed := false
		for _, table := range tables {
			if _, err = db.Exec(`DELETE FROM public.`+table+` WHERE repository_id=$1::uuid`, repositoryID); err != nil {
				failed = true
			}
		}
		if !failed {
			break
		}
	}
	if _, err = db.Exec(`DELETE FROM hosted_repositories WHERE id=$1::uuid`, repositoryID); err != nil {
		t.Logf("purge %s: %v", repositoryID, err)
	}
}
