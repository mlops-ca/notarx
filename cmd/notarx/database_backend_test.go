package main

import (
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/config"
)

func TestSelectDatabaseBackendKeepsPostgreSQLTheDefault(t *testing.T) {
	for _, driver := range []string{"", "postgres"} {
		set, err := selectDatabaseBackend(config.Config{DatabaseDriver: driver})
		if err != nil || set.Name != "postgres" || set.Locker == nil || set.Classifier == nil {
			t.Fatalf("driver %q: %#v %v", driver, set, err)
		}
	}
}

func TestSelectDatabaseBackendRefusesAnIncompleteMySQLBackend(t *testing.T) {
	_, err := selectDatabaseBackend(config.Config{DatabaseDriver: "mysql"})
	if err == nil || !strings.Contains(err.Error(), "no MySQL implementation for") || !strings.Contains(err.Error(), "RawStore") {
		t.Fatalf("error = %v", err)
	}
	if _, err := selectDatabaseBackend(config.Config{DatabaseDriver: "oracle"}); err == nil {
		t.Fatal("unknown driver accepted")
	}
}
