package repository

import (
	"context"
	"time"
)

type ServiceAccountState string

const (
	ServiceAccountActive   ServiceAccountState = "active"
	ServiceAccountDisabled ServiceAccountState = "disabled"
)

// ServiceAccount is a stable non-human authorization principal. Repository
// grants bind to service-account:<id>; credentials may rotate independently.
type ServiceAccount struct {
	ID          string
	Name        string
	Description string
	State       ServiceAccountState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Version     string
	// AssertionSubject is the sub claim of the assertions the account mints (ADR 0020). It is set when the account is
	// created, never changes (a rename does not touch it) and is empty for an account that was created without one.
	AssertionSubject string
}

// AssertionAudience is one entry of the assertion audience allow-list of a service account (ADR 0020). An account
// may mint an assertion only for an audience on its own list; there is no wildcard and no default list.
type AssertionAudience struct {
	ServiceAccountID string
	Audience         string
	// MaxLifetimeSeconds bounds the lifetime of an assertion for this audience, 60 to 600.
	MaxLifetimeSeconds int
	CreatedAt          time.Time
}

// Bounds of the assertion allow-list, mirrored by the check constraints of migration 000016.
const (
	MinAssertionLifetimeSeconds = 60
	MaxAssertionLifetimeSeconds = 600
	// MaxAssertionAudienceLength bounds an audience and a subject: both are key columns.
	MaxAssertionAudienceLength = 255
	// DefaultAssertionSubjectPrefix prefixes the account name in the default assertion subject.
	DefaultAssertionSubjectPrefix = "notarx:service-account:"
)

// DefaultAssertionSubject returns the default assertion subject of a service account name and whether the name may
// be used in one: characters outside [A-Za-z0-9._:-] are refused (ADR 0020).
func DefaultAssertionSubject(name string) (string, bool) {
	if name == "" || len(name) > 128 {
		return "", false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
		default:
			return "", false
		}
	}
	return DefaultAssertionSubjectPrefix + name, true
}

type ServiceAccountUpdate struct {
	ID          string
	Description *string
	State       *ServiceAccountState
}

type ServiceAccountStore interface {
	CreateServiceAccount(context.Context, ServiceAccount, ...AuditRecord) (ServiceAccount, error)
	ListServiceAccounts(context.Context, int, string) ([]ServiceAccount, error)
	GetServiceAccount(context.Context, string) (ServiceAccount, error)
	UpdateServiceAccount(context.Context, ServiceAccountUpdate, string, ...AuditRecord) (ServiceAccount, error)
	CreateServiceAccountCredential(context.Context, APIKey, ...AuditRecord) (APIKey, error)
	ListServiceAccountCredentials(context.Context, string, int, string) ([]APIKey, error)
	RevokeServiceAccountCredential(context.Context, string, string, ...AuditRecord) (APIKey, error)
	// ListAssertionAudiences returns the allow-list of an account ordered by audience; ErrNotFound for an unknown account.
	ListAssertionAudiences(ctx context.Context, accountID string) ([]AssertionAudience, error)
	// GetAssertionAudience returns one entry; ErrNotFound when the account or the entry does not exist.
	GetAssertionAudience(ctx context.Context, accountID, audience string) (AssertionAudience, error)
	// PutAssertionAudience adds an entry or replaces the lifetime of an existing one and reports whether it was added.
	PutAssertionAudience(ctx context.Context, entry AssertionAudience, audit ...AuditRecord) (AssertionAudience, bool, error)
	// DeleteAssertionAudience removes an entry; ErrNotFound when it does not exist.
	DeleteAssertionAudience(ctx context.Context, accountID, audience string, audit ...AuditRecord) error
}
