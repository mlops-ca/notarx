package huggingface

import (
	"strings"
	"testing"
	"time"
)

func lookupMap(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(lookupMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefTTL != 5*time.Minute || !cfg.ServeStale || cfg.MaxFilesPerCommit != 200000 || cfg.MaxObjectBytes != 200<<30 || cfg.MaxConcurrentFetches != 4 || cfg.CDNAllowlist != nil {
		t.Fatalf("defaults = %+v", cfg)
	}
	blank, err := LoadConfig(lookupMap(map[string]string{EnvRefTTL: "  ", EnvMaxObjectBytes: ""}))
	if err != nil || blank.RefTTL != 5*time.Minute {
		t.Fatalf("blank values must keep the defaults: %+v, %v", blank, err)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(lookupMap(map[string]string{
		EnvRefTTL: "90s", EnvServeStale: "false", EnvMaxFilesPerCommit: "10", EnvMaxObjectBytes: "1099511627776",
		EnvMaxConcurrentFetches: "2", EnvCDNAllowlist: " Cdn.Example , *.edge.example,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefTTL != 90*time.Second || cfg.ServeStale || cfg.MaxFilesPerCommit != 10 || cfg.MaxObjectBytes != 1<<40 || cfg.MaxConcurrentFetches != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.CDNAllowlist) != 2 || cfg.CDNAllowlist[0] != "cdn.example" || cfg.CDNAllowlist[1] != "*.edge.example" {
		t.Fatalf("allowlist = %v", cfg.CDNAllowlist)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		EnvRefTTL: "soon", EnvServeStale: "maybe", EnvMaxFilesPerCommit: "0", EnvMaxObjectBytes: "1099511627777",
		EnvMaxConcurrentFetches: "-1", EnvCDNAllowlist: "https://cdn.example",
	} {
		_, err := LoadConfig(lookupMap(map[string]string{name: value}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%q: err = %v, want an error naming the variable", name, value, err)
		}
	}
	if _, err := LoadConfig(lookupMap(map[string]string{EnvMaxObjectBytes: "0"})); err == nil {
		t.Error("a zero object limit was accepted")
	}
	if _, err := LoadConfig(lookupMap(map[string]string{EnvRefTTL: "-1m"})); err == nil {
		t.Error("a negative TTL was accepted")
	}
}

func TestCDNAllowlistAppliesOnlyToTheDefaultUpstream(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.CDNAllowlistFor(DefaultUpstream); len(got) != len(DefaultCDNAllowlist) {
		t.Fatalf("default upstream allowlist = %v", got)
	}
	if got := cfg.CDNAllowlistFor("https://" + strings.ToUpper(strings.TrimPrefix(DefaultUpstream, "https://")) + "/"); len(got) == 0 {
		t.Fatalf("host comparison must ignore case: %v", got)
	}
	if got := cfg.CDNAllowlistFor("https://hub.mirror.example"); len(got) != 0 {
		t.Fatalf("an overridden upstream must follow no redirect until configured: %v", got)
	}
	cfg.CDNAllowlist = []string{"cdn.mirror.example"}
	if got := cfg.CDNAllowlistFor("https://hub.mirror.example"); len(got) != 1 || got[0] != "cdn.mirror.example" {
		t.Fatalf("explicit allowlist = %v", got)
	}
	if got := cfg.CDNAllowlistFor("not a url"); len(got) != 1 {
		t.Fatalf("an explicit allowlist applies to any upstream: %v", got)
	}
}
