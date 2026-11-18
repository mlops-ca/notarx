package config

import "testing"

func TestRubyGemsPreviewRequiresExplicitEnablementAndHonorsWorkerFilters(t *testing.T) {
	for key, value := range map[string]string{
		"NOTARX_DATABASE_URL":            "postgres://notarx:owned@localhost/notarx",
		"NOTARX_OBJECT_STORE_ENDPOINT":   "http://localhost:9000",
		"NOTARX_OBJECT_STORE_BUCKET":     "owned-rubygems",
		"NOTARX_OBJECT_STORE_ACCESS_KEY": "owned",
		"NOTARX_OBJECT_STORE_SECRET_KEY": "owned-secret",
		"NOTARX_ADMIN_TOKEN":             "owned-admin",
		"NOTARX_RESOLVER_TOKEN":          "owned-reader",
		"NOTARX_HELM_PREVIEW_ENABLED":    "false",
		"NOTARX_NUGET_PREVIEW_ENABLED":   "false",
	} {
		t.Setenv(key, value)
	}
	for _, test := range []struct {
		name, enabled, formats             string
		wantEnabled, wantWorker, wantError bool
	}{
		{name: "default"},
		{name: "disabled", enabled: "false"},
		{name: "enabled defaults", enabled: "true", wantEnabled: true, wantWorker: true},
		{name: "explicit inclusion", enabled: "true", formats: "maven,rubygems", wantEnabled: true, wantWorker: true},
		{name: "explicit exclusion", enabled: "true", formats: "maven", wantEnabled: true},
		{name: "disabled explicit worker", enabled: "false", formats: "rubygems", wantError: true},
		{name: "implicit explicit worker", formats: "rubygems", wantError: true},
		{name: "invalid boolean", enabled: "yes", wantError: true},
		{name: "numeric boolean", enabled: "1", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NOTARX_RUBYGEMS_PREVIEW_ENABLED", test.enabled)
			t.Setenv("NOTARX_WORKER_FORMATS", test.formats)
			cfg, err := Load()
			if (err != nil) != test.wantError {
				t.Fatalf("Load() error=%v wantError=%t", err, test.wantError)
			}
			if test.wantError {
				return
			}
			if cfg.RubyGemsPreviewEnabled != test.wantEnabled || contains(cfg.WorkerFormats, "rubygems") != test.wantWorker {
				t.Fatalf("enabled=%t formats=%v", cfg.RubyGemsPreviewEnabled, cfg.WorkerFormats)
			}
		})
	}
}
