package tokenissuer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// Defaults of the lifecycle configuration (ADR 0020).
const (
	DefaultRotationInterval = 720 * time.Hour
	DefaultPublishLead      = 24 * time.Hour
)

// JWKSMaxAge is the Cache-Control max-age of the published key set.
const JWKSMaxAge = 5 * time.Minute

// MinPublishLead is the smallest publish lead that still outlasts verifier JWKS caches and the
// node signing key cache.
const MinPublishLead = JWKSMaxAge + DefaultKeyCacheTTL

// ErrNoNextKey reports a manual rotation with no next key to promote.
var ErrNoNextKey = errors.New("tokenissuer: there is no next key to promote")

// ErrLockBusy reports that another node holds the rotation lock.
var ErrLockBusy = errors.New("tokenissuer: another node is rotating keys; retry")

// LifecycleConfig is the key lifecycle configuration.
type LifecycleConfig struct {
	// Alg is the algorithm that signs new tokens. Keys of other algorithms that already have
	// an active key keep rotating.
	Alg              string
	RotationInterval time.Duration
	PublishLead      time.Duration
	MaxTokenLifetime time.Duration
}

// DefaultLifecycleConfig returns the ADR 0020 defaults.
func DefaultLifecycleConfig() LifecycleConfig {
	return LifecycleConfig{Alg: jose.RS256, RotationInterval: DefaultRotationInterval, PublishLead: DefaultPublishLead, MaxTokenLifetime: HardMaxLifetime}
}

// Validate rejects an unusable configuration.
func (c LifecycleConfig) Validate() error {
	if c.Alg != jose.RS256 && c.Alg != jose.ES256 {
		return errors.New("issuer algorithm must be RS256 or ES256")
	}
	if c.PublishLead <= MinPublishLead {
		return fmt.Errorf("issuer key publish lead must exceed %s: verifiers may cache the key set for %s and nodes cache the signing key for %s", MinPublishLead, JWKSMaxAge, DefaultKeyCacheTTL)
	}
	if c.RotationInterval <= c.PublishLead {
		return errors.New("issuer key rotation interval must exceed the publish lead")
	}
	if c.MaxTokenLifetime <= 0 || c.MaxTokenLifetime > HardMaxLifetime {
		return fmt.Errorf("issuer max token lifetime must be between 1s and %s", HardMaxLifetime)
	}
	return nil
}

// retention is how long a retired key stays published: longer than any token it signed lives.
func (c LifecycleConfig) retention() time.Duration { return c.MaxTokenLifetime + c.PublishLead }

