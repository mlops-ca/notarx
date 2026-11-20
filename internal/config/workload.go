package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// WorkloadConfig holds the verification limits of ADR 0019 (workload identity federation).
type WorkloadConfig struct {
	// Enabled turns workload JWT authentication and the token exchange on (NOTARX_WORKLOAD_ENABLED,
	// default false). While false, JWTs reach only the human OIDC path.
	Enabled bool
	// AllowPrivateNetwork lets every issuer fetch keys from private, loopback and link-local
	// addresses (NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK, default false); an issuer can also set it itself.
	AllowPrivateNetwork bool
	// ExchangeMaxTTL caps the lifetime of an exchanged token (NOTARX_WORKLOAD_EXCHANGE_MAX_TTL,
	// default 15m, at most 1h); a policy may lower it.
	ExchangeMaxTTL time.Duration
	// ExchangeRate is the number of token exchanges per minute allowed per source IP and per
	// issuer (NOTARX_WORKLOAD_EXCHANGE_RATE, default 60).
	ExchangeRate int
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

// Exchange defaults and limits of ADR 0019 ("Token exchange endpoint").
const (
	DefaultWorkloadExchangeMaxTTL = 15 * time.Minute
	MaxWorkloadExchangeTTL        = time.Hour
	DefaultWorkloadExchangeRate   = 60
)

func workloadConfigFromEnv(current WorkloadConfig) (WorkloadConfig, error) {
	var err error
	if current.Enabled, err = boolEnv("NOTARX_WORKLOAD_ENABLED", current.Enabled); err != nil {
		return current, err
	}
	if current.AllowPrivateNetwork, err = boolEnv("NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK", current.AllowPrivateNetwork); err != nil {
		return current, err
	}
	if current.ExchangeMaxTTL, err = durationEnv("NOTARX_WORKLOAD_EXCHANGE_MAX_TTL", current.ExchangeMaxTTL); err != nil {
		return current, err
	}
	if current.ExchangeMaxTTL > MaxWorkloadExchangeTTL {
		return current, fmt.Errorf("NOTARX_WORKLOAD_EXCHANGE_MAX_TTL must not exceed %s", MaxWorkloadExchangeTTL)
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_WORKLOAD_EXCHANGE_RATE")); raw != "" {
		rate, parseErr := strconv.Atoi(raw)
		if parseErr != nil || rate < 1 || rate > 100000 {
			return current, errors.New("NOTARX_WORKLOAD_EXCHANGE_RATE must be a number of exchanges per minute between 1 and 100000")
		}
		current.ExchangeRate = rate
	}
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
