// Package access is the single choke point for read access decisions (ADR 0029).
//
// Every repository and group read path calls Decide: protocol handlers, browse,
// search, download and effective-access. The per-handler anonymous helpers it
// replaced are deleted, and tools/architecture forbids reading repository
// visibility or the public-access policy outside this package and
// internal/repository.
//
// The decision is a function of the caller class, the HTTP method, the
// repository or group visibility, the instance gate (public_access_policy) and,
// for groups, the visibility of each member. It fails closed: an unknown
// visibility, a missing gate or a gate read error never widens access.
package access

import (
	"context"
	"errors"
	"net/http"

	"github.com/mlops-ca/notarx/internal/repository"
)

// Class is the kind of caller a decision is computed for.
type Class int

const (
	// Anonymous is a caller with no credential.
	Anonymous Class = iota
	// Authenticated is a valid non-anonymous identity without a repository grant.
	Authenticated
	// Granted is an identity with a grant (ADR 0006) or admin authority. The
	// grant itself is verified by the repository authorizer; Decide only keeps
	// visibility from narrowing it.
	Granted
)

// DenyKind is how a denial is shaped on the wire.
type DenyKind int

const (
	// DenyNone accompanies an allowed decision.
	DenyNone DenyKind = iota
	// Challenge401 answers an anonymous caller with the per-format
	// WWW-Authenticate challenge. A non-public name and an absent name are
	// indistinguishable.
	Challenge401
	// NotFound404 answers an authenticated caller without read, identical to a
	// missing resource.
	NotFound404
	// Forbidden403 answers an authenticated caller that can see the resource
	// but may not perform the method (a write without a grant).
	Forbidden403
)

// Decision is the outcome of Decide.
type Decision struct {
	Allow bool
	Deny  DenyKind
	// Visibility is the effective visibility after the instance gate caps
	// public to authenticated. It is only capped for anonymous callers, the only
	// class the gate can change the outcome for.
	Visibility repository.Visibility
	// RateClass keys the rate limiter (ADR 0029, "Anonymous rate limiting"):
	// "anon", "token" or "none".
	RateClass string
}

// ResolvedMembers is the member list a caller class may resolve, in the input
// order. It is computed per request; nothing derived is persisted.
type ResolvedMembers struct {
	Members []repository.Member
}

// ReadRequest describes one read decision. Exactly one of Repository, Group and
// LegacyGroup is set.
type ReadRequest struct {
	Class  Class
	Method string
	// Gate supplies the instance gate: a value with GetPublicAccessPolicy. A
	// missing or failing gate counts as closed.
	Gate any
	// Repository is a direct repository read.
	Repository *repository.HostedRepository
	// Group is a V2 group read; Members are its resolved candidates, whose
	// Visibility is the projection set by BindMember.
	Group   *repository.HostedGroup
	Members []repository.Member
	// LegacyGroup is a protocol-specific legacy group (OCI, Maven, Raw, Conan).
	LegacyGroup *repository.Group
}

// ErrInvalidRequest reports a ReadRequest with no or several targets.
var ErrInvalidRequest = errors.New("access: a read request needs exactly one target")

type gateReader interface {
	GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error)
}

// GateOpen reports whether the instance gate admits public visibility. It fails
// closed: a source without the policy, or a read error, is a closed gate.
func GateOpen(ctx context.Context, source any) (open bool) {
	reader, ok := source.(gateReader)
	if !ok || reader == nil {
		return false
	}
	// A typed nil source (a nil store pointer) panics on use; that is a closed gate.
	defer func() {
		if recover() != nil {
			open = false
		}
	}()
	policy, err := reader.GetPublicAccessPolicy(ctx)
	return err == nil && policy.Enabled
}

// IsRead reports whether the method is one an anonymous caller may ever use.
func IsRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func rateClass(class Class) string {
	switch class {
	case Anonymous:
		return "anon"
	case Authenticated:
		return "token"
	default:
		return "none"
	}
}

