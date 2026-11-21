package app

import (
	"context"
	"net/http"
	"sync"

	"github.com/mlops-ca/notarx/internal/remoteinstance"
)

type localInstanceIDReader interface {
	LocalInstanceID(context.Context) (string, error)
}

// instanceDocumentHandler serves GET /.well-known/notarx (api/openapi/protocols/notarx-instance.yaml): the id of
// this deployment, the instance protocol version, the formats accepted as import targets and the import limits.
// The route is anonymous, read-only and carries no credential or repository data.
type instanceDocumentHandler struct {
	ids    localInstanceIDReader
	limits remoteinstance.Limits
	cache  *instanceIDCache
}

// instanceIDCache keeps the id once read: it never changes for a deployment.
type instanceIDCache struct {
	mu sync.Mutex
	id string
}

// newInstanceDocumentHandler serves the instance document with the import limits the instance enforces; the zero value
// advertises the defaults.
func newInstanceDocumentHandler(ids localInstanceIDReader, limits remoteinstance.Limits) instanceDocumentHandler {
	if limits == (remoteinstance.Limits{}) {
		limits = remoteinstance.DefaultLimits()
	}
	return instanceDocumentHandler{ids: ids, limits: limits, cache: &instanceIDCache{}}
}

// instanceImportFormats lists the formats this instance accepts as import targets. The importer slices of ADR 0021
// (7b onward) add their formats here; none is accepted yet.
func instanceImportFormats() []string { return []string{} }

func (h instanceDocumentHandler) localID(ctx context.Context) (string, error) {
	h.cache.mu.Lock()
	defer h.cache.mu.Unlock()
	if h.cache.id != "" {
		return h.cache.id, nil
	}
	id, err := h.ids.LocalInstanceID(ctx)
	if err != nil {
		return "", err
	}
	h.cache.id = id
	return id, nil
}

func (h instanceDocumentHandler) serve(w http.ResponseWriter, r *http.Request) {
	id, err := h.localID(r.Context())
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "instance_unavailable", "the instance id is temporarily unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeNativeMavenJSON(w, http.StatusOK, remoteinstance.Document{InstanceID: id, APIVersion: remoteinstance.APIVersion, Formats: instanceImportFormats(), Limits: h.limits})
}

func registerInstanceRoutes(mux *runtimeLogMux, handler instanceDocumentHandler) {
	mux.HandleFunc("GET "+remoteinstance.WellKnownPath, handler.serve)
}
