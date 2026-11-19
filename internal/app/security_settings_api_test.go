package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestSecuritySettingsReportAuthorizationCacheWindow(t *testing.T) {
	for _, tc := range []struct {
		ttl  time.Duration
		want string
	}{
		{5 * time.Second, `"authorizationCacheEnabled":true,"authorizationCacheTtlSeconds":5`},
		{time.Second, `"authorizationCacheEnabled":true,"authorizationCacheTtlSeconds":1`},
		{0, `"authorizationCacheEnabled":false,"authorizationCacheTtlSeconds":0`},
	} {
		handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: repository.NewMemoryStore(), Adapter: TestAdapter{}, Authenticator: testAuthenticator(), AuthzCacheTTL: tc.ttl})
		anonymous := httptest.NewRecorder()
		handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v2/security-settings", nil))
		if anonymous.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous read=%d", anonymous.Code)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/v2/security-settings", nil)
		authorize(request, "admin-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body := strings.ReplaceAll(response.Body.String(), " ", "")
		if response.Code != http.StatusOK || !strings.Contains(body, tc.want) {
			t.Fatalf("ttl %s: status=%d body=%s", tc.ttl, response.Code, body)
		}
	}
}
