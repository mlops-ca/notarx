package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// GetIssuerStatus implements GET /issuer (ADR 0020): the configuration of the issuer and the lifecycle of its
// signing keys. Key material, sealed or not, is never part of the answer.
func (h identityManagementHandler) GetIssuerStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	status := adminopenapi.IssuerStatus{Keys: []adminopenapi.IssuerKey{}}
	if h.issuer.Enabled() {
		records, err := h.issuer.Manager.Store.ListPublicKeys(r.Context())
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read signing keys failed")
			return
		}
		sort.SliceStable(records, func(i, j int) bool {
			if records[i].Alg != records[j].Alg {
				return records[i].Alg < records[j].Alg
			}
			return records[i].CreatedAt.Before(records[j].CreatedAt)
		})
		for _, record := range records {
			status.Keys = append(status.Keys, adminopenapi.IssuerKey{
				Kid: record.KID, Alg: adminopenapi.IssuerKeyAlg(record.Alg), State: adminopenapi.IssuerKeyState(record.State),
				CreatedAt: record.CreatedAt.UTC(), PublishedAt: optionalTime(record.PublishedAt), ActivatedAt: optionalTime(record.ActivatedAt),
				RetiredAt: optionalTime(record.RetiredAt), ExpiresAt: optionalTime(record.ExpiresAt),
			})
		}
		config := h.issuer.Config
		algorithm, rotation, lead, maxLifetime := adminopenapi.IssuerStatusAlgorithm(config.Alg), config.KeyRotationInterval.String(), config.KeyPublishLead.String(), config.MaxTokenLifetime.String()
		status.Enabled, status.IssuerUrl, status.Algorithm = true, &config.URL, &algorithm
		status.RotationInterval, status.PublishLead, status.MaxTokenLifetime = &rotation, &lead, &maxLifetime
	}
	w.Header().Set("Cache-Control", "no-store")
	writeNativeMavenJSON(w, http.StatusOK, status)
}

// RotateIssuerKeys implements POST /issuer/rotation: an immediate rotation of the signing key of one algorithm.
func (h identityManagementHandler) RotateIssuerKeys(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var request adminopenapi.RotateIssuerKeys
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "invalid rotation request")
		return
	}
	if !h.issuer.Enabled() {
		writeHostedProblem(w, http.StatusNotFound, problemIssuerDisabled, "the OIDC issuer is disabled")
		return
	}
	algorithm := h.issuer.Config.Alg
	if request.Algorithm != nil {
		algorithm = string(*request.Algorithm)
	}
	if algorithm != jose.RS256 && algorithm != jose.ES256 {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "algorithm must be RS256 or ES256")
		return
	}
	result, err := h.issuer.Manager.RotateNow(r.Context(), algorithm, tokenissuer.WithActor(principal.Actor))
	if errors.Is(err, tokenissuer.ErrNoNextKey) {
		writeHostedProblem(w, http.StatusConflict, "no_next_key", "there is no next "+algorithm+" key to promote; a key pair exists only for an algorithm that is or was NOTARX_ISSUER_ALG")
		return
	}
	if problem, status, known := issuerLifecycleProblem(err); known {
		writeHostedProblem(w, status, problem, err.Error())
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "rotate signing keys failed")
		return
	}
	response := adminopenapi.IssuerRotation{Algorithm: adminopenapi.IssuerRotationAlgorithm(algorithm), Activated: result.Activated}
	if result.Retired != "" {
		response.Retired = &result.Retired
	}
	if result.Warning != "" {
		response.Warning = &result.Warning
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

// RevokeIssuerKey implements POST /issuer/keys/{kid}/revoke: the emergency removal of a compromised key.
func (h identityManagementHandler) RevokeIssuerKey(w http.ResponseWriter, r *http.Request, kid string) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !h.issuer.Enabled() {
		writeHostedProblem(w, http.StatusNotFound, problemIssuerDisabled, "the OIDC issuer is disabled")
		return
	}
	err := h.issuer.Manager.Revoke(r.Context(), kid, tokenissuer.WithActor(principal.Actor))
	if problem, status, known := issuerLifecycleProblem(err); known {
		writeHostedProblem(w, status, problem, err.Error())
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "revoke signing key failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// issuerLifecycleProblem maps the expected failures of a manual key operation to a problem.
func issuerLifecycleProblem(err error) (code string, status int, known bool) {
	switch {
	case errors.Is(err, tokenissuer.ErrKeyNotFound):
		return "key_not_found", http.StatusNotFound, true
	case errors.Is(err, tokenissuer.ErrNoNextKey):
		return "no_next_key", http.StatusConflict, true
	case errors.Is(err, tokenissuer.ErrLockBusy):
		return "issuer_busy", http.StatusConflict, true
	}
	return "", 0, false
}
