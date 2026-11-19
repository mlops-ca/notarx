package repository

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

type memorySAMLState struct {
	requests   map[string]memorySAMLRequest
	assertions map[string]time.Time
}

type memorySAMLRequest struct {
	providerID string
	expiresAt  time.Time
}

func newMemorySAMLState() *memorySAMLState {
	return &memorySAMLState{requests: map[string]memorySAMLRequest{}, assertions: map[string]time.Time{}}
}

func (s *MemoryStore) CreateSAMLRequestState(_ context.Context, providerID string, expiresAt time.Time) (string, error) {
	provider, err := samlStateArguments(providerID)
	if err != nil {
		return "", err
	}
	id := ids.New()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saml.requests[id] = memorySAMLRequest{providerID: provider, expiresAt: expiresAt}
	return id, nil
}

func (s *MemoryStore) ConsumeSAMLRequestState(_ context.Context, providerID, id string, now time.Time) (bool, error) {
	provider, state, ok := samlRequestID(providerID, id)
	if !ok {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.saml.requests[state]
	if !found || request.providerID != provider || !request.expiresAt.After(now) {
		return false, nil
	}
	delete(s.saml.requests, state)
	return true, nil
}

// RecordSAMLAssertionID mirrors the PostgreSQL table: a recorded pair conflicts until it is deleted,
// even after expires_at, because deletion belongs to the retention janitor.
func (s *MemoryStore) RecordSAMLAssertionID(_ context.Context, providerID, assertionID string, expiresAt time.Time) (bool, error) {
	provider, err := samlAssertionArguments(providerID, assertionID)
	if err != nil {
		return false, err
	}
	key := provider + ":" + assertionID
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.saml.assertions[key]; seen {
		return false, nil
	}
	s.saml.assertions[key] = expiresAt
	return true, nil
}
