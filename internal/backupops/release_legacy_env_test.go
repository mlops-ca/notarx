package backupops

import "testing"

const legacy = "GATEWAY" + "_"

func TestLegacyEnvironmentKeyMapsObjectStoreSettingsToTheOriginalNames(t *testing.T) {
	for key, want := range map[string]string{
		"NOTARX_OBJECT_STORE_ENDPOINT":           legacy + "RUSTFS_ENDPOINT",
		"NOTARX_OBJECT_STORE_BUCKET":             legacy + "RUSTFS_BUCKET",
		"NOTARX_OBJECT_STORE_ACCESS_KEY":         legacy + "RUSTFS_ACCESS_KEY",
		"NOTARX_OBJECT_STORE_SECRET_KEY":         legacy + "RUSTFS_SECRET_KEY",
		"NOTARX_DATABASE_URL":                    legacy + "DATABASE_URL",
		"NOTARX_NODE_ROLES":                      legacy + "NODE_ROLES",
		"NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET": legacy + "OBJECT_STORE_AUTO_CREATE_BUCKET",
	} {
		if got := legacyEnvironmentKey(key); got != want {
			t.Errorf("legacyEnvironmentKey(%s) = %s, want %s", key, got, want)
		}
	}
}
