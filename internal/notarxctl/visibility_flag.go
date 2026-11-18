package notarxctl

import (
	"fmt"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
)

// parseVisibility validates a --visibility flag value (ADR 0029).
func parseVisibility(value string) (adminopenapi.Visibility, error) {
	visibility := adminopenapi.Visibility(value)
	if !visibility.Valid() {
		return "", &UsageError{Message: fmt.Sprintf("invalid --visibility %q", value), Hint: "use private, authenticated or public"}
	}
	return visibility, nil
}
