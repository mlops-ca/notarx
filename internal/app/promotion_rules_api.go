package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/durableactor"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/repository"
)

// defaultPromotionMaxRules is the rule cap per source repository when the deployment configures none
// (NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY, ADR 0021).
const defaultPromotionMaxRules = 32

const promotionRuleBodyLimit = 64 << 10

// promotionRuleHandler owns the /promotion-rules management resources (ADR 0021, "Rules"). A rule is privilege-bearing:
// it runs as a service account, so creating or editing one needs repository administration on both repositories and
// at least the permissions of that service account on both (the confused-deputy guard), unless the caller is a platform
// administrator, who administers every service account.
type promotionRuleHandler struct {
	managementScope
	rules       repository.PromotionRuleStore
	remotes     repository.RemoteInstanceStore
	accounts    repository.ServiceAccountStore
	grantIndex  repository.RepositoryGrantIndex
	registry    *formatmodule.Registry
	maxRules    int
	dryRunPorts promotionDryRunPorts
}

func newPromotionRuleHandler(scope managementScope, store GatewayStore, legacy generatedRepositoryAPIAdapter, maxRules int) promotionRuleHandler {
	grantIndex, _ := any(store).(repository.RepositoryGrantIndex)
	search, _ := any(store).(repository.ArtifactSearchStore)
	rawVersions, _ := any(store).(repository.NativeRawVersionStore)
	inventory, _ := any(store).(dryRunInventory)
	return promotionRuleHandler{
		managementScope: scope, rules: store, remotes: store, accounts: store, grantIndex: grantIndex, maxRules: maxRules,
		registry: promotionFormatRegistry(promotionModuleDeps{Store: store, Objects: legacy.objects[repository.FormatOCI]}),
		dryRunPorts: promotionDryRunPorts{
			securityPolicies: store, intelligence: store, search: search, rawVersions: rawVersions, inventory: inventory, quarantine: store,
			distributionDigests: legacy.artifactDistributionDigests,
		},
	}
}

func (h promotionRuleHandler) auditRecords(principal Principal, source repository.HostedRepository, rule repository.PromotionRule, operation string, status int) []repository.AuditRecord {
	if h.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(repository.AuditRecord{
		Actor: principal.Actor, Repository: source.Name, GroupName: source.Name, Resource: "promotion-rules/" + rule.ID, Representation: "version=" + rule.Version,
		Operation: operation, Status: status,
		Evidence: map[string]string{"ruleId": rule.ID, "ruleName": rule.Name, "version": rule.Version, "updatedBy": principal.Actor, "runAs": durableactor.ServiceAccountActorPrefix + rule.RunAsServiceAccountID},
	})}
}

// sourceCapabilities reads what the source format can do from the registry: the Source the match validates against.
func (h promotionRuleHandler) sourceCapabilities(source repository.HostedRepository) (promotionrules.Source, bool) {
	if _, ok := formatmodule.Capability[formatmodule.PromotionPublisher](h.registry, source.Format); !ok {
		return promotionrules.Source{}, false
	}
	_, tagged := formatmodule.Capability[formatmodule.Tagged](h.registry, source.Format)
	_, releaseSet := formatmodule.Capability[formatmodule.ReleaseSet](h.registry, source.Format)
	return promotionrules.Source{
		VersionedGeneric: source.Format == repository.FormatRaw && source.RawLayout == repository.RawLayoutVersioned,
		Tagged:           tagged, ReleaseSet: releaseSet,
	}, true
}

func matchFromAPI(value adminopenapi.PromotionRuleMatch) promotionrules.Match {
	match := promotionrules.Match{}
	if value.Coordinate.Glob != nil {
		match.Coordinate.Glob = *value.Coordinate.Glob
	}
	if value.Coordinate.Regex != nil {
		match.Coordinate.Regex = *value.Coordinate.Regex
	}
	if value.Version != nil {
		match.Version = &promotionrules.VersionRange{Range: value.Version.Range}
	}
	if value.Refs != nil {
		match.Refs = *value.Refs
	}
	if value.Properties != nil {
		match.Properties = *value.Properties
	}
	if value.Scope != nil {
		match.Scope = string(*value.Scope)
	}
	return match
}

