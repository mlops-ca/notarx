package config

import "testing"

func TestRepositoryPresetsModeIsExplicitAndDefaultsToStandard(t *testing.T) {
	t.Setenv("NOTARX_DATABASE_URL", "postgres://gateway:password@db:5432/gateway")
	t.Setenv("NOTARX_RUSTFS_ENDPOINT", "http://rustfs:9000")
	t.Setenv("NOTARX_RUSTFS_BUCKET", "gateway-cache")
	t.Setenv("NOTARX_RUSTFS_ACCESS_KEY", "rustfs-user")
	t.Setenv("NOTARX_RUSTFS_SECRET_KEY", "rustfs-secret")
	t.Setenv("NOTARX_ADMIN_TOKEN", "admin-token")
	t.Setenv("NOTARX_RESOLVER_TOKEN", "resolver-token")
	for _, test := range []struct {
		input, expected string
	}{
		{"", "standard"}, {"standard", "standard"}, {"none", "none"}, {" none ", "none"},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Setenv("NOTARX_REPOSITORY_PRESETS", test.input)
			cfg, err := Load()
			if err != nil || cfg.RepositoryPresetsMode != test.expected {
				t.Fatalf("mode=%q err=%v", cfg.RepositoryPresetsMode, err)
			}
		})
	}
	t.Setenv("NOTARX_REPOSITORY_PRESETS", "unknown-secret-value")
	if _, err := Load(); err == nil || err.Error() != "NOTARX_REPOSITORY_PRESETS must be standard or none" {
		t.Fatalf("invalid mode: %v", err)
	}
}
