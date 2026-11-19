// Package tokenissuer makes notarx an OIDC token issuer (ADR 0020). It holds the Signer
// port, claim building, signing key generation and the key lifecycle (publish before
// use, retention, rotation). It has no HTTP code; JOSE primitives live in internal/jose
// and sealing in internal/secrets. Persistence is behind the Store port, implemented by
// internal/repository for PostgreSQL.
package tokenissuer