func matchToAPI(match promotionrules.Match) adminopenapi.PromotionRuleMatch {
	value := adminopenapi.PromotionRuleMatch{}
	if match.Coordinate.Glob != "" {
		value.Coordinate.Glob = &match.Coordinate.Glob
	}
	if match.Coordinate.Regex != "" {
		value.Coordinate.Regex = &match.Coordinate.Regex
	}
	if match.Version != nil {
		value.Version = &adminopenapi.PromotionRuleVersionRange{Range: match.Version.Range}
	}
	if len(match.Refs) > 0 {
		value.Refs = &match.Refs
	}
	if len(match.Properties) > 0 {
		value.Properties = &match.Properties
	}
	scope := adminopenapi.PromotionRuleMatchScope(match.EffectiveScope())
	value.Scope = &scope
	return value
}

func conditionsFromAPI(value *adminopenapi.PromotionRuleConditions) promotionrules.Conditions {
	conditions := promotionrules.Conditions{}
	if value == nil {
		return conditions
	}
	if value.SecurityPolicy != nil {
		conditions.SecurityPolicy = string(*value.SecurityPolicy)
	}
	if value.Scan != nil {
		conditions.Scan = &promotionrules.Scan{Completed: value.Scan.Completed}
		if value.Scan.MaxSeverity != nil {
			conditions.Scan.MaxSeverity = string(*value.Scan.MaxSeverity)
		}
	}
	if value.MinAge != nil {
		conditions.MinAge = *value.MinAge
	}
	conditions.RequireSignature = value.RequireSignature != nil && *value.RequireSignature
	conditions.RequireSBOM = value.RequireSbom != nil && *value.RequireSbom
	conditions.RequireProvenance = value.RequireProvenance != nil && *value.RequireProvenance
	return conditions
}

func conditionsToAPI(conditions promotionrules.Conditions) adminopenapi.PromotionRuleConditions {
	value := adminopenapi.PromotionRuleConditions{}
	if conditions.SecurityPolicy != "" {
		policy := adminopenapi.PromotionRuleConditionsSecurityPolicy(conditions.SecurityPolicy)
		value.SecurityPolicy = &policy
	}
	if conditions.Scan != nil {
		value.Scan = &adminopenapi.PromotionRuleScanCondition{Completed: conditions.Scan.Completed}
		if conditions.Scan.MaxSeverity != "" {
			severity := adminopenapi.PromotionRuleScanConditionMaxSeverity(conditions.Scan.MaxSeverity)
			value.Scan.MaxSeverity = &severity
		}
	}
	if conditions.MinAge != "" {
		value.MinAge = &conditions.MinAge
	}
	if conditions.RequireSignature {
		value.RequireSignature = &conditions.RequireSignature
	}
	if conditions.RequireSBOM {
		value.RequireSbom = &conditions.RequireSBOM
	}
	if conditions.RequireProvenance {
		value.RequireProvenance = &conditions.RequireProvenance
	}
	return value
}

// promotionRuleResponse renders a stored rule; a stored document that no longer parses is a server error, never a
// partial answer.
func promotionRuleResponse(rule repository.PromotionRule) (adminopenapi.PromotionRule, error) {
	match, err := promotionrules.ParseMatch(rule.Match)
	if err != nil {
		return adminopenapi.PromotionRule{}, err
	}
	conditions, err := promotionrules.ParseConditions(rule.Conditions)
	if err != nil {
		return adminopenapi.PromotionRule{}, err
	}
	target := adminopenapi.PromotionRuleTarget{Kind: adminopenapi.PromotionRuleTargetKind(rule.TargetKind)}
	if rule.TargetRepositoryID != "" {
		id := uuid.MustParse(rule.TargetRepositoryID)
		target.RepositoryId = &id
	}
	if rule.RemoteInstanceID != "" {
		id := uuid.MustParse(rule.RemoteInstanceID)
		target.RemoteInstanceId = &id
		target.RemoteRepository = &rule.RemoteRepository
	}
	apiRule := adminopenapi.PromotionRule{
		Id: uuid.MustParse(rule.ID), Name: rule.Name, SourceRepositoryId: uuid.MustParse(rule.SourceRepositoryID), Target: target,
		Match: matchToAPI(match), Conditions: conditionsToAPI(conditions), Mode: adminopenapi.PromotionRuleMode("copy"),
		RunAsServiceAccountId: uuid.MustParse(rule.RunAsServiceAccountID), Enabled: rule.Enabled, State: adminopenapi.PromotionRuleState(rule.State), Version: rule.Version,
		CreatedBy: rule.CreatedBy, UpdatedBy: rule.UpdatedBy, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
	if rule.StateReason != "" {
		apiRule.StateReason = &rule.StateReason
	}
	return apiRule, nil
}

func decodePromotionRuleBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, promotionRuleBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "promotion rule payload is invalid")
		return false
	}
	return true
}