// Absent is the decision for a name that does not exist. It is identical to the
// denial of a non-public name for the same class, so existence never leaks.
func Absent(class Class, method string) Decision {
	return denied(class, method, false)
}

func denied(class Class, method string, visible bool) Decision {
	d := Decision{Allow: false, RateClass: rateClass(class)}
	switch {
	case class == Anonymous:
		d.Deny = Challenge401
	case visible && !IsRead(method):
		d.Deny = Forbidden403
	default:
		d.Deny = NotFound404
	}
	return d
}

// admits reports whether a caller class may read at an effective visibility.
func admits(class Class, visibility repository.Visibility) bool {
	switch class {
	case Anonymous:
		return visibility == repository.VisibilityPublic
	case Authenticated:
		return visibility.Rank() >= repository.VisibilityAuthenticated.Rank()
	case Granted:
		return visibility.Valid()
	default:
		return false
	}
}

// effective caps public at authenticated while the gate is closed. The gate is
// only consulted for an anonymous caller of a public target.
func effective(ctx context.Context, class Class, visibility repository.Visibility, gate any) repository.Visibility {
	if visibility == repository.VisibilityPublic && class == Anonymous && !GateOpen(ctx, gate) {
		return repository.VisibilityAuthenticated
	}
	return visibility
}

func decideVisibility(ctx context.Context, req ReadRequest, visibility repository.Visibility) Decision {
	if !visibility.Valid() {
		// Fail closed for every class: a value outside the enum admits nobody.
		return denied(req.Class, req.Method, false)
	}
	eff := effective(ctx, req.Class, visibility, req.Gate)
	if req.Class != Granted && !IsRead(req.Method) {
		// Writes always need a grant; an anonymous write is challenged, an
		// authenticated one is refused only when the target is visible to it.
		return denied(req.Class, req.Method, admits(req.Class, eff))
	}
	if !admits(req.Class, eff) {
		return denied(req.Class, req.Method, false)
	}
	return Decision{Allow: true, Visibility: eff, RateClass: rateClass(req.Class)}
}

// Decide is the read decision for one repository or group.
func Decide(ctx context.Context, req ReadRequest) (Decision, ResolvedMembers, error) {
	targets := 0
	if req.Repository != nil {
		targets++
	}
	if req.Group != nil {
		targets++
	}
	if req.LegacyGroup != nil {
		targets++
	}
	if targets != 1 {
		return denied(req.Class, req.Method, false), ResolvedMembers{}, ErrInvalidRequest
	}
	switch {
	case req.Repository != nil:
		repo := *req.Repository
		if req.Class != Granted && repo.State != repository.RepositoryActive {
			return denied(req.Class, req.Method, false), ResolvedMembers{}, nil
		}
		return decideVisibility(ctx, req, repo.Visibility), ResolvedMembers{}, nil
	case req.Group != nil:
		decision := decideVisibility(ctx, req, req.Group.Visibility)
		if !decision.Allow {
			return decision, ResolvedMembers{}, nil
		}
		return decision, ResolvedMembers{Members: filterMembers(ctx, req, req.Members)}, nil
	default:
		return decideLegacy(ctx, req)
	}
}

// filterMembers keeps the members whose own effective visibility admits the
// caller. A granted caller keeps every member; the repository authorizer then
// applies per-member grants. Members are filtered before any further lookup.
func filterMembers(ctx context.Context, req ReadRequest, members []repository.Member) []repository.Member {
	kept := make([]repository.Member, 0, len(members))
	for _, member := range members {
		if req.Class == Granted || admits(req.Class, effective(ctx, req.Class, member.Visibility, req.Gate)) {
			kept = append(kept, member)
		}
	}
	return kept
}

// BindMember projects a member repository's visibility onto a resolved member
// so a group decision can filter without a second read. The projection is
// computed per request and never stored.
func BindMember(member repository.Member, repo repository.HostedRepository) repository.Member {
	member.Visibility = repo.Visibility
	return member
}

