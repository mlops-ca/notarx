package repository

import (
	"context"
	"errors"
	"time"
)

// ErrSAMLInvalid reports arguments the SAML state store refuses.
var ErrSAMLInvalid = errors.New("saml state argument is invalid")

// MaxSAMLAssertionIDLength bounds a stored assertion ID; longer values are refused rather than
// truncated, so two different IDs never collide.
const MaxSAMLAssertionIDLength = 256

// SAMLStateStore persists the single-use state of the SAML browser flow (ADR 0018 section 3).
// Expired rows are deleted by the retention janitor (internal/retention), never by the store.
type SAMLStateStore interface {
	// CreateSAMLRequestState stores a new AuthnRequest id for the provider and returns it.
	CreateSAMLRequestState(ctx context.Context, providerID string, expiresAt time.Time) (string, error)
	// ConsumeSAMLRequestState deletes the row and reports true only when it exists for the provider
	// and has not expired at now; a second call for the same id reports false.
	ConsumeSAMLRequestState(ctx context.Context, providerID, id string, now time.Time) (bool, error)
	// RecordSAMLAssertionID inserts (provider, assertion id) once and reports false on a duplicate.
	RecordSAMLAssertionID(ctx context.Context, providerID, assertionID string, expiresAt time.Time) (bool, error)
}

func samlStateArguments(providerID string) (string, error) {
	canonical, ok := canonicalUUID(providerID)
	if !ok {
		return "", errors.Join(ErrSAMLInvalid, errors.New("provider id must be a UUID"))
	}
	return canonical, nil
}

func samlAssertionArguments(providerID, assertionID string) (string, error) {
	canonical, err := samlStateArguments(providerID)
	if err != nil {
		return "", err
	}
	if assertionID == "" || len(assertionID) > MaxSAMLAssertionIDLength {
		return "", errors.Join(ErrSAMLInvalid, errors.New("assertion id must be 1 to 256 characters"))
	}
	return canonical, nil
}

func samlRequestID(providerID, id string) (string, string, bool) {
	provider, ok := canonicalUUID(providerID)
	if !ok {
		return "", "", false
	}
	state, ok := canonicalUUID(id)
	return provider, state, ok
}
