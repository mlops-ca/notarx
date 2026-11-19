// Package jose is the single home of JOSE handling in notarx (ADR 0019 "Shared
// JOSE package"): JWT verification against per-issuer key sources, JWK and JWKS
// encoding, RFC 7638 thumbprints and signing helpers.
//
// The verifier is deliberately strict. The algorithm allow-list is checked
// before any key lookup, "none" and HMAC algorithms are never accepted, RSA keys
// under 2048 bits are refused, the JWK key type must match the algorithm, and
// the crit, jku, x5u and jwk header parameters are never used to find or trust a
// key (a token carrying any crit header is refused, RFC 7515 section 4.1.11). Key
// material comes only from the issuer's configured source: OIDC discovery or a
// static JWKS. Every network fetch goes through an SSRF-guarded client.
package jose
