package app

import (
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// listTotalResponse maps a store total onto the {value, relation} object of every listing (ADR 0023 section 8).
func listTotalResponse(total repository.ListTotal) adminopenapi.ListTotal {
	return adminopenapi.ListTotal{Value: total.Value, Relation: adminopenapi.ListTotalRelation(total.Relation)}
}
