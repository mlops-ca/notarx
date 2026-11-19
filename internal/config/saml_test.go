package config

import (
	"testing"
	"time"
)

func TestSAMLConfigDefaultsAndOverrides(t *testing.T) {
	defaults, err := samlConfigFromEnv(DefaultSAMLConfig())
	if err != nil || defaults.ClockSkew != time.Minute || defaults.RequestTTL != 10*time.Minute || defaults.MaxResponseBytes != 1048576 {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	t.Setenv("NOTARX_SAML_CLOCK_SKEW", "5m")
	t.Setenv("NOTARX_SAML_REQUEST_TTL", "120")
	t.Setenv("NOTARX_SAML_MAX_RESPONSE_BYTES", "2048")
	got, err := samlConfigFromEnv(defaults)
	if err != nil || got.ClockSkew != 5*time.Minute || got.RequestTTL != 2*time.Minute || got.MaxResponseBytes != 2048 {
		t.Fatalf("overrides = %+v, %v", got, err)
	}
}

func TestSAMLConfigRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"NOTARX_SAML_CLOCK_SKEW":         "301s",
		"NOTARX_SAML_REQUEST_TTL":        "-1s",
		"NOTARX_SAML_MAX_RESPONSE_BYTES": "0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := samlConfigFromEnv(DefaultSAMLConfig()); err == nil {
				t.Fatalf("%s=%s accepted", name, value)
			}
		})
	}
}