// decideLegacy covers the protocol-specific legacy groups. They have no
// per-repository visibility: a legacy group is public to anonymous callers only
// when it is enabled and marked anonymous, and every member is then admitted.
func decideLegacy(ctx context.Context, req ReadRequest) (Decision, ResolvedMembers, error) {
	group := req.LegacyGroup
	visibility := repository.VisibilityPrivate
	if group.Enabled && group.Anonymous {
		visibility = repository.VisibilityPublic
	}
	if !group.Enabled && req.Class != Granted {
		return denied(req.Class, req.Method, false), ResolvedMembers{}, nil
	}
	decision := decideVisibility(ctx, req, visibility)
	if !decision.Allow {
		return decision, ResolvedMembers{}, nil
	}
	return decision, ResolvedMembers{Members: group.Members}, nil
}

// The helpers below are the anonymous shortcuts most handlers need. Each is a
// thin call to Decide so no handler composes its own rule.

// AnonymousRepositoryRead reports whether an anonymous caller may read repo.
func AnonymousRepositoryRead(ctx context.Context, gate any, repo repository.HostedRepository, method string) bool {
	decision, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: method, Gate: gate, Repository: &repo})
	return decision.Allow
}

// AnonymousGroupRead is the group-level anonymous decision, before members are
// resolved.
func AnonymousGroupRead(ctx context.Context, gate any, group repository.HostedGroup, method string) bool {
	decision, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: method, Gate: gate, Group: &group})
	return decision.Allow
}

// AnonymousGroupMembers keeps the members an anonymous caller may resolve: none
// when the group is not readable, otherwise the members that are themselves
// public. Members carry the visibility projected by BindMember.
func AnonymousGroupMembers(ctx context.Context, gate any, group repository.HostedGroup, members []repository.Member) []repository.Member {
	_, resolved, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: gate, Group: &group, Members: members})
	return resolved.Members
}

// AnonymousLegacyGroupRead is the anonymous decision for a protocol-specific
// legacy group.
func AnonymousLegacyGroupRead(ctx context.Context, gate any, group repository.Group, method string) bool {
	decision, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: method, Gate: gate, LegacyGroup: &group})
	return decision.Allow
}

type fixedGate bool

func (g fixedGate) GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error) {
	return repository.PublicAccessPolicy{Enabled: bool(g)}, nil
}

// FixedGate is a gate snapshot for callers that decide many targets in one
// request (the public catalog) and must not read the policy for each one.
func FixedGate(open bool) any { return fixedGate(open) }

// Effective is the explanation the effective-access API gives for an anonymous
// caller. It is computed with Decide, so the simulator cannot drift from
// enforcement.
type Effective struct {
	Visibility repository.Visibility
	Allowed    bool
	Reason     string
}

// Reasons reported by Effective. They are stable API values.
const (
	ReasonRepositoryNotActive = "repository_not_active"
	ReasonGateDisabled        = "global_anonymous_access_disabled"
	ReasonRepositoryDisabled  = "repository_anonymous_read_disabled"
	ReasonRepositoryEnabled   = "repository_anonymous_read_enabled"
)

// DescribeAnonymous explains the anonymous read decision for a repository.
func DescribeAnonymous(ctx context.Context, gate any, repo repository.HostedRepository) Effective {
	decision, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: gate, Repository: &repo})
	report := Effective{Visibility: repo.Visibility, Allowed: decision.Allow}
	switch {
	case decision.Allow:
		report.Reason = ReasonRepositoryEnabled
	case repo.State != repository.RepositoryActive:
		report.Reason = ReasonRepositoryNotActive
	case !GateOpen(ctx, gate):
		report.Reason = ReasonGateDisabled
	default:
		report.Reason = ReasonRepositoryDisabled
	}
	return report
}

// PolicyVersion is the version of the public-access policy, for callers that
// fingerprint a navigation state which must be invalidated when the gate
// changes. Unlike GateOpen it reports a read error.
func PolicyVersion(ctx context.Context, source any) (string, error) {
	reader, ok := source.(gateReader)
	if !ok || reader == nil {
		return "", repository.ErrDisabled
	}
	policy, err := reader.GetPublicAccessPolicy(ctx)
	return policy.Version, err
}
