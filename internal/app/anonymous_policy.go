package app

import (
	"context"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/repository"
)

const anonymousActor = "anonymous"

const (
	anonymousAuthorizationSource = "anonymous_policy"
	anonymousAuthorizationReason = "global_and_resource_policy_enabled"
)

func anonymousPrincipal() Principal {
	return Principal{Actor: anonymousActor}
}

func isAnonymous(principal Principal) bool {
	return principal.Actor == anonymousActor
}

// The helpers below are adapters over access.Decide, the only place that reads
// visibility and the public-access gate (ADR 0029). Handlers never inspect
// either themselves.

func anonymousReadMethod(method string) bool {
	return access.IsRead(method)
}

// anonymousAccessAllowed reports the instance gate. It remains for the browse
// and catalog paths that have not moved to Decide yet.
func anonymousAccessAllowed(ctx context.Context, source any) bool {
	return access.GateOpen(ctx, source)
}

// anonymousRepositoryDecision is the anonymous read decision for one repository.
func anonymousHostedRepositoryReadAllowed(ctx context.Context, gate any, repo repository.HostedRepository, method string) bool {
	return access.AnonymousRepositoryRead(ctx, gate, repo, method)
}

// anonymousGroupAllowed is the group-level anonymous decision, before members
// are resolved.
func anonymousGroupAllowed(ctx context.Context, gate any, group repository.HostedGroup, method string) bool {
	return access.AnonymousGroupRead(ctx, gate, group, method)
}

// anonymousGroupMembers keeps the members an anonymous caller may resolve: none
// when the group itself is not readable, otherwise the members that are
// themselves public. Members carry the visibility projected by BindMember.
func anonymousGroupMembers(ctx context.Context, gate any, group repository.HostedGroup, members []repository.Member) []repository.Member {
	return access.AnonymousGroupMembers(ctx, gate, group, members)
}

// anonymousHostedGroupReadAllowed reports whether an anonymous caller may read a
// group, which needs at least one active member of the group's format that is
// itself public. Members are read from the repository store.
func anonymousHostedGroupReadAllowed(ctx context.Context, gate any, repositories managementRepositoryReader, group repository.HostedGroup, method string) bool {
	members := make([]repository.Member, 0, len(group.Members))
	for _, member := range group.Members {
		repo, err := repositories.GetHostedRepository(ctx, member.RepositoryID)
		if err != nil || repo.State != repository.RepositoryActive || repo.Format != group.Format {
			continue
		}
		members = append(members, access.BindMember(repository.Member{RepositoryID: member.RepositoryID, Position: member.Position}, repo))
	}
	decision, resolved, _ := access.Decide(ctx, access.ReadRequest{Class: access.Anonymous, Method: method, Gate: gate, Group: &group, Members: members})
	return decision.Allow && len(resolved.Members) > 0
}

// anonymousLegacyGroupAllowed is the anonymous decision for a protocol-specific
// legacy group (OCI, Maven, Raw, Conan).
func anonymousLegacyGroupAllowed(ctx context.Context, gate any, group repository.Group, method string) bool {
	return access.AnonymousLegacyGroupRead(ctx, gate, group, method)
}

func (h OCIHandler) anonymousGroupRead(ctx context.Context, groupName, method string) bool {
	if h.Resolver.Store == nil {
		return false
	}
	group, err := h.Resolver.Store.GetGroup(ctx, groupName)
	return err == nil && anonymousLegacyGroupAllowed(ctx, h.Resolver.Store, group, method)
}

func (h MavenHandler) anonymousGroupRead(ctx context.Context, groupName, method string) bool {
	if h.Store == nil {
		return false
	}
	group, err := h.Store.GetMavenGroup(ctx, groupName)
	return err == nil && anonymousLegacyGroupAllowed(ctx, h.Store, group, method)
}

func (h ConanHandler) anonymousGroupRead(ctx context.Context, groupName, method string) bool {
	if h.Store == nil {
		return false
	}
	group, err := h.Store.GetConanGroup(ctx, groupName)
	return err == nil && anonymousLegacyGroupAllowed(ctx, h.Store, group, method)
}

// anonymousRead reports whether an anonymous caller may read the Conan remote
// with this name: a legacy group, or a native hosted or proxy repository.
func (h ConanHandler) anonymousRead(ctx context.Context, name, method string) bool {
	if h.anonymousGroupRead(ctx, name, method) {
		return true
	}
	if h.Repositories == nil || h.Store == nil {
		return false
	}
	repo, err := h.Repositories.GetHostedRepositoryByName(ctx, name)
	return err == nil && repo.Format == repository.FormatConan && anonymousHostedRepositoryReadAllowed(ctx, h.Store, repo, method)
}