// Manager runs the key lifecycle against a Store. Every node may run it; the store lock makes
// only one act at a time and every transition is idempotent and time based, so a crashed or
// duplicated worker converges.
type Manager struct {
	Store     Store
	Config    LifecycleConfig
	Generator Generator
	Now       func() time.Time
	// Changed is called after a pass that changed keys, so caches can be dropped.
	Changed func()
	Alert   IntegrityAlert
	Logger  *slog.Logger
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// Reconcile performs one pass. acquired is false when another node held the lock.
func (m *Manager) Reconcile(ctx context.Context) (acquired, changed bool, err error) {
	if err := m.Config.Validate(); err != nil {
		return false, false, err
	}
	acquired, err = m.Store.WithRotationLock(ctx, func(ctx context.Context, tx KeyTx) error {
		var inner error
		changed, inner = m.reconcile(ctx, tx)
		return inner
	})
	if err == nil && changed && m.Changed != nil {
		m.Changed()
	}
	return acquired, changed, err
}

// Run reconciles every interval until ctx ends; failures are logged and retried.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, _, err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
			m.logger().Error("signing key rotation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// split separates the rows that pass the integrity check from those that fail it; failing rows
// raise the alert and take no part in any decision.
func (m *Manager) split(records []KeyRecord) (trusted, untrusted []KeyRecord) {
	alert := m.Alert
	if alert == nil {
		alert = LogIntegrityAlert
	}
	for _, record := range records {
		if err := record.VerifyIntegrity(); err != nil {
			alert(record.KID, err)
			untrusted = append(untrusted, record)
			continue
		}
		trusted = append(trusted, record)
	}
	return trusted, untrusted
}

func (m *Manager) trusted(records []KeyRecord) []KeyRecord {
	trusted, _ := m.split(records)
	return trusted
}

func (m *Manager) reconcile(ctx context.Context, tx KeyTx) (bool, error) {
	all, err := tx.List(ctx)
	if err != nil {
		return false, err
	}
	now := m.now()
	changed := false
	live := make([]KeyRecord, 0, len(all))
	for _, record := range all {
		if record.Expired(now) {
			if err := tx.Delete(ctx, record.KID); err != nil {
				return false, err
			}
			changed = true
			continue
		}
		live = append(live, record)
	}
	var untrusted []KeyRecord
	live, untrusted = m.split(live)
	algs := []string{m.Config.Alg}
	for _, record := range live {
		if record.State == StateActive && !slices.Contains(algs, record.Alg) {
			algs = append(algs, record.Alg)
		}
	}
	for _, alg := range algs {
		if blocked := untrustedSlots(untrusted, alg); len(blocked) > 0 {
			// A row that fails its integrity check still holds the one active or next slot, so
			// creating or promoting a key would collide with it. Fail closed and say why: the
			// operator restores the key ring, reseals, or revokes the listed keys.
			m.logger().Error("signing keys fail their integrity check; key rotation for this algorithm is paused",
				"alg", alg, "kids", blocked, "remedy", "restore the encryption key ring and reseal, or revoke the listed keys")
			continue
		}
		var forAlg []KeyRecord
		for _, record := range live {
			if record.Alg == alg {
				forAlg = append(forAlg, record)
			}
		}
		did, err := m.reconcileAlg(ctx, tx, alg, forAlg, now)
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	return changed, nil
}

// untrustedSlots lists the kids of untrusted rows that occupy an active or next slot of alg.
func untrustedSlots(untrusted []KeyRecord, alg string) []string {
	var kids []string
	for _, record := range untrusted {
		if record.Alg == alg && (record.State == StateActive || record.State == StateNext) {
			kids = append(kids, record.KID)
		}
	}
	return kids
}

func pick(records []KeyRecord, state State) *KeyRecord {
	for i := range records {
		if records[i].State == state {
			return &records[i]
		}
	}
	return nil
}

func (m *Manager) reconcileAlg(ctx context.Context, tx KeyTx, alg string, records []KeyRecord, now time.Time) (bool, error) {
	cfg := m.Config
	active, next := pick(records, StateActive), pick(records, StateNext)
	changed, bootstrapped := false, false
	if active == nil {
		switch {
		case next == nil:
			// No token of this algorithm has ever been issued, so activating at once is safe.
			created, err := m.create(ctx, tx, alg, StateActive, now)
			if err != nil {
				return false, err
			}
			active, changed, bootstrapped = &created, true, true
		case !next.PublishedAt.IsZero() && !now.Before(next.PublishedAt.Add(cfg.PublishLead)):
			if err := m.promote(ctx, tx, next, now); err != nil {
				return false, err
			}
			active, next, changed = next, nil, true
		default:
			return false, nil // the next key has not been published long enough; sign with nothing rather than early
		}
	}
	if next == nil && (bootstrapped || !now.Before(active.ActivatedAt.Add(cfg.RotationInterval-cfg.PublishLead))) {
		created, err := m.create(ctx, tx, alg, StateNext, now)
		if err != nil {
			return false, err
		}
		next, changed = &created, true
	}
	if next != nil && !bootstrapped && !now.Before(active.ActivatedAt.Add(cfg.RotationInterval)) &&
		!next.PublishedAt.IsZero() && !now.Before(next.PublishedAt.Add(cfg.PublishLead)) {
		if err := m.demote(ctx, tx, active, now); err != nil {
			return false, err
		}
		if err := m.promote(ctx, tx, next, now); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func (m *Manager) create(ctx context.Context, tx KeyTx, alg string, state State, now time.Time) (KeyRecord, error) {
	record, err := m.Generator.Generate(alg, state, now)
	if err != nil {
		return KeyRecord{}, err
	}
	record.PublishedAt = now
	if state == StateActive {
		record.ActivatedAt = now
	}
	if err := record.Seal(); err != nil {
		return KeyRecord{}, err
	}
	if err := tx.Insert(ctx, record); err != nil {
		return KeyRecord{}, fmt.Errorf("tokenissuer: insert %s key: %w", state, err)
	}
	return record, nil
}

func (m *Manager) demote(ctx context.Context, tx KeyTx, record *KeyRecord, now time.Time) error {
	record.State, record.RetiredAt, record.ExpiresAt = StateRetired, now, now.Add(m.Config.retention())
	if err := record.Seal(); err != nil {
		return err
	}
	return tx.Update(ctx, *record)
}

func (m *Manager) promote(ctx context.Context, tx KeyTx, record *KeyRecord, now time.Time) error {
	record.State, record.ActivatedAt = StateActive, now
	if err := record.Seal(); err != nil {
		return err
	}
	return tx.Update(ctx, *record)
}

// Option tunes a manual operation.
type Option func(*callOptions)

type callOptions struct{ actor string }

// WithActor audits the operation as done by actor, in the transaction of the change. Without it nothing is audited:
// the periodic worker is not an operator action.
func WithActor(actor string) Option { return func(o *callOptions) { o.actor = actor } }

func collect(options []Option) callOptions {
	var out callOptions
	for _, option := range options {
		option(&out)
	}
	return out
}

// RotateResult describes a manual rotation.
type RotateResult struct {
	Retired, Activated string
	// Warning is set when the promoted key had been published for less than the lead time:
	// verifiers may not trust it yet.
	Warning string
}

// RotateNow demotes the active key of alg to retired and promotes its next key at once,
// then creates a fresh next key. The lead time is a warning only.
func (m *Manager) RotateNow(ctx context.Context, alg string, options ...Option) (RotateResult, error) {
	if err := m.Config.Validate(); err != nil {
		return RotateResult{}, err
	}
	call := collect(options)
	var result RotateResult
	acquired, err := m.Store.WithRotationLock(ctx, func(ctx context.Context, tx KeyTx) error {
		all, err := tx.List(ctx)
		if err != nil {
			return err
		}
		now := m.now()
		var forAlg []KeyRecord
		for _, record := range m.trusted(all) {
			if record.Alg == alg && !record.Expired(now) {
				forAlg = append(forAlg, record)
			}
		}
		active, next := pick(forAlg, StateActive), pick(forAlg, StateNext)
		if next == nil {
			return ErrNoNextKey
		}
		if active != nil {
			if err := m.demote(ctx, tx, active, now); err != nil {
				return err
			}
			result.Retired = active.KID
		}
		if lead := now.Sub(next.PublishedAt); next.PublishedAt.IsZero() || lead < m.Config.PublishLead {
			result.Warning = "the promoted key has been published for less than the publish lead; verifiers may not trust it yet"
		}
		if err := m.promote(ctx, tx, next, now); err != nil {
			return err
		}
		result.Activated = next.KID
		if _, err = m.create(ctx, tx, alg, StateNext, now); err != nil {
			return err
		}
		if call.actor == "" {
			return nil
		}
		return tx.Audit(ctx, AuditEvent{Actor: call.actor, Operation: "issuer.rotate", Evidence: map[string]string{
			"algorithm": alg, "retired": result.Retired, "activated": result.Activated,
		}})
	})
	if err != nil {
		return RotateResult{}, err
	}
	if !acquired {
		return RotateResult{}, ErrLockBusy
	}
	if m.Changed != nil {
		m.Changed()
	}
	return result, nil
}

// Revoke is the emergency path for a compromised key: it leaves the JWKS at once
// (expires_at = now). A revoked active key is replaced by its next key immediately.
func (m *Manager) Revoke(ctx context.Context, kid string, options ...Option) error {
	call := collect(options)
	acquired, err := m.Store.WithRotationLock(ctx, func(ctx context.Context, tx KeyTx) error {
		all, err := tx.List(ctx)
		if err != nil {
			return err
		}
		now := m.now()
		var target *KeyRecord
		for i := range all {
			if all[i].KID == kid {
				target = &all[i]
			}
		}
		if target == nil {
			return ErrKeyNotFound
		}
		wasActive := target.State == StateActive
		target.ExpiresAt = now
		if wasActive {
			target.State, target.RetiredAt = StateRetired, now
		}
		if err := target.Seal(); err != nil {
			return err
		}
		if err := tx.Update(ctx, *target); err != nil {
			return err
		}
		if call.actor != "" {
			if err := tx.Audit(ctx, AuditEvent{Actor: call.actor, Operation: "issuer.revoke", Evidence: map[string]string{
				"kid": kid, "algorithm": target.Alg, "state": string(target.State), "wasActive": fmt.Sprint(wasActive),
			}}); err != nil {
				return err
			}
		}
		if !wasActive {
			return nil
		}
		for _, record := range m.trusted(all) {
			if record.Alg == target.Alg && record.State == StateNext && !record.Expired(now) {
				return m.promote(ctx, tx, &record, now)
			}
		}
		return nil // the next reconcile pass bootstraps a replacement
	})
	if err != nil {
		return err
	}
	if !acquired {
		return ErrLockBusy
	}
	if m.Changed != nil {
		m.Changed()
	}
	return nil
}
