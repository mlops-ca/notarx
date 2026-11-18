package app

import (
	"net/http"
	"sort"
	"strings"
	"time"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	runtimeNodeStaleAfter   = 30 * time.Second
	runtimeNodeOfflineAfter = 2 * time.Minute
)

func runtimeNodeStatus(now, lastSeen time.Time) adminopenapi.RuntimeNodeStatus {
	age := now.Sub(lastSeen)
	if age < 0 || age < runtimeNodeStaleAfter {
		return adminopenapi.RuntimeNodeStatusOnline
	}
	if age <= runtimeNodeOfflineAfter {
		return adminopenapi.RuntimeNodeStatusStale
	}
	return adminopenapi.RuntimeNodeStatusOffline
}

func runtimeNodeHealth(items []adminopenapi.RuntimeNode) adminopenapi.RuntimeNodeHealth {
	health := adminopenapi.RuntimeNodeHealth{Issues: make([]adminopenapi.RuntimeNodeHealthIssue, 0)}
	activeInstanceIDs := make(map[string]int)
	hasAPI, hasScheduler, hasWorker := false, false, false
	for _, node := range items {
		switch node.Status {
		case adminopenapi.RuntimeNodeStatusOnline:
			health.Online++
		case adminopenapi.RuntimeNodeStatusStale:
			health.Stale++
		case adminopenapi.RuntimeNodeStatusOffline:
			health.Offline++
		}
		if node.Status == adminopenapi.RuntimeNodeStatusOffline {
			continue
		}
		activeInstanceIDs[node.InstanceId]++
		if node.Status != adminopenapi.RuntimeNodeStatusOnline {
			continue
		}
		for _, role := range node.Roles {
			switch role {
			case "standalone":
				hasAPI, hasScheduler, hasWorker = true, true, true
			case "api":
				hasAPI = true
			case "scheduler":
				hasScheduler = true
			case "worker":
				hasWorker = true
			}
		}
	}
	addIssue := func(code adminopenapi.RuntimeNodeHealthIssueCode, severity, message string) {
		issue := adminopenapi.RuntimeNodeHealthIssue{Code: code, Message: message}
		if severity == "error" {
			issue.Severity = adminopenapi.RuntimeNodeHealthIssueSeverityError
		} else {
			issue.Severity = adminopenapi.RuntimeNodeHealthIssueSeverityWarning
		}
		health.Issues = append(health.Issues, issue)
	}
	if !hasAPI {
		addIssue(adminopenapi.ApiUnavailable, "error", "No online API nodes")
	}
	if !hasScheduler {
		addIssue(adminopenapi.SchedulerUnavailable, "warning", "No online scheduler nodes; background scheduling will not run")
	}
	if !hasWorker {
		addIssue(adminopenapi.WorkerUnavailable, "warning", "No online worker nodes; background jobs will not be processed")
	}
	duplicateCount := 0
	for _, count := range activeInstanceIDs {
		if count > 1 {
			duplicateCount += count - 1
		}
	}
	if duplicateCount > 0 {
		addIssue(adminopenapi.DuplicateInstanceId, "warning", "Duplicate instance IDs exist; their sessions are recorded separately")
	}
	if health.Stale > 0 {
		addIssue(adminopenapi.StaleNodes, "warning", "Runtime nodes with stale heartbeats exist")
	}
	builds := make(map[string][]string)
	unknownBuild := make([]string, 0)
	for _, node := range items {
		if node.Status == adminopenapi.RuntimeNodeStatusOffline {
			continue
		}
		version, revision := "", ""
		if node.Version != nil {
			version = strings.TrimSpace(*node.Version)
		}
		if node.Revision != nil {
			revision = strings.TrimSpace(*node.Revision)
		}
		if version == "" || revision == "" || revision == "unknown" {
			unknownBuild = append(unknownBuild, node.SessionId)
			continue
		}
		builds[version+"\x00"+revision] = append(builds[version+"\x00"+revision], node.SessionId)
	}
	if len(unknownBuild) > 0 {
		sort.Strings(unknownBuild)
		affected := unknownBuild
		health.Issues = append(health.Issues, adminopenapi.RuntimeNodeHealthIssue{
			Code: adminopenapi.BuildIdentityUnknown, Severity: adminopenapi.RuntimeNodeHealthIssueSeverityWarning,
			Message: "Some online nodes did not report a complete build identity; version consistency cannot be confirmed", AffectedNodes: &affected,
		})
	}
	if len(builds) > 1 {
		affected := make([]string, 0)
		for _, sessions := range builds {
			affected = append(affected, sessions...)
		}
		sort.Strings(affected)
		health.Issues = append(health.Issues, adminopenapi.RuntimeNodeHealthIssue{
			Code: adminopenapi.MixedBuild, Severity: adminopenapi.RuntimeNodeHealthIssueSeverityWarning,
			Message: "Online nodes run different versions or revisions; a rolling upgrade may be in progress", AffectedNodes: &affected,
		})
	}
	health.Status = adminopenapi.RuntimeNodeHealthStatusHealthy
	for _, issue := range health.Issues {
		if issue.Severity == adminopenapi.RuntimeNodeHealthIssueSeverityError {
			health.Status = adminopenapi.RuntimeNodeHealthStatusCritical
			break
		}
		health.Status = adminopenapi.RuntimeNodeHealthStatusDegraded
	}
	return health
}

func (h generatedRepositoryAPIAdapter) ListRuntimeNodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	nodes, err := h.runtimeNodes.ListRuntimeNodes(r.Context())
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list runtime nodes failed")
		return
	}
	items := runtimeNodeResponses(nodes, time.Now().UTC())
	response := adminopenapi.RuntimeNodeList{Items: items, Health: runtimeNodeHealth(items), ReleaseSource: adminopenapi.RuntimeNodeListReleaseSourceNotConfigured}
	if sessionID := h.diagnostics.Runtime.SessionID; sessionID != "" {
		response.CurrentSessionId = &sessionID
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

func runtimeNodeResponses(nodes []repository.RuntimeNode, now time.Time) []adminopenapi.RuntimeNode {
	items := make([]adminopenapi.RuntimeNode, 0, len(nodes))
	for _, node := range nodes {
		workerFormats := make([]repository.Format, 0, len(node.WorkerFormats))
		for _, format := range node.WorkerFormats {
			workerFormats = append(workerFormats, repository.Format(format))
		}
		formats, previewFormats := runtimeWorkerFormatProjection(workerFormats)
		status := runtimeNodeStatus(now, node.LastSeenAt)
		if !node.StoppedAt.IsZero() {
			status = adminopenapi.RuntimeNodeStatusOffline
		}
		item := adminopenapi.RuntimeNode{
			InstanceId:           node.InstanceID,
			SessionId:            node.SessionID,
			Roles:                append([]string{}, node.Roles...),
			WorkerFormats:        formats,
			PreviewWorkerFormats: previewFormats,
			WorkerKinds:          append([]string{}, node.WorkerKinds...),
			StartedAt:            node.StartedAt,
			LastSeenAt:           node.LastSeenAt,
			Status:               status,
		}
		if node.BuildVersion != "" {
			version := node.BuildVersion
			item.Version = &version
		}
		if node.BuildRevision != "" {
			revision := node.BuildRevision
			item.Revision = &revision
		}
		if !node.StoppedAt.IsZero() {
			stoppedAt := node.StoppedAt
			item.StoppedAt = &stoppedAt
		}
		items = append(items, item)
	}
	return items
}