// ruleRequest is the part shared by a create and an update request.
type ruleRequest struct {
	name      string
	target    adminopenapi.PromotionRuleTarget
	match     adminopenapi.PromotionRuleMatch
	condition *adminopenapi.PromotionRuleConditions
	mode      *adminopenapi.PromotionRuleMode
	runAs     uuid.UUID
	enabled   bool
}

func (h promotionRuleHandler) writeRuleError(w http.ResponseWriter, err error) bool {
	var refusal *promotionrules.Error
	if !errors.As(err, &refusal) {
		return false
	}
	switch refusal.Code {
	case promotionrules.CodeInvalid:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", refusal.Message)
	default:
		writeHostedProblem(w, http.StatusUnprocessableEntity, refusal.Code, refusal.Message)
	}
	return true
}

// loadRepository reads a repository and answers 404 or 500 itself.
func (h promotionRuleHandler) loadRepository(w http.ResponseWriter, r *http.Request, id string) (repository.HostedRepository, bool) {
	repo, err := h.repositories.GetHostedRepository(r.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "repository not found")
		return repository.HostedRepository{}, false
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get repository failed")
		return repository.HostedRepository{}, false
	}
	return repo, true
}

// authorizeAdmin requires repository administration and answers the denial itself.
func (h promotionRuleHandler) authorizeAdmin(w http.ResponseWriter, r *http.Request, principal Principal, repo repository.HostedRepository) bool {
	if decision := h.authorizer.Authorize(r.Context(), principal, repo, RepositoryAdmin); !decision.Allowed {
		h.writeRepositoryDenial(w, r, principal, repo, RepositoryAdmin, decision)
		return false
	}
	return true
}

