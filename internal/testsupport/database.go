package testsupport

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateIsolatedDatabaseURL prevents integration tests from writing fixtures
// into a development or operator-visible notarx database.
func ValidateIsolatedDatabaseURL(raw string) error {
	return validateIsolated(EnvPostgresURL, raw)
}

// validateIsolated accepts the postgres:// and mysql:// forms: both name the database in the path.
// The error never echoes the URL, so a password cannot leak into a test log.
func validateIsolated(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is invalid", name)
	}
	databaseName := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	if databaseName == "" || !strings.HasSuffix(databaseName, "_test") {
		return fmt.Errorf("%s must select a database whose name ends in _test", name)
	}
	return nil
}
