package config

import (
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// WorkloadConfig holds the verification limits of ADR 0019 (workload identity federation).
type WorkloadConfig struct {
	// ClockSkew is the leeway applied to exp, nbf and iat (NOTARX_WORKLOAD_CLOCK_SKEW, max 5m).
	ClockSkew time.Duration
	// MaxTokenAge caps now-iat and exp-iat (NOTARX_WORKLOAD_MAX_TOKEN_AGE).
	MaxTokenAge time.Duration
	// JWKSTTL is how long an issuer's keys are cached (NOTARX_WORKLOAD_JWKS_TTL).
	JWKSTTL time.Duration
	// JWKSStaleMax is how long expired keys keep being served when a refresh fails
	// (NOTARX_WORKLOAD_JWKS_STALE_MAX).
	JWKSStaleMax time.Duration
}

// VerifierOptions converts the configuration into options of internal/jose.
func (w WorkloadConfig) VerifierOptions() jose.Options {
	options := jose.DefaultOptions()
	options.ClockSkew, options.MaxTokenAge = w.ClockSkew, w.MaxTokenAge
	options.JWKSTTL, options.JWKSStaleMax = w.JWKSTTL, w.JWKSStaleMax
	return options
}

func workloadConfigFromEnv(current WorkloadConfig) (WorkloadConfig, error) {
	var err error
	if current.ClockSkew, err = durationEnv("NOTARX_WORKLOAD_CLOCK_SKEW", current.ClockSkew); err != nil {
		return current, err
	}
	if current.ClockSkew > jose.MaxClockSkew {
		return current, fmt.Errorf("NOTARX_WORKLOAD_CLOCK_SKEW must not exceed %s", jose.MaxClockSkew)
	}
	if current.MaxTokenAge, err = durationEnv("NOTARX_WORKLOAD_MAX_TOKEN_AGE", current.MaxTokenAge); err != nil {
		return current, err
	}
	if current.JWKSTTL, err = durationEnv("NOTARX_WORKLOAD_JWKS_TTL", current.JWKSTTL); err != nil {
		return current, err
	}
	if current.JWKSStaleMax, err = durationEnv("NOTARX_WORKLOAD_JWKS_STALE_MAX", current.JWKSStaleMax); err != nil {
		return current, err
	}
	return current, nil
}
