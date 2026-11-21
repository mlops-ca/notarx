package app

import (
	helmapi "github.com/mlops-ca/notarx/internal/admin/helm"
	nugetapi "github.com/mlops-ca/notarx/internal/admin/nuget"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	rubygemsapi "github.com/mlops-ca/notarx/internal/admin/rubygems"
)

// managementServer composes domain handlers. Generated route and parameter
// binding is shared; authorization and persistence belong to each domain.
type managementServer struct {
	*nugetapi.Adapter
	*helmapi.Boundary
	*rubygemsapi.RubyGemsBoundary
	repositoryManagementHandler
	artifactManagementHandler
	accessManagementHandler
	identityManagementHandler
	workloadIdentityHandler
	remoteInstanceHandler
	promotionRuleHandler
	importHandler
	pypiYankManagementHandler
	rawVersionManagementHandler
	generatedRepositoryAPIAdapter
}

var _ adminopenapi.ServerInterface = managementServer{}
