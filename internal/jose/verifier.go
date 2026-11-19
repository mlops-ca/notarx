package jose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
)

// Defaults of ADR 0019 (NOTARX_WORKLOAD_*). In Options a zero ClockSkew means no leeway, a zero
// MaxTokenAge means no age cap (the environment settings cannot be zero, so the cap is always on
// for workload tokens), a zero JWKSStaleMax means stale keys are never served, and a zero
// JWKSTTL or KidRefetchInterval selects the default.
const (
	DefaultClockSkew        = 60 * time.Second
	MaxClockSkew            = 5 * time.Minute
	DefaultMaxTokenAge      = time.Hour
	DefaultJWKSTTL          = 10 * time.Minute
	DefaultJWKSStaleMax     = 24 * time.Hour
	DefaultKidRefetchWindow = 30 * time.Second
	maxTokenBytes           = 16 << 10
)

// Issuer is the verifier-facing view of one trusted issuer.
type Issuer struct {
	// Issuer is the exact iss value; in discovery mode it is also the discovery base URL.
	Issuer string
	// Audiences lists the accepted aud values; a token matches when any value intersects.
	Audiences []string
	// Algorithms is the per-issuer allow-list; empty selects DefaultAlgorithms.
	Algorithms []string
	Mode       JWKSMode
	// JWKSURI short-circuits discovery in discovery mode.
	JWKSURI    string
	StaticJWKS []byte
	// CABundlePEM, when set, replaces the system roots for this issuer's fetches.
	CABundlePEM         string
	AllowPrivateNetwork bool
	// JWKSTTL overrides Options.JWKSTTL for this issuer (60 s for notarx-trusted issuers,
	// ADR 0020, so a revoke takes effect well inside the assertion life).
	JWKSTTL time.Duration
}

// Options configures a Verifier.
type Options struct {
	ClockSkew   time.Duration
	MaxTokenAge time.Duration
	JWKSTTL     time.Duration
	// JWKSStaleMax is how long past its TTL an unrefreshable key set keeps being served.
	JWKSStaleMax time.Duration
	// KidRefetchInterval rate limits kid-miss refetches (and failed refreshes) per issuer.
	KidRefetchInterval time.Duration
	// HTTPClient replaces the SSRF-guarded client. It is meant for operator-configured
	// issuers that predate the guard (the human OIDC validator) and for tests.
	HTTPClient *http.Client
	Metrics    Metrics
	Now        func() time.Time
}

// DefaultOptions returns the ADR 0019 defaults.
func DefaultOptions() Options {
	return Options{
		ClockSkew: DefaultClockSkew, MaxTokenAge: DefaultMaxTokenAge, JWKSTTL: DefaultJWKSTTL,
		JWKSStaleMax: DefaultJWKSStaleMax, KidRefetchInterval: DefaultKidRefetchWindow,
	}
}

// Verified is a token whose signature and registered claims were accepted.
type Verified struct {
	Issuer string
	// Claims is the full claim map; numbers are json.Number so integer ids survive.
	Claims map[string]any
	KeyID  string
	Alg    string
}

// TokenVerifier is the interface the human OIDC validator (ADR 0018), workload
// federation (ADR 0019) and notarx-as-issuer trust (ADR 0020) consume.
type TokenVerifier interface {
	Verify(ctx context.Context, token string, issuer Issuer) (*Verified, error)
}

// Verifier verifies JWTs and caches key sets per issuer.
type Verifier struct {
	opts Options
	mu   sync.RWMutex
	sets map[string]*keySet
}

var _ TokenVerifier = (*Verifier)(nil)

