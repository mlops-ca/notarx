package access

import (
	"fmt"

	"github.com/mlops-ca/notarx/internal/repository"
)

// GroupMode is how group visibility invariants are enforced on writes (ADR 0029,
// "Group invariants"). It is configured by NOTARX_GROUP_VISIBILITY_MODE.
type GroupMode string

const (
	// GroupStrict (the default) refuses a group that is more visible than its
	// least visible member, and lowering a repository below a group that
	// contains it.
	GroupStrict GroupMode = "strict"
	// GroupLenient keeps computed filtering only: members above the caller's
	// class are silently invisible. It exists for migrating existing layouts.
	GroupLenient GroupMode = "lenient"
)

// ParseGroupMode reads the configured mode; the empty value is strict.
func ParseGroupMode(value string) (GroupMode, error) {
	switch GroupMode(value) {
	case "", GroupStrict:
		return GroupStrict, nil
	case GroupLenient:
		return GroupLenient, nil
	default:
		return "", fmt.Errorf("group visibility mode must be strict or lenient, not %q", value)
	}
}

// Enforced reports whether writes are checked. The zero value enforces, so a
// handler built without the option is strict.
func (m GroupMode) Enforced() bool { return m != GroupLenient }

// BelowCeiling returns the member repositories whose own visibility is below
// the group's: the members a strict-mode write must refuse. A group whose
// visibility is private has no ceiling to violate.
func BelowCeiling(group repository.HostedGroup, members []repository.HostedRepository) []repository.HostedRepository {
	ceiling := repository.NormalizeVisibility(group.Visibility)
	var below []repository.HostedRepository
	for _, member := range members {
		if member.Visibility.Rank() < ceiling.Rank() {
			below = append(below, member)
		}
	}
	return below
}

// GroupsAbove returns the groups that contain repositoryID and are more visible
// than lowered: the groups a repository cannot be lowered below in strict mode.
func GroupsAbove(lowered repository.Visibility, repositoryID string, groups []repository.HostedGroup) []repository.HostedGroup {
	var above []repository.HostedGroup
	for _, group := range groups {
		if repository.NormalizeVisibility(group.Visibility).Rank() <= lowered.Rank() {
			continue
		}
		for _, member := range group.Members {
			if member.RepositoryID == repositoryID {
				above = append(above, group)
				break
			}
		}
	}
	return above
}

// LowersRepository reports whether next is a lower tier than the repository's.
func LowersRepository(repo repository.HostedRepository, next repository.Visibility) bool {
	return repository.NormalizeVisibility(next).Rank() < repository.NormalizeVisibility(repo.Visibility).Rank()
}
