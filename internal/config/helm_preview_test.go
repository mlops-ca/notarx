package config

import "testing"

func TestHelmPreviewRequiresExplicitTrueConfiguration(t *testing.T) {
	for key, value := range map[string]string{"NOTARX_DATABASE_URL": "postgres://notarx:owned@localhost/notarx", "NOTARX_OBJECT_STORE_ENDPOINT": "http://localhost:9000", "NOTARX_OBJECT_STORE_BUCKET": "owned-helm", "NOTARX_OBJECT_STORE_ACCESS_KEY": "owned", "NOTARX_OBJECT_STORE_SECRET_KEY": "owned-secret", "NOTARX_ADMIN_TOKEN": "owned-admin", "NOTARX_RESOLVER_TOKEN": "owned-reader"} {
		t.Setenv(key, value)
	}
	for _, input := range []string{"", "false", "true", "yes", "1"} {
		t.Setenv("NOTARX_HELM_PREVIEW_ENABLED", input)
		cfg, err := Load()
		if input == "yes" || input == "1" {
			if err == nil {
				t.Fatalf("invalid opt-in accepted %q", input)
			}
			continue
		}
		if err != nil || cfg.HelmPreviewEnabled != (input == "true") || contains(cfg.WorkerFormats, "helm") != (input == "true") {
			t.Fatalf("opt-in=%q enabled=%t error=%v", input, cfg.HelmPreviewEnabled, err)
		}
	}
	t.Setenv("NOTARX_WORKER_FORMATS", "helm")
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("NOTARX_HELM_PREVIEW_ENABLED", enabled)
		_, err := Load()
		if (err == nil) != (enabled == "true") {
			t.Fatalf("preview worker format enabled=%s error=%v", enabled, err)
		}
	}
}
