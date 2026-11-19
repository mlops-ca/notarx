package app

import (
	"net/http"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// listTotalResponse maps a store total onto the {value, relation} object of every listing (ADR 0023 section 8).
func listTotalResponse(total repository.ListTotal) adminopenapi.ListTotal {
	return adminopenapi.ListTotal{Value: total.Value, Relation: adminopenapi.ListTotalRelation(total.Relation)}
}

// rejectOffsetParameter answers 400 when a client still pages with the offset parameter that keyset pages
// replaced, instead of silently serving the first page on every call (ADR 0023 section 8).
func rejectOffsetParameter(w http.ResponseWriter, r *http.Request) bool {
	if !r.URL.Query().Has("offset") {
		return false
	}
	writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "offset paging was replaced by pageToken; continue with the nextPageToken of the previous page")
	return true
}
