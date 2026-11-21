package config

import "testing"

func TestSCIMConfigDefaultsAndOverrides(t *testing.T) {
	defaults, err := scimConfigFromEnv(DefaultSCIMConfig())
	if err != nil || defaults.Enabled || defaults.MaxPage != 200 {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	t.Setenv("NOTARX_SCIM_ENABLED", "true")
	t.Setenv("NOTARX_SCIM_MAX_PAGE", "50")
	got, err := scimConfigFromEnv(defaults)
	if err != nil || !got.Enabled || got.MaxPage != 50 {
		t.Fatalf("overrides = %+v, %v", got, err)
	}
}

func TestSCIMConfigRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"NOTARX_SCIM_ENABLED":  "yes",
		"NOTARX_SCIM_MAX_PAGE": "0",
	} {
		t.Setenv(name, value)
		if _, err := scimConfigFromEnv(DefaultSCIMConfig()); err == nil {
			t.Fatalf("%s=%s was accepted", name, value)
		}
		t.Setenv(name, "")
	}
	t.Setenv("NOTARX_SCIM_MAX_PAGE", "1001")
	if _, err := scimConfigFromEnv(DefaultSCIMConfig()); err == nil {
		t.Fatal("a page cap above the maximum was accepted")
	}
}
