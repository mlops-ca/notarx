package repository

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

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
