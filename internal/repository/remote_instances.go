package repository

import (
	"context"
	"errors"
	"time"
)

// Remote instance authentication kinds (ADR 0021, "Remote targets").
const (
	RemoteInstanceAuthWorkloadIdentity = "workload_identity"
	RemoteInstanceAuthStaticToken      = "static_token"
)

// Remote instance states. A check that cannot reach the remote, or that finds another instance than the one
// registered, leaves the instance unreachable; an administrator can disable one so senders skip it.
const (
	RemoteInstanceStateActive      = "active"
	RemoteInstanceStateUnreachable = "unreachable"
	RemoteInstanceStateDisabled    = "disabled"
)

var (
	// ErrRemoteInstanceNameExists reports a second remote instance under an existing name.
	ErrRemoteInstanceNameExists = errors.New("remote instance name already exists")
	ErrRemoteInstanceInUse      = errors.New("remote instance is still the target of a promotion rule")
)

// RemoteInstance is a registered peer notarx instance that promotions can push to. TokenSealed holds the static
// token sealed by internal/secrets (stored as TEXT); it is never part of an API response, an audit record or an
// event.
type RemoteInstance struct {
	ID            string
	Name          string
	BaseURL       string
	InstanceID    string
	AuthKind      string
	Audience      string
	TokenSealed   string `json:"-"`
	TrustEvidence bool
	State         string
	LastCheckedAt time.Time
	Version       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ValidRemoteInstanceAuthKind reports whether kind is a known authentication kind.
func ValidRemoteInstanceAuthKind(kind string) bool {
	return kind == RemoteInstanceAuthWorkloadIdentity || kind == RemoteInstanceAuthStaticToken
}

// ValidRemoteInstanceState reports whether state is a known remote instance state.
func ValidRemoteInstanceState(state string) bool {
	switch state {
	case RemoteInstanceStateActive, RemoteInstanceStateUnreachable, RemoteInstanceStateDisabled:
		return true
	}
	return false
}

type RemoteInstanceQuery struct {
	Limit     int
	PageToken string
}

type RemoteInstancePage struct {
	Items         []RemoteInstance
	NextPageToken string
}

// RemoteInstanceStore persists the remote instance registry and the identity of this deployment.
type RemoteInstanceStore interface {
	CreateRemoteInstance(context.Context, RemoteInstance, ...AuditRecord) (RemoteInstance, error)
	PageRemoteInstances(context.Context, RemoteInstanceQuery) (RemoteInstancePage, error)
	GetRemoteInstance(context.Context, string) (RemoteInstance, error)
	// UpdateRemoteInstance replaces the editable fields when the stored version equals the expected one.
	UpdateRemoteInstance(context.Context, RemoteInstance, string, ...AuditRecord) (RemoteInstance, error)
	DeleteRemoteInstance(context.Context, string, ...AuditRecord) error
	// RecordRemoteInstanceCheck stores the outcome of a well-known check (state and time) without changing the version.
	RecordRemoteInstanceCheck(ctx context.Context, id, state string, checkedAt time.Time) (RemoteInstance, error)
	// LocalInstanceID returns the id of this deployment, a UUID created on first use and shared by every node.
	LocalInstanceID(context.Context) (string, error)
}
