package app

import (
	"net/http"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// EntraHTTPDoer returns the request sender of the entra-workload token exchanges. It runs each
// request through the same pinned-DNS egress path as every other upstream call: https only,
// reserved and private addresses refused, the environment proxy honoured, and redirects never
// followed. The only host a request may reach is the origin of its own URL, which the exchange
// code has already pinned to the configured Entra authority or the registry being served.
func EntraHTTPDoer(client UpstreamClient) func(*http.Request) (*http.Response, error) {
	if client.HTTPClient == nil {
		client.HTTPClient = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return func(request *http.Request) (*http.Response, error) {
		member := repository.Member{Type: repository.MemberProxy, Name: "entra-exchange", Endpoint: request.URL.Scheme + "://" + request.URL.Host}
		return client.doProxyUpstreamOnce(request.Context(), member, request)
	}
}
