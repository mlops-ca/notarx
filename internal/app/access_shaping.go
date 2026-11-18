package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/repository"
)

// anonymousAbsentShaper keeps a name that does not exist from being
// distinguishable from a name that exists but is not visible (ADR 0029, "Deny
// shaping"). An anonymous read of a private repository is challenged with the
// format's 401; so is an anonymous read of a repository or group that does not
// exist, using the same challenge function. It wraps the handler that runs when
// no V2 group of the format has the name.
type anonymousAbsentShaper struct {
	format    repository.Format
	repos     repository.HostedRepositoryStore
	challenge func(http.ResponseWriter, *http.Request)
	next      http.Handler
}

func (h anonymousAbsentShaper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if access.IsRead(r.Method) && r.Header.Get("Authorization") == "" {
		if _, _, basic := r.BasicAuth(); !basic {
			if name := protocolRepositoryName(h.format, r.URL.Path); name != "" {
				if _, err := h.repos.GetHostedRepositoryByName(r.Context(), name); errors.Is(err, repository.ErrNotFound) {
					h.challenge(w, r)
					return
				}
			}
		}
	}
	h.next.ServeHTTP(w, r)
}

// protocolRepositoryName is the repository or group segment of a protocol path.
func protocolRepositoryName(format repository.Format, path string) string {
	var prefix string
	switch format {
	case repository.FormatPyPI:
		prefix = "/pypi/"
	case repository.FormatGo:
		prefix = "/go/"
	case repository.FormatCargo:
		prefix = "/cargo/"
	default:
		return nativeRepositoryName(format, path)
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" {
		return ""
	}
	return strings.Split(rest, "/")[0]
}
