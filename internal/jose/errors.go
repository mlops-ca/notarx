package jose

import "errors"

var (
	// ErrInvalidToken means the token, its signature or its claims are not acceptable.
	// Callers must treat it as "not authenticated" and never echo the detail to clients.
	ErrInvalidToken = errors.New("jose: invalid token")
	// ErrKeySourceUnavailable means the issuer's keys could not be obtained (network,
	// TLS, SSRF guard, malformed document) and no usable stale copy exists.
	ErrKeySourceUnavailable = errors.New("jose: issuer keys unavailable")
	// ErrInvalidConfig means the issuer or verifier configuration is unusable.
	ErrInvalidConfig = errors.New("jose: invalid configuration")
)

// Error carries the operator-only reason of a rejection and matches one of the
// package sentinels with errors.Is.
type Error struct {
	Kind   error
	Reason string
	Err    error
}

func (e *Error) Error() string {
	text := e.Kind.Error() + ": " + e.Reason
	if e.Err != nil {
		text += ": " + e.Err.Error()
	}
	return text
}

func (e *Error) Is(target error) bool { return target == e.Kind }

func (e *Error) Unwrap() error { return e.Err }

func invalid(reason string) error { return &Error{Kind: ErrInvalidToken, Reason: reason} }

func unavailable(reason string, err error) error {
	return &Error{Kind: ErrKeySourceUnavailable, Reason: reason, Err: err}
}

func badConfig(reason string) error { return &Error{Kind: ErrInvalidConfig, Reason: reason} }