// NewVerifier validates the options and returns a verifier.
func NewVerifier(opts Options) (*Verifier, error) {
	if opts.ClockSkew < 0 || opts.ClockSkew > MaxClockSkew {
		return nil, badConfig(fmt.Sprintf("clock skew must be between 0 and %s", MaxClockSkew))
	}
	if opts.MaxTokenAge < 0 || opts.JWKSTTL < 0 || opts.JWKSStaleMax < 0 || opts.KidRefetchInterval < 0 {
		return nil, badConfig("durations must not be negative")
	}
	if opts.JWKSTTL == 0 {
		opts.JWKSTTL = DefaultJWKSTTL
	}
	if opts.KidRefetchInterval == 0 {
		opts.KidRefetchInterval = DefaultKidRefetchWindow
	}
	if opts.Metrics == nil {
		opts.Metrics = noMetrics{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Verifier{opts: opts, sets: map[string]*keySet{}}, nil
}

func allowedAlgorithms(issuer Issuer) ([]gojose.SignatureAlgorithm, error) {
	algs := issuer.Algorithms
	if len(algs) == 0 {
		algs = DefaultAlgorithms()
	}
	out := make([]gojose.SignatureAlgorithm, 0, len(algs))
	for _, alg := range algs {
		if _, ok := keyTypeFor(alg); !ok {
			return nil, badConfig("algorithm " + alg + " is not allowed")
		}
		out = append(out, gojose.SignatureAlgorithm(alg))
	}
	return out, nil
}

// Verify checks the signature of token against the issuer's keys and validates iss, aud,
// sub, exp, nbf, iat and the maximum age.
func (v *Verifier) Verify(ctx context.Context, token string, issuer Issuer) (*Verified, error) {
	if issuer.Issuer == "" || len(issuer.Audiences) == 0 {
		return nil, badConfig("issuer and audiences are required")
	}
	if issuer.Mode != ModeDiscovery && issuer.Mode != ModeStatic {
		return nil, badConfig("unknown jwks mode")
	}
	algs, err := allowedAlgorithms(issuer)
	if err != nil {
		return nil, err
	}
	if len(token) == 0 || len(token) > maxTokenBytes || strings.Count(token, ".") != 2 {
		return nil, invalid("not a compact JWS")
	}
	// ParseSignedCompact rejects every alg outside the allow-list (including "none" and
	// HMAC) before any key is looked up.
	parsed, err := gojose.ParseSignedCompact(token, algs)
	if err != nil || len(parsed.Signatures) != 1 {
		return nil, invalid("malformed token or algorithm not allowed")
	}
	header := parsed.Signatures[0].Header
	alg := header.Algorithm
	// Cheap early rejection so a token for another issuer never triggers a key fetch.
	unverified, err := decodeClaims(parsed.UnsafePayloadWithoutVerification())
	if err != nil {
		return nil, err
	}
	if iss, _ := unverified["iss"].(string); iss != issuer.Issuer {
		return nil, invalid("issuer mismatch")
	}
	key, err := v.lookup(ctx, issuer, header.KeyID, alg)
	if err != nil {
		return nil, err
	}
	if err := checkKeyForAlg(key, alg); err != nil {
		return nil, err
	}
	payload, err := parsed.Verify(key.Public().Key)
	if err != nil {
		return nil, invalid("signature verification failed")
	}
	claims, err := decodeClaims(payload)
	if err != nil {
		return nil, err
	}
	if err := v.checkClaims(claims, issuer); err != nil {
		return nil, err
	}
	return &Verified{Issuer: issuer.Issuer, Claims: claims, KeyID: header.KeyID, Alg: alg}, nil
}

func decodeClaims(payload []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil || claims == nil {
		return nil, invalid("payload is not a JSON object")
	}
	return claims, nil
}

func (v *Verifier) checkClaims(claims map[string]any, issuer Issuer) error {
	now, skew := v.opts.Now(), v.opts.ClockSkew
	if iss, _ := claims["iss"].(string); iss != issuer.Issuer {
		return invalid("issuer mismatch")
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return invalid("sub is required")
	}
	if !audienceMatches(claims["aud"], issuer.Audiences) {
		return invalid("audience mismatch")
	}
	exp, ok := numericDate(claims["exp"])
	if !ok {
		return invalid("exp is required")
	}
	if !now.Before(exp.Add(skew)) {
		return invalid("token expired")
	}
	if raw, present := claims["nbf"]; present {
		nbf, ok := numericDate(raw)
		if !ok {
			return invalid("nbf is not a number")
		}
		if nbf.Add(-skew).After(now) {
			return invalid("token not yet valid")
		}
	}
	maxAge := v.opts.MaxTokenAge
	raw, hasIAT := claims["iat"]
	if !hasIAT {
		// Without iat the age is unknown, so the cap applies to the time the token still has left.
		if maxAge > 0 && exp.Sub(now) > maxAge+skew {
			return invalid("token without iat is valid for longer than the maximum age")
		}
		return nil
	}
	iat, ok := numericDate(raw)
	if !ok {
		return invalid("iat is not a number")
	}
	if iat.Add(-skew).After(now) {
		return invalid("iat is in the future")
	}
	if maxAge > 0 {
		if now.Sub(iat) > maxAge {
			return invalid("token is older than the maximum age")
		}
		if exp.Sub(iat) > maxAge {
			return invalid("token lifetime exceeds the maximum age")
		}
	}
	return nil
}

// numericDate converts a NumericDate (seconds, possibly fractional) into a time.
func numericDate(raw any) (time.Time, bool) {
	number, ok := raw.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	if seconds, err := number.Int64(); err == nil {
		return time.Unix(seconds, 0), true
	}
	value, err := number.Float64()
	if err != nil || value != value || value > 1<<40 || value < -(1<<40) {
		return time.Time{}, false
	}
	whole := int64(value)
	return time.Unix(whole, int64((value-float64(whole))*1e9)), true
}

func audienceMatches(raw any, accepted []string) bool {
	switch aud := raw.(type) {
	case string:
		return slices.Contains(accepted, aud)
	case []any:
		for _, item := range aud {
			if text, ok := item.(string); ok && slices.Contains(accepted, text) {
				return true
			}
		}
	}
	return false
}
