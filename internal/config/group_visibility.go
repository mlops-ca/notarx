package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	// GroupVisibilityStrict refuses a group more visible than its least visible
	// member (ADR 0029). It is the default.
	GroupVisibilityStrict = "strict"
	// GroupVisibilityLenient keeps computed member filtering only and exists for
	// migrating existing layouts.
	GroupVisibilityLenient = "lenient"
)

// loadGroupVisibilityMode reads NOTARX_GROUP_VISIBILITY_MODE.
func loadGroupVisibilityMode() (string, error) {
	mode := strings.TrimSpace(os.Getenv("NOTARX_GROUP_VISIBILITY_MODE"))
	switch mode {
	case "":
		return GroupVisibilityStrict, nil
	case GroupVisibilityStrict, GroupVisibilityLenient:
		return mode, nil
	default:
		return "", fmt.Errorf("NOTARX_GROUP_VISIBILITY_MODE must be %s or %s", GroupVisibilityStrict, GroupVisibilityLenient)
	}
}
