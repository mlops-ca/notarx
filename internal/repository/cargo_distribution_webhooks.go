package repository

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

func cargoPromotionWebhookEvent(job LifecycleJob) (WebhookEvent, bool) {
	if job.Kind != LifecycleJobPromotion || !lifecycleJobMatchesFormats(job.Payload, []Format{FormatCargo}) ||
		(job.State != LifecycleJobCompleted && job.State != LifecycleJobFailed) {
		return WebhookEvent{}, false
	}
	var payload struct {
		SourceRepositoryID string `json:"sourceRepositoryId"`
		Name               string `json:"name"`
		Version            string `json:"version"`
		Digest             string `json:"digest"`
	}
	_ = json.Unmarshal(job.Payload, &payload)
	eventType := WebhookEventArtifactPromoted
	if job.State == LifecycleJobFailed {
		eventType = WebhookEventPromotionFailed
	}
	data, _ := json.Marshal(CargoDistributionWebhookData{
		OperationID: job.ID, Kind: "promotion", Format: FormatCargo,
		SourceRepositoryID: payload.SourceRepositoryID, TargetRepositoryID: job.RepositoryID,
		Coordinate: payload.Name + "@" + payload.Version, Digest: payload.Digest,
		State: string(job.State), Attempts: job.Attempts,
	})
	return WebhookEvent{ID: ids.New(), Type: eventType, RepositoryID: job.RepositoryID, Format: string(FormatCargo), Coordinate: payload.Name + "@" + payload.Version,
		Digest: payload.Digest, Version: job.ID, OccurredAt: job.CompletedAt, Data: data}, true
}

func cargoReplicationWebhookEvent(plan ReplicationPlan) (WebhookEvent, bool) {
	if plan.Format != FormatCargo || (plan.State != "completed" && (plan.State != "failed" || !plan.NextAttemptAt.IsZero())) {
		return WebhookEvent{}, false
	}
	eventType := WebhookEventReplicationCompleted
	if plan.State == "failed" {
		eventType = WebhookEventReplicationFailed
	}
	data, _ := json.Marshal(CargoDistributionWebhookData{
		OperationID: plan.ID, Kind: "replication", Format: FormatCargo,
		SourceRepositoryID: plan.SourceRepositoryID, TargetRepositoryID: plan.TargetRepositoryID,
		Coordinate: plan.Coordinate, Digest: plan.Digest, State: plan.State, Attempts: plan.Attempts,
	})
	occurred := plan.CompletedAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	return WebhookEvent{ID: ids.New(), Type: eventType, RepositoryID: plan.TargetRepositoryID, Format: string(FormatCargo), Coordinate: plan.Coordinate,
		Digest: plan.Digest, Version: plan.ID + ":" + strconv.Itoa(plan.Attempts), OccurredAt: occurred, Data: data}, true
}
