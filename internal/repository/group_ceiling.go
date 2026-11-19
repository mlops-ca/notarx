package repository

import (
	"context"
	"errors"
	"sort"
)

// ErrVisibilityConflict is returned by the group and repository writes of a store
// when strict group mode is on (see WithGroupCeiling) and the write would leave a
// group more visible than one of its members (ADR 0029, "Group invariants").
var ErrVisibilityConflict = errors.New("group visibility exceeds the visibility of a member")

// VisibilityConflictError carries what a refused write needs to answer 409. It
// matches ErrVisibilityConflict with errors.Is.
type VisibilityConflictError struct {
	// Lowering is true when the refused write lowered a repository below a group
	// that contains it; Members is then empty. Otherwise Members are the members
	// of the written group whose visibility is below the group's.
	Lowering bool
	Members  []HostedRepository
}

func (e *VisibilityConflictError) Error() string { return ErrVisibilityConflict.Error() }

func (e *VisibilityConflictError) Is(target error) bool { return target == ErrVisibilityConflict }

type groupCeilingKey struct{}

// WithGroupCeiling asks the store to enforce the group visibility ceiling (strict
// mode) in the same transaction as the write that could break it. The writes are
// CreateHostedGroupIdempotently, ReplaceHostedGroup, ReplaceHostedGroupMembers and
// UpdateHostedRepository. A context without the mark writes unchecked (lenient mode).
func WithGroupCeiling(ctx context.Context) context.Context {
	return context.WithValue(ctx, groupCeilingKey{}, true)
}

func groupCeilingEnforced(ctx context.Context) bool {
	enforced, _ := ctx.Value(groupCeilingKey{}).(bool)
	return enforced
}

// LessVisibleMembers returns the members whose own visibility is below the group's:
// the members a group of that visibility serves to fewer callers than the group
// admits (silently invisible in lenient mode). members is keyed by repository id; a
// member missing from it is skipped.
func LessVisibleMembers(group HostedGroup, members map[string]HostedRepository) []VisibilityRef {
	ceiling := NormalizeVisibility(group.Visibility)
	var below []VisibilityRef
	for _, member := range group.Members {
		repo, ok := members[member.RepositoryID]
		if ok && repo.Visibility.Rank() < ceiling.Rank() {
			below = append(below, repo.VisibilityRef())
		}
	}
	sort.Slice(below, func(i, j int) bool { return below[i].Name < below[j].Name })
	return below
}