// buildRule validates a create or update request against the source and the caller and returns the rule to store, with
// ID, Name, SourceRepositoryID and the actor fields set. It answers every refusal itself.
func (h promotionRuleHandler) buildRule(w http.ResponseWriter, r *http.Request, principal Principal, source repository.HostedRepository, request ruleRequest) (repository.PromotionRule, bool) {
	name := strings.TrimSpace(request.name)
	if name == "" || utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "rule name must be between 1 and 128 characters without line breaks")
		return repository.PromotionRule{}, false
	}
	if request.mode != nil && string(*request.mode) != "copy" {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "mode must be copy")
		return repository.PromotionRule{}, false
	}
	if !h.authorizeAdmin(w, r, principal, source) {
		return repository.PromotionRule{}, false
	}
	if source.Type != repository.RepositoryTypeHosted || source.State != repository.RepositoryActive {
		writeHostedProblem(w, http.StatusConflict, "invalid_source", "source must be an active Hosted repository")
		return repository.PromotionRule{}, false
	}
	capabilities, supported := h.sourceCapabilities(source)
	if !supported {
		writeHostedProblem(w, http.StatusUnprocessableEntity, promotionrules.CodeUnsupportedFormat, "promotion rules are not supported for this repository format")
		return repository.PromotionRule{}, false
	}
	rule := repository.PromotionRule{Name: name, SourceRepositoryID: source.ID, RunAsServiceAccountID: request.runAs.String(), Enabled: request.enabled, CreatedBy: principal.Actor, UpdatedBy: principal.Actor}
	guarded := []repository.HostedRepository{source}
	switch request.target.Kind {
	case adminopenapi.PromotionRuleTargetKind(repository.PromotionTargetLocal):
		if request.target.RepositoryId == nil || request.target.RemoteInstanceId != nil || request.target.RemoteRepository != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "a local target names repositoryId and nothing else")
			return repository.PromotionRule{}, false
		}
		target, ok := h.loadRepository(w, r, request.target.RepositoryId.String())
		if !ok || !h.authorizeAdmin(w, r, principal, target) {
			return repository.PromotionRule{}, false
		}
		if target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
			writeHostedProblem(w, http.StatusConflict, "invalid_target", "target must be another active Hosted repository with the same format")
			return repository.PromotionRule{}, false
		}
		rule.TargetKind, rule.TargetRepositoryID = repository.PromotionTargetLocal, target.ID
		guarded = append(guarded, target)
	case adminopenapi.PromotionRuleTargetKind(repository.PromotionTargetRemote):
		if request.target.RemoteInstanceId == nil || request.target.RemoteRepository == nil || request.target.RepositoryId != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "a remote target names remoteInstanceId and remoteRepository and nothing else")
			return repository.PromotionRule{}, false
		}
		remoteRepository := strings.TrimSpace(*request.target.RemoteRepository)
		if remoteRepository == "" || utf8.RuneCountInString(remoteRepository) > 255 || strings.ContainsAny(remoteRepository, "\r\n\x00") {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "remoteRepository must be between 1 and 255 characters without line breaks")
			return repository.PromotionRule{}, false
		}
		// Remote instances are registered and visible to platform administrators only (ADR 0021, "Remote targets"), so
		// only one can aim a rule at them.
		if !principal.Admin {
			writeHostedProblem(w, http.StatusForbidden, "access_denied", "administrator permission is required for a remote target")
			return repository.PromotionRule{}, false
		}
		if _, err := h.remotes.GetRemoteInstance(r.Context(), request.target.RemoteInstanceId.String()); errors.Is(err, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "unknown_remote_instance", "remote instance not found")
			return repository.PromotionRule{}, false
		} else if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get remote instance failed")
			return repository.PromotionRule{}, false
		}
		rule.TargetKind, rule.RemoteInstanceID, rule.RemoteRepository = repository.PromotionTargetRemote, request.target.RemoteInstanceId.String(), remoteRepository
	default:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "target kind must be local or remote")
		return repository.PromotionRule{}, false
	}
	match := matchFromAPI(request.match)
	if err := match.Validate(capabilities); err != nil {
		if !h.writeRuleError(w, err) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		}
		return repository.PromotionRule{}, false
	}
	conditions := conditionsFromAPI(request.condition)
	if err := conditions.Validate(); err != nil {
		if !h.writeRuleError(w, err) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		}
		return repository.PromotionRule{}, false
	}
	var err error
	if rule.Match, err = match.Marshal(); err == nil {
		rule.Conditions, err = conditions.Marshal()
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "encode promotion rule failed")
		return repository.PromotionRule{}, false
	}
	if !h.checkRunAs(w, r, principal, request.runAs.String(), guarded) {
		return repository.PromotionRule{}, false
	}
	return rule, true
}

// checkRunAs verifies that the run-as service account exists and is active, then applies the confused-deputy guard: the
// rule must not let the service account do through it more than the caller could do directly. For every repository the
// rule touches and every operation, the caller must be allowed whenever the service account is. A platform
// administrator administers every service account and is exempt.
func (h promotionRuleHandler) checkRunAs(w http.ResponseWriter, r *http.Request, principal Principal, accountID string, repositories []repository.HostedRepository) bool {
	account, err := h.accounts.GetServiceAccount(r.Context(), accountID)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusUnprocessableEntity, "unknown_service_account", "run-as service account not found")
		return false
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get service account failed")
		return false
	}
	if account.State != repository.ServiceAccountActive {
		writeHostedProblem(w, http.StatusUnprocessableEntity, "service_account_disabled", "run-as service account is disabled")
		return false
	}
	if repo, operation, denied := h.confusedDeputy(r.Context(), principal, accountID, repositories); denied {
		h.recordAuthorizationDenial(r, principal, repo, operation, AuthorizationDecision{Source: "promotion_rule", Reason: "confused_deputy"})
		writeHostedProblem(w, http.StatusForbidden, "confused_deputy", "the run-as service account holds "+string(operation)+" permission on "+repo.Name+" that the caller does not hold")
		return false
	}
	return true
}

var promotionRuleOperations = []RepositoryOperation{RepositoryRead, RepositoryWrite, RepositoryAdmin, RepositoryIntelligence, RepositoryImportWrite}

