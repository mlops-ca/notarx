package app

import (
	"net/http"
	"strings"

	"github.com/mlops-ca/notarx/internal/readcache"
)

// readThroughManagement makes the management API, login and token minting read
// credentials and account state from the database instead of the read caches
// (ADR 0023 section 6): only protocol reads use the authorization window.
func readThroughManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			r = r.WithContext(readcache.Bypass(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}
