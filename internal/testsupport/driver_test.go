package testsupport

import (
	"strings"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestDriverDefaultsToPostgres(t *testing.T) {
	for value, want := range map[string]string{"": DriverPostgres, " postgres ": DriverPostgres, "mysql": DriverMySQL} {
		got, err := Driver(environment(map[string]string{EnvDriver: value}))
		if err != nil || got != want {
			t.Errorf("Driver(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	if _, err := Driver(environment(map[string]string{EnvDriver: "oracle"})); err == nil || !strings.Contains(err.Error(), "postgres or mysql") {
		t.Fatalf("an unknown driver was accepted: %v", err)
	}
}

func TestURLSelectsTheVariableOfTheEngine(t *testing.T) {
	env := environment(map[string]string{EnvPostgresURL: "postgres://x/a_test", EnvMySQLURL: "mysql://x/b_test"})
	if URL(env, DriverPostgres) != "postgres://x/a_test" || URL(env, DriverMySQL) != "mysql://x/b_test" || URL(env, "other") != "" {
		t.Fatal("URL returned the wrong variable")
	}
}

func TestValidateIsolatedEnvironmentCoversBothEngines(t *testing.T) {
	good := map[string]string{EnvPostgresURL: "postgres://u:secret@h:5432/notarx_test?sslmode=disable", EnvMySQLURL: "mysql://u:secret@h:3306/notarx_test?tls=required", EnvDriver: "mysql"}
	if err := ValidateIsolatedEnvironment(environment(good)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIsolatedEnvironment(environment(nil)); err != nil {
		t.Fatalf("an empty environment is valid: %v", err)
	}
	for name, mutate := range map[string]func(map[string]string){
		"mysql development database": func(m map[string]string) { m[EnvMySQLURL] = "mysql://u:secret@h:3306/notarx" },
		"postgres development":       func(m map[string]string) { m[EnvPostgresURL] = "postgres://u:secret@h:5432/notarx" },
		"unknown driver":             func(m map[string]string) { m[EnvDriver] = "sqlite" },
		"unparsable mysql":           func(m map[string]string) { m[EnvMySQLURL] = "mysql://u:secret@h:bad/notarx_test" },
	} {
		env := map[string]string{}
		for k, v := range good {
			env[k] = v
		}
		mutate(env)
		err := ValidateIsolatedEnvironment(environment(env))
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error leaks the password: %v", name, err)
		}
	}
}

func TestDatabaseAndRunPerDriver(t *testing.T) {
	t.Setenv(EnvDriver, "mysql")
	t.Setenv(EnvMySQLURL, "mysql://u:p@h:3306/notarx_test")
	driver, url := Database(t)
	if driver != DriverMySQL || url != "mysql://u:p@h:3306/notarx_test" {
		t.Fatalf("Database = %q %q", driver, url)
	}
	var ran []string
	RunPerDriver(t, map[string]func(*testing.T, string){
		DriverPostgres: func(*testing.T, string) { ran = append(ran, "postgres") },
		DriverMySQL:    func(_ *testing.T, u string) { ran = append(ran, "mysql "+u) },
	})
	if len(ran) != 1 || ran[0] != "mysql mysql://u:p@h:3306/notarx_test" {
		t.Fatalf("ran %v", ran)
	}
}

func TestRunPerDriverSkipsAnEngineWithoutSuiteOrDatabase(t *testing.T) {
	t.Run("no suite", func(t *testing.T) {
		t.Setenv(EnvDriver, "mysql")
		t.Setenv(EnvMySQLURL, "mysql://u:p@h:3306/notarx_test")
		RunPerDriver(t, map[string]func(*testing.T, string){DriverPostgres: func(*testing.T, string) { t.Error("ran postgres") }})
		if !t.Skipped() {
			t.Error("a suite-less engine was not skipped")
		}
	})
	t.Run("no database", func(t *testing.T) {
		t.Setenv(EnvDriver, "postgres")
		t.Setenv(EnvPostgresURL, "")
		RunPerDriver(t, map[string]func(*testing.T, string){DriverPostgres: func(*testing.T, string) { t.Error("ran without a URL") }})
		if !t.Skipped() {
			t.Error("a missing URL was not skipped")
		}
	})
}

func TestResolveReportsAMissingDatabase(t *testing.T) {
	driver, url, missing, err := resolve(environment(map[string]string{EnvDriver: "mysql"}))
	if err != nil || driver != DriverMySQL || url != "" || missing != EnvMySQLURL {
		t.Fatalf("resolve = %q %q %q %v", driver, url, missing, err)
	}
	if _, _, _, err := resolve(environment(map[string]string{EnvDriver: "x"})); err == nil {
		t.Error("a bad driver was accepted")
	}
	if _, _, _, err := resolve(environment(map[string]string{EnvMySQLURL: "mysql://u:p@h/dev", EnvDriver: "mysql"})); err == nil {
		t.Error("a development database was accepted")
	}
}
