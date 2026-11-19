package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEntraHTTPDoerRefusesPlainHTTPAndReservedAddresses(t *testing.T) {
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(server.Close)
	do := EntraHTTPDoer(UpstreamClient{})
	for _, target := range []string{server.URL + "/token", "https://127.0.0.1:1/token", "https://169.254.169.254/token", "https://10.0.0.1/token"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader("a=b"))
		if err != nil {
			t.Fatal(err)
		}
		if response, err := do(request); err == nil {
			_ = response.Body.Close()
			t.Errorf("%s must be refused", target)
		}
	}
	if reached {
		t.Fatal("a refused target must never be contacted")
	}
}

func TestEntraHTTPDoerDoesNotFollowRedirects(t *testing.T) {
	var contacted bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted = true }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	do := EntraHTTPDoer(UpstreamClient{AllowHTTPForTesting: true})
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, redirector.URL+"/x", strings.NewReader("a=b"))
	// The redirect leaves the pinned origin, so it is refused instead of followed.
	if response, err := do(request); err == nil {
		_ = response.Body.Close()
		t.Fatal("a redirect away from the token host must not be followed")
	}
	if contacted {
		t.Fatal("the redirect target was contacted")
	}
}
