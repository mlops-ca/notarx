package repository

// Visibility is the ordered read tier of a repository or group (ADR 0029):
// private < authenticated < public. The zero value is treated as private
// everywhere a caller normalises input with NormalizeVisibility; a value outside
// the enum never admits anyone (fail closed).
type Visibility string

const (
	VisibilityPrivate       Visibility = "private"
	VisibilityAuthenticated Visibility = "authenticated"
	VisibilityPublic        Visibility = "public"
)

// Valid reports whether v is one of the three defined tiers.
func (v Visibility) Valid() bool {
	return v.Rank() >= 0
}

// Rank orders the tiers; an unknown value ranks -1 so it is below private and
// can never satisfy a minimum.
func (v Visibility) Rank() int {
	switch v {
	case VisibilityPrivate:
		return 0
	case VisibilityAuthenticated:
		return 1
	case VisibilityPublic:
		return 2
	default:
		return -1
	}
}

// NormalizeVisibility maps the empty value to private and leaves any other
// value untouched so validation can reject it.
func NormalizeVisibility(v Visibility) Visibility {
	if v == "" {
		return VisibilityPrivate
	}
	return v
}

// MinVisibility returns the lower of two tiers; an unknown value wins so the
// result stays fail-closed.
func MinVisibility(a, b Visibility) Visibility {
	if a.Rank() <= b.Rank() {
		return a
	}
	return b
}

// VisibilityRef names a repository and its visibility in a visibility_conflict
// problem (ADR 0029, strict group mode).
type VisibilityRef struct {
	RepositoryID string     `json:"repositoryId"`
	Name         string     `json:"name"`
	Visibility   Visibility `json:"visibility"`
}

// VisibilityRef is the repository's reference at its stored visibility.
func (r HostedRepository) VisibilityRef() VisibilityRef {
	return VisibilityRef{RepositoryID: r.ID, Name: r.Name, Visibility: r.Visibility}
}

// VisibilityRefAt is the reference at a requested visibility.
func (r HostedRepository) VisibilityRefAt(visibility Visibility) VisibilityRef {
	return VisibilityRef{RepositoryID: r.ID, Name: r.Name, Visibility: visibility}
}
