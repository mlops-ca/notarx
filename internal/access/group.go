package access

import (
	"context"
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

// WithCeiling marks ctx so the store enforces the ceiling in the transaction of the
// write (repository.WithGroupCeiling) when the mode is strict, and leaves it
// unmarked in lenient mode.
func (m GroupMode) WithCeiling(ctx context.Context) context.Context {
	if m.Enforced() {
		return repository.WithGroupCeiling(ctx)
	}
	return ctx
}
