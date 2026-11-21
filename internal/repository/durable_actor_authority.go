package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/durableactor"
)

// durableActorAuthority is the current authority of the actor of a durable replication job, as the Helm and NuGet
// stores read it; each store copies it into its own distribution Authority. keyRole maps an API key's roles to the
// format's replication role.
type durableActorAuthority struct {
	Active             bool
	Role               string
	MustChangePassword bool
	ValidUntil         time.Time
}

// durableActorAuthorityLocked reads the authority in the memory store; the caller holds s.mu and has validated the
// actor with durableactor.ActorValid.
func (s *MemoryStore) durableActorAuthorityLocked(kind, actor, credential string, keyRole func([]string) string) durableActorAuthority {
	a := durableActorAuthority{}
	switch durableactor.ClassOf(kind) {
	case durableactor.ClassStatic:
		a.Active = true
	case durableactor.ClassUser:
		for _, u := range s.users {
			if strings.EqualFold(u.Name, strings.TrimPrefix(actor, durableactor.UserActorPrefix)) {
				a.Active = u.State == UserActive
				a.Role = u.Role
				a.MustChangePassword = u.MustChangePassword
				break
			}
		}
	case durableactor.ClassAPIKey:
		k, ok := s.apiKeys[strings.TrimPrefix(actor, durableactor.APIKeyActorPrefix)]
		a.Active = ok && k.ServiceAccountID == "" && k.RevokedAt == nil && (k.ExpiresAt == nil || k.ExpiresAt.After(time.Now()))
		a.Role = keyRole(k.Roles)
		if k.ExpiresAt != nil {
			a.ValidUntil = *k.ExpiresAt
		}
	case durableactor.ClassWorkload:
		w := s.WorkloadAuthorityLocked(actor, credential)
		a.Active, a.ValidUntil = w.Active, w.ValidUntil
	case durableactor.ClassServiceAccountCredential:
		account, ok := s.serviceAccounts[strings.TrimPrefix(actor, durableactor.ServiceAccountActorPrefix)]
		k, found := s.apiKeys[credential]
		a.Active = ok && account.State == ServiceAccountActive && found && k.ServiceAccountID == account.ID && k.RevokedAt == nil && (k.ExpiresAt == nil || k.ExpiresAt.After(time.Now()))
		if k.ExpiresAt != nil {
			a.ValidUntil = *k.ExpiresAt
		}
	}
	return a
}

// durableActorAuthorityTx reads the authority in tx, locking the rows it reads FOR SHARE; an unknown or ended
// subject is inactive, not an error. The caller has validated the actor with durableactor.ActorValid.
func durableActorAuthorityTx(ctx context.Context, tx *sql.Tx, kind, actor, credential string, keyRole func([]string) string) (durableActorAuthority, error) {
	a := durableActorAuthority{}
	switch durableactor.ClassOf(kind) {
	case durableactor.ClassStatic:
		a.Active = true
	case durableactor.ClassUser:
		var state string
		err := tx.QueryRowContext(ctx, `SELECT role,state,must_change_password FROM users WHERE lower(name)=lower($1) FOR SHARE`, strings.TrimPrefix(actor, durableactor.UserActorPrefix)).Scan(&a.Role, &state, &a.MustChangePassword)
		if errors.Is(err, sql.ErrNoRows) {
			return durableActorAuthority{}, nil
		}
		if err != nil {
			return durableActorAuthority{}, err
		}
		a.Active = state == string(UserActive)
	case durableactor.ClassWorkload:
		w, err := WorkloadAuthorityTx(ctx, tx, actor, credential)
		if err != nil {
			return durableActorAuthority{}, err
		}
		a.Active, a.ValidUntil = w.Active, w.ValidUntil
	case durableactor.ClassAPIKey, durableactor.ClassServiceAccountCredential:
		keyID := strings.TrimPrefix(actor, durableactor.APIKeyActorPrefix)
		account := ""
		if durableactor.ClassOf(kind) == durableactor.ClassServiceAccountCredential {
			account = strings.TrimPrefix(actor, durableactor.ServiceAccountActorPrefix)
			keyID = credential
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM service_accounts WHERE id=$1::uuid FOR SHARE`, account).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				return durableActorAuthority{}, nil
			}
			if err != nil {
				return durableActorAuthority{}, err
			}
			if state != string(ServiceAccountActive) {
				return durableActorAuthority{}, nil
			}
		}
		var roles []byte
		var revoked, expires sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT array_to_json(roles),revoked_at,expires_at FROM api_keys WHERE id=$1::uuid AND COALESCE(service_account_id::text,'')=$2 FOR SHARE`, keyID, account).Scan(&roles, &revoked, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return durableActorAuthority{}, nil
		}
		if err != nil {
			return durableActorAuthority{}, err
		}
		var values []string
		if err = json.Unmarshal(roles, &values); err != nil {
			return durableActorAuthority{}, err
		}
		a.Role = keyRole(values)
		a.Active = !revoked.Valid && (!expires.Valid || expires.Time.After(time.Now()))
		if expires.Valid {
			a.ValidUntil = expires.Time
		}
	}
	return a, nil
}
