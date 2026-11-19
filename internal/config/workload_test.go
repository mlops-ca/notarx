package config

import (
	"testing"
	"time"
)

func TestWorkloadConfigDefaultsAndOverrides(t *testing.T) {
	defaults, err := workloadConfigFromEnv(WorkloadConfig{ClockSkew: time.Minute, MaxTokenAge: time.Hour, JWKSTTL: 10 * time.Minute, JWKSStaleMax: 24 * time.Hour})
	if err != nil || defaults.ClockSkew != time.Minute || defaults.JWKSStaleMax != 24*time.Hour {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	t.Setenv("NOTARX_WORKLOAD_CLOCK_SKEW", "2m")
	t.Setenv("NOTARX_WORKLOAD_MAX_TOKEN_AGE", "30m")
	t.Setenv("NOTARX_WORKLOAD_JWKS_TTL", "300")
	t.Setenv("NOTARX_WORKLOAD_JWKS_STALE_MAX", "12h")
	got, err := workloadConfigFromEnv(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClockSkew != 2*time.Minute || got.MaxTokenAge != 30*time.Minute || got.JWKSTTL != 5*time.Minute || got.JWKSStaleMax != 12*time.Hour {
		t.Fatalf("overrides = %+v", got)
	}
	options := got.VerifierOptions()
	if options.ClockSkew != 2*time.Minute || options.KidRefetchInterval != 30*time.Second {
		t.Fatalf("options = %+v", options)
	}
}

func TestWorkloadConfigRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"NOTARX_WORKLOAD_CLOCK_SKEW":     "6m",
		"NOTARX_WORKLOAD_MAX_TOKEN_AGE":  "-1s",
		"NOTARX_WORKLOAD_JWKS_TTL":       "soon",
		"NOTARX_WORKLOAD_JWKS_STALE_MAX": "0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := workloadConfigFromEnv(WorkloadConfig{}); err == nil {
				t.Fatalf("%s=%s accepted", name, value)
			}
		})
	}
}
