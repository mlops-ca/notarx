package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/mlops-ca/notarx/internal/repository"
)

// ErrRedirectRejected is returned when an upstream redirect is not permitted
// by the single-hop policy. Its text is the stable problem code
// upstream_redirect_rejected; callers match it with errors.Is.
var ErrRedirectRejected = errors.New("upstream_redirect_rejected")

// stripOnHop lists request headers that must never reach a redirect target:
// the target is a different origin (a CDN), so credentials stay behind.
var stripOnHop = []string{"Authorization", "Cookie", "Proxy-Authorization"}

// RedirectFollower follows exactly one upstream redirect under an explicit
// host allowlist. It is a controlled exception for formats whose upstream
// hands out one CDN redirect (Hugging Face); it does not relax any caller that
// refuses redirects, such as Raw.
type RedirectFollower struct {
	// Allowlist holds exact hosts ("cdn.example") and wildcard suffixes
	// ("*.example", which never matches the bare "example"). An empty
	// allowlist follows nothing.
	Allowlist []string
	// Proxy is the repository egress configuration applied to the hop; nil
	// keeps the environment default, exactly as for the origin request.
	Proxy *repository.EgressProxy
	Hooks Hooks
	// Client is the base client whose transport settings (TLS roots, timeouts)
	// the hop inherits. Nil uses a default client.
	Client *http.Client
}

// FollowOnce returns a follower for the given host allowlist.
func FollowOnce(allowlist []string, proxy *repository.EgressProxy, hooks Hooks, client *http.Client) *RedirectFollower {
	return &RedirectFollower{Allowlist: allowlist, Proxy: proxy, Hooks: hooks, Client: client}
}

// IsRedirect reports whether the status code is an HTTP redirect.
func IsRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// Follow performs the single permitted hop for the redirect response resp that
// answered from. The caller must have disabled redirect following on the
// origin request. resp is closed. The returned response is the hop's final
// answer; a second redirect is rejected.
func (f *RedirectFollower) Follow(ctx context.Context, from *http.Request, resp *http.Response) (*http.Response, error) {
	defer drainClose(resp.Body)
	if !IsRedirect(resp.StatusCode) {
		return nil, fmt.Errorf("%w: status %d is not a redirect", ErrRedirectRejected, resp.StatusCode)
	}
	if from.Method != http.MethodGet && from.Method != http.MethodHead {
		return nil, fmt.Errorf("%w: method %s is not redirectable", ErrRedirectRejected, from.Method)
	}
	location, err := resp.Location()
	if err != nil {
		return nil, fmt.Errorf("%w: missing or invalid Location", ErrRedirectRejected)
	}
	if location.Scheme != "https" || location.User != nil {
		return nil, fmt.Errorf("%w: target must be an https URL without credentials", ErrRedirectRejected)
	}
	if !f.allowed(location.Hostname()) {
		return nil, fmt.Errorf("%w: host is not allowlisted", ErrRedirectRejected)
	}
	hooks := f.Hooks
	hooks.DialContext = guardDial(hooks.DialContext)
	client, err := ApplyContext(ctx, f.Client, f.Proxy, location.String(), hooks)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRedirectRejected, err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	hop, err := http.NewRequestWithContext(ctx, from.Method, location.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRedirectRejected, err)
	}
	hop.Header = from.Header.Clone()
	if hop.Header == nil {
		hop.Header = http.Header{}
	}
	for _, name := range stripOnHop {
		hop.Header.Del(name)
	}
	next, err := client.Do(hop)
	if err != nil {
		return nil, fmt.Errorf("redirect hop: %w", err)
	}
	if IsRedirect(next.StatusCode) {
		drainClose(next.Body)
		return nil, fmt.Errorf("%w: second redirect", ErrRedirectRejected)
	}
	return next, nil
}

func (f *RedirectFollower) allowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	for _, entry := range f.Allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "*."):
			if strings.HasSuffix(host, entry[1:]) && len(host) > len(entry)-1 {
				return true
			}
		case host == entry:
			return true
		}
	}
	return false
}

// guardDial re-checks the address actually being dialed. The pinned transport
// already dials only addresses that passed the resolution check; this second
// check at dial time keeps a rebinding or hook regression from reaching a
// private network.
func guardDial(next func(ctx context.Context, network, address string) (net.Conn, error)) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil && PrivateAddress(ip) {
			return nil, errors.New("egress redirect target dials a private address")
		}
		return next(ctx, network, address)
	}
}

func drainClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}
