package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Promotion rule target kinds (ADR 0021, "Rules"): a rule promotes into a local repository or pushes to a remote
// instance.
const (
	PromotionTargetLocal  = "local"
	PromotionTargetRemote = "remote"
)

var (
	// ErrPromotionRuleNameExists reports a second rule under an existing name.
	ErrPromotionRuleNameExists = errors.New("promotion rule name already exists")
	// ErrPromotionRuleLimit reports that the source repository already holds the most rules a deployment allows
	// (NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY).
	ErrPromotionRuleLimit = errors.New("promotion rule limit of the source repository reached")
	// ErrPromotionRuleReference reports a rule that names a repository, remote instance or service account that
	// does not exist.
	ErrPromotionRuleReference = errors.New("promotion rule references an unknown repository, remote instance or service account")
)

// PromotionRule is the stored configuration of one rule. Match, Conditions, Approval and Schedule are JSON documents
// whose shape internal/promotionrules owns; the store keeps them verbatim. The source repository never changes after
// the rule is created.
type PromotionRule struct {
	ID                 string
	Name               string
	SourceRepositoryID string
	TargetKind         string
	// TargetRepositoryID is set for a local target; RemoteInstanceID and RemoteRepository for a remote one.
	TargetRepositoryID    string
	RemoteInstanceID      string
	RemoteRepository      string
	Match                 json.RawMessage
	Conditions            json.RawMessage
	Approval              json.RawMessage
	Schedule              json.RawMessage
	RunAsServiceAccountID string
	Enabled               bool
	Version               string
	CreatedBy             string
	UpdatedBy             string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// PromotionRuleQuery pages rules by name. SourceRepositoryIDs, when not nil, restricts the page to rules whose source
// is one of them (the repositories the caller administers); an empty non-nil slice matches nothing.
type PromotionRuleQuery struct {
	Limit               int
	PageToken           string
	SourceRepositoryIDs []string
}

type PromotionRulePage struct {
	Items         []PromotionRule
	NextPageToken string
}

// PromotionRuleStore persists promotion rules. Every mutation takes the audit records of the change, which commit with
// it.
type PromotionRuleStore interface {
	// CreatePromotionRule inserts the rule and refuses it with ErrPromotionRuleLimit when its source repository
	// already has maxPerSource rules; the count and the insert are serialised per source repository.
	CreatePromotionRule(ctx context.Context, rule PromotionRule, maxPerSource int, audit ...AuditRecord) (PromotionRule, error)
	PagePromotionRules(context.Context, PromotionRuleQuery) (PromotionRulePage, error)
	GetPromotionRule(context.Context, string) (PromotionRule, error)
	// UpdatePromotionRule replaces the editable fields when the stored version equals the expected one, increments
	// the version and records updated_by.
	UpdatePromotionRule(ctx context.Context, rule PromotionRule, expectedVersion string, audit ...AuditRecord) (PromotionRule, error)
	DeletePromotionRule(ctx context.Context, id string, audit ...AuditRecord) error
}

// promotionRulesFilter binds a page token to the repository restriction of the listing it came from.
func promotionRulesFilter(query PromotionRuleQuery) string {
	if query.SourceRepositoryIDs == nil {
		return "promotion-rules"
	}
	ids := slices.Clone(query.SourceRepositoryIDs)
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return "promotion-rules:" + hex.EncodeToString(sum[:8])
}

func clonePromotionRule(rule PromotionRule) PromotionRule {
	rule.Match = slices.Clone(rule.Match)
	rule.Conditions = slices.Clone(rule.Conditions)
	if len(rule.Conditions) == 0 {
		rule.Conditions = json.RawMessage("{}")
	}
	rule.Approval = slices.Clone(rule.Approval)
	rule.Schedule = slices.Clone(rule.Schedule)
	return rule
}
