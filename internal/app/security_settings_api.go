package app

import (
	"net/http"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
)

// GetSecuritySettings reports the authorization cache window (ADR 0023 section 6)
// to the Console. It is configuration, so there is no write counterpart.
func (h generatedRepositoryAPIAdapter) GetSecuritySettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.SecuritySettings{
		AuthorizationCacheEnabled:    h.authzCacheTTL > 0,
		AuthorizationCacheTtlSeconds: int(h.authzCacheTTL.Seconds()),
	})
}
