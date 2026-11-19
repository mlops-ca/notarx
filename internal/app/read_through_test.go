package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mlops-ca/notarx/internal/readcache"
)

func TestReadThroughManagementBypassesCachesOnlyForManagementPaths(t *testing.T) {
	var bypassed bool
	handler := readThroughManagement(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		bypassed = readcache.Bypassed(r.Context())
	}))
	for path, want := range map[string]bool{
		"/api/v1/api-keys":                 true,
		"/api/v2/groups/x/browse":          true,
		"/auth/login":                      true,
		"/auth/token":                      true,
		"/v2/library/alpine/manifests/3.1": false,
		"/maven/repo/a/b.jar":              false,
		"/livez":                           false,
	} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if bypassed != want {
			t.Errorf("%s bypassed=%t, want %t", path, bypassed, want)
		}
	}
}
