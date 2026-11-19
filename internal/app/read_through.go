package app

import (
	"net/http"
	"strings"

	"github.com/mlops-ca/notarx/internal/readcache"
)

// managementPathPrefixes are the management API, login and token-minting routes.
// Protocol routes that happen to live under /api/ (RubyGems, Helm) are not listed.
var managementPathPrefixes = []string{
	"/api/v2/", "/auth/",
	"/api/v1/oci/", "/api/v1/maven/", "/api/v1/raw/", "/api/v1/conan/",
	"/api/v1/audits", "/api/v1/operations/",
}

// readThroughRequest reports whether a request must read credentials and account
// state from the database instead of the read caches (ADR 0023 section 6): every
// write, the management API, login and token minting. Only protocol reads use
// the authorization window.
func readThroughRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	for _, prefix := range managementPathPrefixes {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	return false
}

func readThroughManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if readThroughRequest(r) {
			r = r.WithContext(readcache.Bypass(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}
