package tokenissuer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// DefaultLifetime is the lifetime of an assertion when the caller does not ask for one.
	DefaultLifetime = 5 * time.Minute
	// HardMaxLifetime is the ceiling no configuration can raise (ADR 0020).
	HardMaxLifetime = 10 * time.Minute
)

// Credential kinds recorded in notarx_cred.
const (
	CredentialAPIKey             = "api_key"
	CredentialWorkloadFederation = "workload_federation"
)

// Claims is the whole payload of an assertion. There are deliberately no email, groups or
// roles claims: assertions stay small and non-escalating.
type Claims struct {
	Issuer         string `json:"iss"`
	Subject        string `json:"sub"`
	Audience       string `json:"aud"`
	IssuedAt       int64  `json:"iat"`
	NotBefore      int64  `json:"nbf"`
	Expiry         int64  `json:"exp"`
	JTI            string `json:"jti"`
	ServiceAccount string `json:"notarx_sa,omitempty"`
	Credential     string `json:"notarx_cred,omitempty"`
}

// ClaimRequest is what a caller asks for.
type ClaimRequest struct {
	Subject string
	// Audiences must hold exactly one value: tokens are single audience.
	Audiences      []string
	Lifetime       time.Duration
	ServiceAccount string
	Credential     string
}

// ClaimBuilder builds claims for one issuer.
type ClaimBuilder struct {
	Issuer string
	// MaxLifetime bounds every request; zero or above HardMaxLifetime means HardMaxLifetime.
	MaxLifetime time.Duration
	Now         func() time.Time
	Rand        io.Reader
}

// Build validates the request and returns the claims.
func (b ClaimBuilder) Build(request ClaimRequest) (Claims, error) {
	if strings.TrimSpace(b.Issuer) == "" {
		return Claims{}, errors.New("tokenissuer: issuer is not configured")
	}
	if strings.TrimSpace(request.Subject) == "" {
		return Claims{}, errors.New("tokenissuer: subject is required")
	}
	if len(request.Audiences) != 1 || strings.TrimSpace(request.Audiences[0]) == "" {
		return Claims{}, errors.New("tokenissuer: exactly one audience is required")
	}
	maximum := b.MaxLifetime
	if maximum <= 0 || maximum > HardMaxLifetime {
		maximum = HardMaxLifetime
	}
	lifetime := request.Lifetime
	if lifetime == 0 {
		lifetime = min(DefaultLifetime, maximum)
	}
	if lifetime < 0 || lifetime > maximum {
		return Claims{}, fmt.Errorf("tokenissuer: lifetime must be between 1s and %s", maximum)
	}
	if request.Credential != "" && request.Credential != CredentialAPIKey && request.Credential != CredentialWorkloadFederation {
		return Claims{}, errors.New("tokenissuer: unknown credential kind")
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	source := b.Rand
	if source == nil {
		source = rand.Reader
	}
	jti := make([]byte, 16)
	if _, err := io.ReadFull(source, jti); err != nil {
		return Claims{}, fmt.Errorf("tokenissuer: jti: %w", err)
	}
	issued := now().UTC()
	return Claims{
		Issuer: b.Issuer, Subject: request.Subject, Audience: request.Audiences[0],
		IssuedAt: issued.Unix(), NotBefore: issued.Unix(), Expiry: issued.Add(lifetime).Unix(),
		JTI: hex.EncodeToString(jti), ServiceAccount: request.ServiceAccount, Credential: request.Credential,
	}, nil
}