// serviceAccountPrincipal is the principal a service account authenticates as: its actor is the durable
// service-account:<id> and it carries no role, so every permission comes from grants.
func serviceAccountPrincipal(accountID string) Principal {
	return Principal{Actor: durableactor.ServiceAccountActorPrefix + accountID, AuthenticationKind: authorization.AuthenticationServiceAccountCredential}
}

// confusedDeputy returns the first repository and operation the service account holds and the caller does not.
func (h promotionRuleHandler) confusedDeputy(ctx context.Context, caller Principal, accountID string, repositories []repository.HostedRepository) (repository.HostedRepository, RepositoryOperation, bool) {
	if caller.Admin {
		return repository.HostedRepository{}, "", false
	}
	account := serviceAccountPrincipal(accountID)
	for _, repo := range repositories {
		for _, operation := range promotionRuleOperations {
			if h.authorizer.Authorize(ctx, account, repo, operation).Allowed && !h.authorizer.Authorize(ctx, caller, repo, operation).Allowed {
				return repo, operation, true
			}
		}
	}
	return repository.HostedRepository{}, "", false
}

func (h promotionRuleHandler) newRuleID() string { return ids.New() }

func (h promotionRuleHandler) storeError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, repository.ErrPromotionRuleNameExists):
		writeHostedProblem(w, http.StatusConflict, "name_exists", "promotion rule name already exists")
	case errors.Is(err, repository.ErrPromotionRuleLimit):
		writeHostedProblem(w, http.StatusConflict, "rule_limit_reached", "the source repository already has "+strconv.Itoa(h.cap())+" promotion rules")
	case errors.Is(err, repository.ErrPromotionRuleReference):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "unknown_reference", "the rule names a repository, remote instance or service account that does not exist")
	case errors.Is(err, repository.ErrVersionConflict):
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match current promotion rule version")
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "promotion rule not found")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", action+" failed")
	}
}

func (h promotionRuleHandler) cap() int {
	if h.maxRules > 0 {
		return h.maxRules
	}
	return defaultPromotionMaxRules
}

func (h promotionRuleHandler) CreatePromotionRule(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var body adminopenapi.CreatePromotionRule
	if !decodePromotionRuleBody(w, r, &body) {
		return
	}
	source, ok := h.loadRepository(w, r, body.SourceRepositoryId.String())
	if !ok {
		return
	}
	enabled := body.Enabled == nil || *body.Enabled
	rule, ok := h.buildRule(w, r, principal, source, ruleRequest{name: body.Name, target: body.Target, match: body.Match, condition: body.Conditions, mode: body.Mode, runAs: body.RunAsServiceAccountId, enabled: enabled})
	if !ok {
		return
	}
	rule.ID = h.newRuleID()
	rule.Version = "1"
	created, err := h.rules.CreatePromotionRule(r.Context(), rule, h.cap(), h.auditRecords(principal, source, rule, "promotion_rule.create", http.StatusCreated)...)
	if err != nil {
		h.storeError(w, err, "create promotion rule")
		return
	}
	h.writeRule(w, http.StatusCreated, created)
}

func (h promotionRuleHandler) writeRule(w http.ResponseWriter, status int, rule repository.PromotionRule) {
	response, err := promotionRuleResponse(rule)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stored promotion rule is invalid")
		return
	}
	w.Header().Set("ETag", rule.Version)
	writeNativeMavenJSON(w, status, response)
}

// loadRule reads a rule and its source repository and requires administration of the source.
func (h promotionRuleHandler) loadRule(w http.ResponseWriter, r *http.Request, principal Principal, id adminopenapi.PromotionRuleId) (repository.PromotionRule, repository.HostedRepository, bool) {
	rule, err := h.rules.GetPromotionRule(r.Context(), id.String())
	if err != nil {
		h.storeError(w, err, "get promotion rule")
		return repository.PromotionRule{}, repository.HostedRepository{}, false
	}
	source, ok := h.loadRepository(w, r, rule.SourceRepositoryID)
	if !ok || !h.authorizeAdmin(w, r, principal, source) {
		return repository.PromotionRule{}, repository.HostedRepository{}, false
	}
	return rule, source, true
}

func (h promotionRuleHandler) GetPromotionRule(w http.ResponseWriter, r *http.Request, ruleID adminopenapi.PromotionRuleId) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	rule, _, ok := h.loadRule(w, r, principal, ruleID)
	if !ok {
		return
	}
	h.writeRule(w, http.StatusOK, rule)
}

