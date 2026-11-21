package repository

import (
	"encoding/json"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// scanSummaryPayload is the severity summary of one scan result inside an artifact.scan.completed payload.
type scanSummaryPayload struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Critical  int       `json:"critical"`
	High      int       `json:"high"`
	Medium    int       `json:"medium"`
	Low       int       `json:"low"`
	Unknown   int       `json:"unknown"`
	ScannedAt time.Time `json:"scannedAt"`
}

func scanSummary(result ScanResult) scanSummaryPayload {
	return scanSummaryPayload{ID: result.ID, Status: result.Status, Critical: result.Critical, High: result.High, Medium: result.Medium,
		Low: result.Low, Unknown: result.Unknown, ScannedAt: result.ScannedAt.UTC()}
}

// scanCompletedEvent builds the artifact.scan.completed event of a persisted scan result (ADR 0021, ADR 0030).
// The payload is {previous, current, newFindings}: previous is null for the first scan of the digest and
// newFindings counts the distinct unsuppressed vulnerability ids of current that previous did not have.
// The version of the dedupe key is the scan result id, so a re-scan is a new event.
func scanCompletedEvent(result ScanResult, previous *ScanResult, format Format, newFindings int) WebhookEvent {
	payload := struct {
		RepositoryID string              `json:"repositoryId"`
		Format       Format              `json:"format"`
		Digest       string              `json:"digest"`
		Previous     *scanSummaryPayload `json:"previous"`
		Current      scanSummaryPayload  `json:"current"`
		NewFindings  int                 `json:"newFindings"`
	}{RepositoryID: result.RepositoryID, Format: format, Digest: result.ArtifactDigest, Current: scanSummary(result), NewFindings: newFindings}
	if previous != nil {
		summary := scanSummary(*previous)
		payload.Previous = &summary
	}
	data, _ := json.Marshal(payload)
	return WebhookEvent{ID: ids.New(), Type: WebhookEventArtifactScanCompleted, RepositoryID: result.RepositoryID, Format: string(format),
		Digest: result.ArtifactDigest, Version: result.ID, Data: data}
}
