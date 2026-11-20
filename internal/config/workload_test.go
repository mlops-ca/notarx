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

func TestWorkloadConfigEnablementAndExchangeSettings(t *testing.T) {
	defaults := WorkloadConfig{ExchangeMaxTTL: DefaultWorkloadExchangeMaxTTL, ExchangeRate: DefaultWorkloadExchangeRate}
	got, err := workloadConfigFromEnv(defaults)
	if err != nil || got.Enabled || got.AllowPrivateNetwork || got.ExchangeMaxTTL != 15*time.Minute || got.ExchangeRate != 60 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_WORKLOAD_ENABLED", "true")
	t.Setenv("NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK", "true")
	t.Setenv("NOTARX_WORKLOAD_EXCHANGE_MAX_TTL", "1h")
	t.Setenv("NOTARX_WORKLOAD_EXCHANGE_RATE", "5")
	got, err = workloadConfigFromEnv(defaults)
	if err != nil || !got.Enabled || !got.AllowPrivateNetwork || got.ExchangeMaxTTL != time.Hour || got.ExchangeRate != 5 {
		t.Fatalf("overrides = %+v, %v", got, err)
	}
}

func TestWorkloadConfigRejectsInvalidExchangeSettings(t *testing.T) {
	for name, value := range map[string]string{
		"NOTARX_WORKLOAD_ENABLED":               "yes",
		"NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK": "1",
		"NOTARX_WORKLOAD_EXCHANGE_MAX_TTL":      "2h",
		"NOTARX_WORKLOAD_EXCHANGE_RATE":         "0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := workloadConfigFromEnv(WorkloadConfig{}); err == nil {
				t.Fatalf("%s=%s accepted", name, value)
			}
		})
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
