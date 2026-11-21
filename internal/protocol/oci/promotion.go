package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/ocigraph"
	"github.com/mlops-ca/notarx/internal/repository"
)

// NativePromotion copies immutable manifest bytes to a target-owned object and
// mounts referenced blobs. Keeping manifest keys target-owned prevents a source
// tombstone/reclaim cycle from deleting bytes still visible in the target.
type NativePromotion struct {
	Store interface {
		repository.NativeOCIStore
		repository.LifecycleJobStore
	}
	Objects      objectstore.Store
	Intelligence repository.ArtifactIntelligenceStore
	Metrics      repository.BackgroundOperationMetrics
}

type PromotionPayload struct {
	Format             repository.Format `json:"format"`
	SourceRepositoryID string            `json:"sourceRepositoryId"`
	Name               string            `json:"name"`
	Digest             string            `json:"digest"`
}

func (m NativePromotion) Enqueue(ctx context.Context, targetID, key string, payload PromotionPayload, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	payload.Format = repository.FormatOCI
	encoded, err := json.Marshal(payload)
	if err != nil {
		return repository.LifecycleJob{}, false, err
	}
	return m.Store.EnqueueLifecycleJob(ctx, repository.LifecycleJob{ID: ids.New(), RepositoryID: targetID, Kind: repository.LifecycleJobPromotion, IdempotencyKey: key, Payload: encoded}, audit...)
}

func (m NativePromotion) RunJobs(ctx context.Context, limit int) error {
	jobs, err := m.Store.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobPromotion, repository.FormatOCI, limit)
	if err != nil {
		return err
	}
	var firstErr error
	for _, job := range jobs {
		m.begin()
		if err := m.run(ctx, job); err != nil {
			m.end("failed")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		m.end("completed")
	}
	return firstErr
}
func (m NativePromotion) begin() {
	if m.Metrics != nil {
		m.Metrics.RecordBackgroundOperation("promotion", repository.FormatOCI, "started")
		m.Metrics.AddBackgroundOperationInFlight("promotion", repository.FormatOCI, 1)
	}
}
func (m NativePromotion) end(outcome string) {
	if m.Metrics != nil {
		m.Metrics.RecordBackgroundOperation("promotion", repository.FormatOCI, outcome)
		m.Metrics.AddBackgroundOperationInFlight("promotion", repository.FormatOCI, -1)
	}
}

func (m NativePromotion) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	background.Go(ctx, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.RunJobs(ctx, 100)
			}
		}
	})
}

func (m NativePromotion) run(ctx context.Context, job repository.LifecycleJob) error {
	var p PromotionPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Format != repository.FormatOCI || p.SourceRepositoryID == "" || p.Name == "" || !strings.HasPrefix(p.Digest, "sha256:") {
		return m.fail(ctx, job, "invalid OCI promotion payload")
	}
	if message := m.copyManifest(ctx, job.RepositoryID, p, 0); message != "" {
		return m.fail(ctx, job, message)
	}
	return m.Store.CompleteLifecycleJob(ctx, job.ID, job.LeaseToken)
}

// copyManifest publishes one manifest of the source into the target and returns the reason it cannot, or "". The child
// manifests of an index are published first, so an index is never visible without them whatever order the members of a
// release are promoted in; a child that was promoted already is published again without change.
func (m NativePromotion) copyManifest(ctx context.Context, targetID string, p PromotionPayload, depth int) string {
	if depth > ocigraph.MaxDepth {
		return "OCI index is nested too deeply"
	}
	source, err := m.Store.GetOCIManifest(ctx, p.SourceRepositoryID, p.Name, p.Digest)
	if err != nil {
		return "source OCI manifest is unavailable"
	}
	body, err := m.Objects.Get(ctx, source.ObjectKey)
	if err != nil {
		return "source OCI manifest object is unavailable"
	}
	document, _ := decodeManifestDocument(body)
	for _, child := range document.Manifests {
		if message := m.copyManifest(ctx, targetID, PromotionPayload{SourceRepositoryID: p.SourceRepositoryID, Name: p.Name, Digest: child.Digest}, depth+1); message != "" {
			return message
		}
	}
	for _, digest := range manifestBlobDigests(body) {
		if _, err = m.Store.MountOCIBlobFrom(ctx, targetID, p.SourceRepositoryID, digest); err != nil {
			return "source OCI blob is unavailable"
		}
	}
	key := ManifestObjectKey(targetID, p.Name, p.Digest)
	if err = m.Store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: targetID, ObjectKey: key, Digest: p.Digest, Size: int64(len(body))}); err != nil {
		return "stage target OCI manifest failed"
	}
	if err = m.Objects.PutVerifiedReader(ctx, key, bytes.NewReader(body), int64(len(body)), p.Digest); err != nil {
		return "persist target OCI manifest failed"
	}
	releaseAdmission, err := repository.LockArtifactDistributionAdmission(ctx, m.Store, p.SourceRepositoryID, repository.FormatOCI, p.Name, p.Digest)
	if errors.Is(err, repository.ErrArtifactQuarantined) {
		return repository.ArtifactQuarantinedReason
	}
	if err != nil {
		return "evaluate OCI artifact quarantine failed"
	}
	if _, err = m.Store.PutOCIManifest(ctx, repository.OCIManifest{RepositoryID: targetID, Name: p.Name, Digest: p.Digest, ObjectKey: key, MediaType: source.MediaType, SubjectDigest: source.SubjectDigest, ArtifactType: source.ArtifactType, Size: int64(len(body))}, p.Digest); err != nil {
		releaseAdmission()
		return "publish target OCI manifest failed"
	}
	releaseAdmission()
	intelligenceErr := repository.CopyArtifactIntelligenceOrEnqueue(ctx, m.Intelligence, m.Store, targetID, p.SourceRepositoryID, repository.FormatOCI, p.Name, p.Digest)
	if intelligenceErr != nil && !errors.Is(intelligenceErr, repository.ErrArtifactIntelligenceDeferred) {
		return fmt.Sprintf("copy OCI artifact intelligence failed: %v", intelligenceErr)
	}
	if errors.Is(intelligenceErr, repository.ErrArtifactIntelligenceDeferred) && m.Metrics != nil {
		m.Metrics.RecordBackgroundOperation("intelligence-copy", repository.FormatOCI, "deferred")
	}
	return ""
}

func (m NativePromotion) fail(ctx context.Context, job repository.LifecycleJob, message string) error {
	if err := m.Store.FailLifecycleJob(ctx, job.ID, job.LeaseToken, message); err != nil {
		return err
	}
	return fmt.Errorf("%s", message)
}

// manifestBlobDigests lists the blobs of a manifest, which a promotion mounts. The child manifests of an index are not blobs:
// they are promoted as artifacts of their own (the release set of an index names them).
func manifestBlobDigests(body []byte) []string {
	document, err := decodeManifestDocument(body)
	if err != nil {
		return nil
	}
	digests := []string{}
	for _, item := range blobDescriptors(document) {
		if item.Digest != "" {
			digests = append(digests, item.Digest)
		}
	}
	return digests
}
