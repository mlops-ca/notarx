package config

import (
	"strings"
	"testing"
)

const legacyPrefix = "GATEWAY" + "_"

func TestRefuseLegacyEnvNamesEveryReplacement(t *testing.T) {
	for legacy, replacement := range legacyEnvNames {
		if !strings.HasPrefix(legacy, legacyPrefix) || !strings.HasPrefix(replacement, "NOTARX_") {
			t.Fatalf("legacy entry %q -> %q has the wrong prefix", legacy, replacement)
		}
		if legacyPrefix+strings.TrimPrefix(replacement, "NOTARX_") != legacy {
			t.Fatalf("legacy entry %q is not a 1:1 rename to %q", legacy, replacement)
		}
		err := RefuseLegacyEnv([]string{"PATH=/bin", legacy + "=secret-value"})
		if err == nil {
			t.Fatalf("RefuseLegacyEnv(%s) = nil, want refusal", legacy)
		}
		if !strings.Contains(err.Error(), legacy+" was renamed to "+replacement) {
			t.Fatalf("RefuseLegacyEnv(%s) = %v, want replacement %s", legacy, err, replacement)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("RefuseLegacyEnv(%s) leaked the value: %v", legacy, err)
		}
	}
}

func TestLegacyEnvListCoversKeyNames(t *testing.T) {
	for _, name := range []string{"INSTANCE_ID", "SETTINGS_ENCRYPTION_KEY", "DATABASE_URL", "ADMIN_TOKEN", "RESOLVER_TOKEN", "APT_SIGNER_TOKEN", "SCANNER_ENDPOINT"} {
		if _, ok := legacyEnvNames[legacyPrefix+name]; !ok {
			t.Errorf("legacy list is missing %s%s", legacyPrefix, name)
		}
	}
}

func TestRefuseLegacyEnvReportsAllNamesSorted(t *testing.T) {
	err := RefuseLegacyEnv([]string{legacyPrefix + "DATABASE_URL=x", legacyPrefix + "ADMIN_TOKEN=y"})
	if err == nil {
		t.Fatal("RefuseLegacyEnv() = nil, want refusal")
	}
	msg := err.Error()
	admin := strings.Index(msg, legacyPrefix+"ADMIN_TOKEN was renamed to NOTARX_ADMIN_TOKEN")
	database := strings.Index(msg, legacyPrefix+"DATABASE_URL was renamed to NOTARX_DATABASE_URL")
	if admin < 0 || database < 0 || admin > database {
		t.Fatalf("message = %q, want both names sorted", msg)
	}
}

func TestRefuseLegacyEnvIgnoresUnrelatedVariables(t *testing.T) {
	environ := []string{
		legacyPrefix + "SOMETHING_ELSE=1",
		legacyPrefix + "=1",
		"MY" + legacyPrefix + "DATABASE_URL=1",
		legacyPrefix + "DATABASE_URL_EXTRA=1",
		"NOTARX_DATABASE_URL=postgres://x",
		"NOTARX_SERVICE_HOST=10.0.0.1",
		"NOTARX_PORT=tcp://10.0.0.1:8080",
		"malformed-entry",
	}
	if err := RefuseLegacyEnv(environ); err != nil {
		t.Fatalf("RefuseLegacyEnv() = %v, want nil for unrelated variables", err)
	}
}

func TestLoadRefusesLegacyEnvironment(t *testing.T) {
	setValidNativeEnv(t)
	t.Setenv(legacyPrefix+"ADMIN_TOKEN", "legacy")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "renamed to NOTARX_ADMIN_TOKEN") {
		t.Fatalf("Load() error = %v, want renamed to NOTARX_ADMIN_TOKEN", err)
	}
}

func TestLoadIgnoresKubernetesServiceLinkVariables(t *testing.T) {
	setValidNativeEnv(t)
	t.Setenv("NOTARX_SERVICE_HOST", "10.0.0.1")
	t.Setenv("NOTARX_PORT", "tcp://10.0.0.1:8080")
	t.Setenv("NOTARX_SERVICE_PORT", "8080")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want service-link variables ignored", err)
	}
	for _, name := range []string{"NOTARX_SERVICE_HOST", "NOTARX_PORT"} {
		if !IgnoredServiceLinkEnv(name) {
			t.Fatalf("IgnoredServiceLinkEnv(%s) = false", name)
		}
	}
	if IgnoredServiceLinkEnv("NOTARX_DATABASE_URL") {
		t.Fatal("IgnoredServiceLinkEnv(NOTARX_DATABASE_URL) = true")
	}
}

func setValidNativeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NOTARX_DATABASE_URL", "postgres://notarx:password@db:5432/notarx")
	t.Setenv("NOTARX_OBJECT_STORE_ENDPOINT", "http://rustfs:9000")
	t.Setenv("NOTARX_OBJECT_STORE_BUCKET", "notarx-cache")
	t.Setenv("NOTARX_OBJECT_STORE_ACCESS_KEY", "rustfs-user")
	t.Setenv("NOTARX_OBJECT_STORE_SECRET_KEY", "rustfs-secret")
	t.Setenv("NOTARX_ADMIN_TOKEN", "admin-token")
	t.Setenv("NOTARX_RESOLVER_TOKEN", "resolver-token")
}

func TestRefuseLegacyEnvNamesObjectStoreReplacements(t *testing.T) {
	for legacy, replacement := range renamedObjectStoreEnv {
		if !strings.HasPrefix(replacement, "NOTARX_OBJECT_STORE_") {
			t.Fatalf("%s -> %s is not an object store key", legacy, replacement)
		}
		err := RefuseLegacyEnv([]string{legacy + "=secret-value"})
		if err == nil || !strings.Contains(err.Error(), legacy+" was renamed to "+replacement) {
			t.Fatalf("RefuseLegacyEnv(%s) = %v, want replacement %s", legacy, err, replacement)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("RefuseLegacyEnv(%s) leaked the value", legacy)
		}
	}
	if len(renamedObjectStoreEnv) != 8 {
		t.Fatalf("renamedObjectStoreEnv has %d entries, want 8", len(renamedObjectStoreEnv))
	}
}
