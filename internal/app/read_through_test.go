package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mlops-ca/notarx/internal/readcache"
)

func TestReadThroughBypassesCachesForWritesAndManagementOnly(t *testing.T) {
	var bypassed bool
	handler := readThroughManagement(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		bypassed = readcache.Bypassed(r.Context())
	}))
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v2/api-keys", true},
		{http.MethodGet, "/api/v2/groups/x/browse", true},
		{http.MethodGet, "/api/v1/audits", true},
		{http.MethodGet, "/api/v1/operations/cache", true},
		{http.MethodGet, "/auth/session", true},
		{http.MethodGet, "/auth/token", true},
		{http.MethodPut, "/repository/maven/releases/a/b.jar", true},
		{http.MethodPost, "/v2/library/alpine/blobs/uploads/", true},
		{http.MethodDelete, "/npm/repo/pkg", true},
		{http.MethodGet, "/v2/library/alpine/manifests/3.1", false},
		{http.MethodHead, "/maven/repo/a/b.jar", false},
		{http.MethodGet, "/api/v1/gems/rake.json", false},
		{http.MethodGet, "/api/charts", false},
		{http.MethodGet, "/livez", false},
	} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
		if bypassed != tc.want {
			t.Errorf("%s %s bypassed=%t, want %t", tc.method, tc.path, bypassed, tc.want)
		}
	}
}