func (h promotionRuleHandler) UpdatePromotionRule(w http.ResponseWriter, r *http.Request, ruleID adminopenapi.PromotionRuleId, params adminopenapi.UpdatePromotionRuleParams) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	current, source, ok := h.loadRule(w, r, principal, ruleID)
	if !ok {
		return
	}
	var body adminopenapi.UpdatePromotionRule
	if !decodePromotionRuleBody(w, r, &body) {
		return
	}
	next, ok := h.buildRule(w, r, principal, source, ruleRequest{name: body.Name, target: body.Target, match: body.Match, condition: body.Conditions, mode: body.Mode, runAs: body.RunAsServiceAccountId, enabled: body.Enabled})
	if !ok {
		return
	}
	next.ID, next.CreatedBy = current.ID, current.CreatedBy
	if expected, err := strconv.ParseInt(string(params.IfMatch), 10, 64); err == nil {
		next.Version = strconv.FormatInt(expected+1, 10)
	}
	updated, err := h.rules.UpdatePromotionRule(r.Context(), next, string(params.IfMatch), h.auditRecords(principal, source, next, "promotion_rule.update", http.StatusOK)...)
	if err != nil {
		h.storeError(w, err, "update promotion rule")
		return
	}
	h.writeRule(w, http.StatusOK, updated)
}

func (h promotionRuleHandler) DeletePromotionRule(w http.ResponseWriter, r *http.Request, ruleID adminopenapi.PromotionRuleId) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	rule, source, ok := h.loadRule(w, r, principal, ruleID)
	if !ok {
		return
	}
	if err := h.rules.DeletePromotionRule(r.Context(), rule.ID, h.auditRecords(principal, source, rule, "promotion_rule.delete", http.StatusNoContent)...); err != nil {
		h.storeError(w, err, "delete promotion rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h promotionRuleHandler) ListPromotionRules(w http.ResponseWriter, r *http.Request, params adminopenapi.ListPromotionRulesParams) {
	principal, ok := h.repositoryViewPrincipal(w, r)
	if !ok || !validListLimit(w, params.Limit, 500) {
		return
	}
	query := repository.PromotionRuleQuery{}
	if params.Limit != nil {
		query.Limit = *params.Limit
	}
	if params.PageToken != nil {
		query.PageToken = *params.PageToken
	}
	switch {
	case params.SourceRepositoryId != nil:
		source, found := h.loadRepository(w, r, params.SourceRepositoryId.String())
		if !found || !h.authorizeAdmin(w, r, principal, source) {
			return
		}
		query.SourceRepositoryIDs = []string{source.ID}
	case !principal.Admin:
		administered, err := h.administeredRepositoryIDs(r.Context(), principal)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list promotion rules failed")
			return
		}
		query.SourceRepositoryIDs = administered
	}
	page, err := h.rules.PagePromotionRules(r.Context(), query)
	if errors.Is(err, repository.ErrInvalidPageToken) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageToken is not valid for this listing")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list promotion rules failed")
		return
	}
	response := adminopenapi.PromotionRulePage{Items: make([]adminopenapi.PromotionRule, 0, len(page.Items)), NextPageToken: optionalString(page.NextPageToken)}
	for _, rule := range page.Items {
		item, convertErr := promotionRuleResponse(rule)
		if convertErr != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stored promotion rule is invalid")
			return
		}
		response.Items = append(response.Items, item)
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

// administeredRepositoryIDs lists the repositories the principal administers through a grant; each candidate is
// authorized again, so a grant that no longer decides drops out. An empty, non-nil slice means none.
func (h promotionRuleHandler) administeredRepositoryIDs(ctx context.Context, principal Principal) ([]string, error) {
	administered := []string{}
	if h.grantIndex == nil {
		return administered, nil
	}
	candidates, err := h.grantIndex.ListRepositoriesGrantedToPrincipals(ctx, principal.GrantPrincipals(), authorization.GrantScopeAdmin)
	if err != nil {
		return nil, err
	}
	for _, repo := range candidates {
		if h.authorizer.Authorize(ctx, principal, repo, RepositoryAdmin).Allowed {
			administered = append(administered, repo.ID)
		}
	}
	return administered, nil
}
