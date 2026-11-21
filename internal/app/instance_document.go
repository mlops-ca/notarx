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
	ids     localInstanceIDReader
	limits  remoteinstance.Limits
	formats []string
	cache   *instanceIDCache
}

// instanceIDCache keeps the id once read: it never changes for a deployment.
type instanceIDCache struct {
	mu sync.Mutex
	id string
}

// newInstanceDocumentHandler serves the instance document with the formats that accept imports and the import limits the
// instance enforces; the zero limits advertise the defaults.
func newInstanceDocumentHandler(ids localInstanceIDReader, limits remoteinstance.Limits, formats []string) instanceDocumentHandler {
	if limits == (remoteinstance.Limits{}) {
		limits = remoteinstance.DefaultLimits()
	}
	return instanceDocumentHandler{ids: ids, limits: limits, formats: formats, cache: &instanceIDCache{}}
}

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
	writeNativeMavenJSON(w, http.StatusOK, remoteinstance.Document{InstanceID: id, APIVersion: remoteinstance.APIVersion, Formats: h.formats, Limits: h.limits})
}

func registerInstanceRoutes(mux *runtimeLogMux, handler instanceDocumentHandler) {
	mux.HandleFunc("GET "+remoteinstance.WellKnownPath, handler.serve)
}
