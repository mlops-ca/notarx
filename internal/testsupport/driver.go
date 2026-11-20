package testsupport

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The environment of the integration suites (ADR 0022, slice 5). TEST_DATABASE_DRIVER selects
// the engine the dialect-parametrised suites run against; PostgreSQL is the default and its
// variables are unchanged. TEST_DATABASE_URL is the migrated PostgreSQL, TEST_MYSQL_URL the
// migrated MySQL. scripts/integration-test.sh --driver mysql leaves TEST_DATABASE_URL empty, so
// the PostgreSQL-only suites skip themselves instead of dialling PostgreSQL with a MySQL URL.
const (
	EnvDriver      = "TEST_DATABASE_DRIVER"
	EnvPostgresURL = "TEST_DATABASE_URL"
	EnvMySQLURL    = "TEST_MYSQL_URL"
	// EnvRequired makes a missing database a failure instead of a skip. The MySQL lane of
	// scripts/integration-test.sh sets it, so a suite that silently skipped cannot pass for green.
	EnvRequired = "TEST_DATABASE_REQUIRED"
)

// Engine names, equal to the NOTARX_DATABASE_DRIVER values.
const (
	DriverPostgres = "postgres"
	DriverMySQL    = "mysql"
)

var urlVariables = map[string]string{DriverPostgres: EnvPostgresURL, DriverMySQL: EnvMySQLURL}

// Driver returns the engine selected by TEST_DATABASE_DRIVER: postgres when it is unset.
func Driver(getenv func(string) string) (string, error) {
	switch driver := strings.TrimSpace(getenv(EnvDriver)); driver {
	case "", DriverPostgres:
		return DriverPostgres, nil
	case DriverMySQL:
		return DriverMySQL, nil
	default:
		return "", fmt.Errorf("%s must be postgres or mysql, not %q", EnvDriver, driver)
	}
}

// URL returns the connection URL of an engine, empty when the suite has none for it.
func URL(getenv func(string) string, driver string) string {
	return getenv(urlVariables[driver])
}

// ValidateIsolatedEnvironment checks the engine name and every configured database URL, so a
// suite never writes fixtures into a development or operator-visible database on either engine.
// Packages call it from TestMain.
func ValidateIsolatedEnvironment(getenv func(string) string) error {
	if _, err := Driver(getenv); err != nil {
		return err
	}
	for _, name := range []string{EnvPostgresURL, EnvMySQLURL} {
		if err := validateIsolated(name, getenv(name)); err != nil {
			return err
		}
	}
	return nil
}

// resolve returns the selected engine and its URL. missing names the variable that is not set
// when the suite has no database to run against.
func resolve(getenv func(string) string) (driver, url, missing string, err error) {
	driver, err = Driver(getenv)
	if err != nil {
		return "", "", "", err
	}
	url = URL(getenv, driver)
	if url == "" {
		return driver, "", urlVariables[driver], nil
	}
	return driver, url, "", validateIsolated(urlVariables[driver], url)
}

// Database returns the engine and URL a dialect-parametrised suite runs against. A selected
// engine without a database skips the test, or fails it when TEST_DATABASE_REQUIRED=1.
func Database(t testing.TB) (driver, url string) {
	t.Helper()
	driver, url, missing, err := resolve(os.Getenv)
	switch {
	case err != nil:
		t.Fatal(err)
	case missing != "" && os.Getenv(EnvRequired) == "1":
		t.Fatalf("%s is required for the %s contract tests", missing, driver)
	case missing != "":
		t.Skipf("%s is required for the %s contract tests", missing, driver)
	}
	return driver, url
}

// RunPerDriver runs the suite of the selected engine as a subtest named after it. A package
// registers one function per engine it supports; the others are not run.
func RunPerDriver(t *testing.T, suites map[string]func(t *testing.T, databaseURL string)) {
	t.Helper()
	driver, url := Database(t)
	suite, ok := suites[driver]
	switch {
	case !ok && os.Getenv(EnvRequired) == "1":
		t.Fatalf("this suite has no %s implementation, and %s=1 does not allow a skip", driver, EnvRequired)
	case !ok:
		t.Skipf("this suite has no %s implementation yet", driver)
	}
	t.Run(driver, func(t *testing.T) { suite(t, url) })
}
