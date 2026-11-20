// Package rubygems implements the explicitly enabled RubyGems Hosted preview.
package rubygems

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/authorization"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	protocol "github.com/mlops-ca/notarx/internal/protocol/rubygems"
	"github.com/mlops-ca/notarx/internal/repository"
	dist "github.com/mlops-ca/notarx/internal/rubygemsdistribution"
	domain "github.com/mlops-ca/notarx/internal/rubygemspublication"
)

type RepositoryStore interface {
	GetHostedRepository(context.Context, string) (repository.HostedRepository, error)
	GetHostedRepositoryByName(context.Context, string) (repository.HostedRepository, error)
	CreateHostedRepositoryIdempotently(context.Context, repository.HostedRepository, string, string, string, ...repository.AuditRecord) (repository.HostedRepository, bool, error)
}
type AnonymousPolicy interface {
	GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error)
}
type Options struct {
	Store         domain.Store
	Distribution  dist.Store
	Repositories  RepositoryStore
	Objects       objectstore.Store
	Authenticator authorization.Authenticator
	Authorizer    authorization.RepositoryAuthorizer
	Audit         domain.Auditor
	Anonymous     AnonymousPolicy
	MaxGemBytes   int64
}
type Module struct {
	store        domain.Store
	distribution dist.Store
	repos        RepositoryStore
	objects      objectstore.Store
	auth         authorization.Authenticator
	access       authorization.RepositoryAuthorizer
	audit        domain.Auditor
	anonymous    AnonymousPolicy
	maxBytes     int64
}

