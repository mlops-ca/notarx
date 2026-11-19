package config

import (
	"testing"
	"time"
)

func TestSAMLConfigDefaultsAndOverrides(t *testing.T) {
	defaults, err := samlConfigFromEnv(DefaultSAMLConfig())
	if err != nil || defaults.ClockSkew != time.Minute || defaults.RequestTTL != 10*time.Minute || defaults.MaxResponseBytes != 1048576 || defaults.MetadataRefreshInterval != 24*time.Hour {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	t.Setenv("NOTARX_SAML_CLOCK_SKEW", "5m")
	t.Setenv("NOTARX_SAML_REQUEST_TTL", "120")
	t.Setenv("NOTARX_SAML_MAX_RESPONSE_BYTES", "2048")
	t.Setenv("NOTARX_SAML_METADATA_REFRESH_INTERVAL", "6h")
	got, err := samlConfigFromEnv(defaults)
	if err != nil || got.ClockSkew != 5*time.Minute || got.RequestTTL != 2*time.Minute || got.MaxResponseBytes != 2048 || got.MetadataRefreshInterval != 6*time.Hour {
		t.Fatalf("overrides = %+v, %v", got, err)
	}
}

func TestSAMLConfigAllowsZeroToDisable(t *testing.T) {
	for _, value := range []string{"0", "0s", "off"} {
		t.Setenv("NOTARX_SAML_CLOCK_SKEW", value)
		t.Setenv("NOTARX_SAML_METADATA_REFRESH_INTERVAL", value)
		got, err := samlConfigFromEnv(DefaultSAMLConfig())
		if err != nil || got.ClockSkew != 0 || got.MetadataRefreshInterval != 0 {
			t.Fatalf("%s = %+v, %v", value, got, err)
		}
	}
}

func TestSAMLConfigRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"NOTARX_SAML_CLOCK_SKEW":                "301s",
		"NOTARX_SAML_REQUEST_TTL":               "-1s",
		"NOTARX_SAML_MAX_RESPONSE_BYTES":        "0",
		"NOTARX_SAML_METADATA_REFRESH_INTERVAL": "never",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := samlConfigFromEnv(DefaultSAMLConfig()); err == nil {
				t.Fatalf("%s=%s accepted", name, value)
			}
		})
	}
}