func nilPort(v any) bool {
	if v == nil {
		return true
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return reflect.ValueOf(v).IsNil()
	}
	return false
}
func New(o Options) (*Module, error) {
	for _, p := range []any{o.Store, o.Repositories, o.Audit, o.Anonymous, o.Authorizer.Grants} {
		if nilPort(p) {
			return nil, errors.New("RubyGems preview requires explicit publication, repository, authorization, audit and anonymous-policy ports")
		}
	}
	if o.Distribution != nil && nilPort(o.Distribution) {
		return nil, errors.New("RubyGems replication store is typed nil")
	}
	if e := objectstore.Validate(o.Objects); e != nil {
		return nil, e
	}
	if o.MaxGemBytes == 0 {
		o.MaxGemBytes = protocol.MaxGemBytes
	}
	if o.MaxGemBytes < 1 || o.MaxGemBytes > protocol.MaxGemBytes {
		return nil, errors.New("RubyGems archive limit must be between 1 and 64 MiB")
	}
	return &Module{store: o.Store, distribution: o.Distribution, repos: o.Repositories, objects: o.Objects, auth: o.Authenticator, access: o.Authorizer, audit: o.Audit, anonymous: o.Anonymous, maxBytes: o.MaxGemBytes}, nil
}
func (*Module) Format() repository.Format { return repository.FormatRubyGems }
func (m *Module) Routes() []formatmodule.Route {
	return []formatmodule.Route{{Pattern: "/rubygems/", Handler: m}}
}
func (m *Module) Preview() http.Handler { return http.HandlerFunc(m.servePreview) }
func (m *Module) principal(r *http.Request) (authorization.Principal, bool) {
	if len(r.Header.Values("Authorization")) != 1 {
		return authorization.Principal{}, false
	}
	header := r.Header.Get("Authorization")
	if _, password, basic := r.BasicAuth(); basic {
		return m.auth.AuthenticateContext(r.Context(), "Bearer "+password)
	}
	if strings.HasPrefix(header, "Bearer ") {
		return m.auth.AuthenticateContext(r.Context(), header)
	}
	return m.auth.AuthenticateContext(r.Context(), "Bearer "+header)
}
func (m *Module) authorize(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, op authorization.RepositoryOperation, resource string, collection bool) (authorization.Principal, bool) {
	p, ok := m.principal(r)
	if !ok && r.Header.Get("Authorization") == "" && op == authorization.RepositoryRead && access.AnonymousRepositoryRead(r.Context(), m.anonymous, repo, r.Method) {
		return authorization.Principal{Actor: "anonymous"}, true
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="notarx RubyGems"`)
		m.problem(w, 401, "authentication_required")
		return p, false
	}
	d := m.access.AuthorizeResource(r.Context(), p, repo, op, resource)
	if !d.Allowed && collection && p.AccountStateReason() == "" {
		if set, e := m.access.Grants.GetRepositoryGrants(r.Context(), repo.ID); e == nil {
			for _, g := range set.Grants {
				if p.NamesGrantPrincipal(g.Principal) && g.ResourcePrefix != "" {
					candidate := m.access.AuthorizeResource(r.Context(), p, repo, op, g.ResourcePrefix)
					if candidate.Allowed {
						d = candidate
						break
					}
				}
			}
		}
	}
	if !d.Allowed {
		_ = m.audit.RecordAudit(r.Context(), repository.AuditRecord{Actor: p.Actor, Repository: repo.Name, GroupName: repo.Name, Format: "rubygems", Resource: resource, Operation: string(op), Outcome: repository.AuditAccessDenied, Status: 403, OccurredAt: time.Now().UTC(), AuthorizationSource: d.Source, AuthorizationReason: d.Reason})
		m.problem(w, 403, "access_denied")
		return p, false
	}
	return p, true
}
func (m *Module) allowed(ctx context.Context, p authorization.Principal, repo repository.HostedRepository, name string) bool {
	return p.Actor == "anonymous" && p.AuthenticationKind == "" || m.access.AuthorizeResource(ctx, p, repo, authorization.RepositoryRead, name).Allowed
}
func (m *Module) repository(ctx context.Context, key string, byID bool) (repository.HostedRepository, error) {
	var r repository.HostedRepository
	var e error
	if byID {
		r, e = m.repos.GetHostedRepository(ctx, key)
	} else {
		r, e = m.repos.GetHostedRepositoryByName(ctx, key)
	}
	if e == nil && !domain.Active(r) {
		e = repository.ErrNotFound
	}
	return r, e
}
func (m *Module) problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "status": status, "code": code, "title": code})
}
func (m *Module) failure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, repository.ErrNotFound), errors.Is(e, objectstore.ErrNotFound):
		m.problem(w, 404, "not_found")
	case errors.Is(e, domain.ErrAccessDenied):
		m.problem(w, 403, "access_denied")
	case errors.Is(e, repository.ErrVersionConflict):
		m.problem(w, 409, "governance_version_conflict")
	case errors.Is(e, domain.ErrConflict), errors.Is(e, repository.ErrNameExists), errors.Is(e, repository.ErrIdempotencyConflict):
		m.problem(w, 409, "immutable_identity_conflict")
	case errors.Is(e, repository.ErrQuotaExceeded):
		m.problem(w, 409, "quota_exceeded")
	case errors.Is(e, domain.ErrInvalid):
		m.problem(w, 400, "invalid_publication")
	case errors.Is(e, domain.ErrExpired), errors.Is(e, domain.ErrState):
		m.problem(w, 409, "upload_expired")
	default:
		m.problem(w, 500, "publication_unavailable")
	}
}
func (m *Module) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != path.Clean(r.URL.Path) || strings.ContainsAny(r.URL.Path, "\\\x00\r\n") || r.URL.RawQuery != "" {
		m.problem(w, 404, "not_found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/rubygems/"), "/")
	if len(parts) < 2 || !strings.HasPrefix(r.URL.Path, "/rubygems/") {
		m.problem(w, 404, "not_found")
		return
	}
	repo, e := m.repository(r.Context(), parts[0], false)
	if e != nil {
		if m.anonymousAbsent(w, r, e) {
			return
		}
		m.failure(w, e)
		return
	}
	relative := strings.Join(parts[1:], "/")
	if relative == "api/v1/gems/yank" {
		m.nativeYank(w, r, repo)
		return
	}
	if relative == "api/v1/gems" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		p, ok := m.authorize(w, r, repo, authorization.RepositoryWrite, "", true)
		if ok {
			m.push(w, r, repo, p)
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	m.read(w, r, repo, relative)
}

// anonymousAbsent answers an anonymous read of a name that does not exist
// exactly like one that is not visible, so existence never leaks (ADR 0029,
// "Deny shaping").
func (m *Module) anonymousAbsent(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, repository.ErrNotFound) || !access.IsRead(r.Method) || r.Header.Get("Authorization") != "" {
		return false
	}
	if _, ok := m.principal(r); ok {
		return false
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="notarx RubyGems"`)
	m.problem(w, 401, "authentication_required")
	return true
}
