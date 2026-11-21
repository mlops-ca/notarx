package raw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawmaintenance "github.com/mlops-ca/notarx/internal/maintenance/raw"
	rawprotocol "github.com/mlops-ca/notarx/internal/protocol/raw"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

func (*Module) Profile() repository.FormatProfile {
	profile, _ := repository.FormatProfileFor(repository.FormatRaw)
	return profile
}

func (m *Module) ResolveArtifactScan(ctx context.Context, repositoryID string, payload repository.ArtifactScanPayload) (scanning.Artifact, error) {
	if payload.Format != repository.FormatRaw {
		return scanning.Artifact{}, repository.ErrDisabled
	}
	if m.store == nil {
		return scanning.Artifact{}, errors.New("raw repository store is unavailable")
	}
	if m.objects == nil {
		return scanning.Artifact{}, errors.New("artifact object store is unavailable")
	}
	asset, err := m.store.GetRawAsset(ctx, repositoryID, payload.Coordinate)
	if err != nil {
		return scanning.Artifact{}, err
	}
	if asset.Digest != payload.Digest {
		return scanning.Artifact{}, errors.New("raw asset digest changed")
	}
	return scanning.Artifact{RepositoryID: repositoryID, Format: payload.Format, Coordinate: payload.Coordinate, Digest: payload.Digest, Assets: []scanning.Asset{{Path: asset.Path, Digest: asset.Digest, Size: asset.Size, MediaType: asset.ContentType, Open: func(ctx context.Context) (io.ReadCloser, error) {
		reader, _, err := m.objects.Open(ctx, asset.ObjectKey)
		return reader, err
	}}}}, nil
}
func (m *Module) Tombstone(ctx context.Context, repositoryID, coordinate string, audit ...repository.AuditRecord) error {
	if m.store == nil {
		return repository.ErrDisabled
	}
	return m.store.DeleteRawAsset(ctx, repositoryID, coordinate, audit...)
}
func (m *Module) Restore(ctx context.Context, repositoryID, coordinate string) error {
	if m.store == nil {
		return repository.ErrDisabled
	}
	_, err := m.store.RestoreRawAsset(ctx, repositoryID, coordinate)
	return err
}
func (m *Module) PlanRetention(ctx context.Context, repositoryID string, policy repository.RepositoryRetentionPolicy, now time.Time) ([]formatmodule.RetentionCandidate, error) {
	if m.store == nil {
		return nil, repository.ErrDisabled
	}
	coordinatePatterns, err := compilePatterns(policy.CoordinatePatterns)
	if err != nil {
		return nil, fmt.Errorf("compile retention coordinate patterns: %w", err)
	}
	protectedPatterns, err := compilePatterns(policy.ProtectedPatterns)
	if err != nil {
		return nil, fmt.Errorf("compile retention protected patterns: %w", err)
	}
	var candidates []formatmodule.RetentionCandidate
	after := ""
	for {
		assets, err := m.store.ListRawAssets(ctx, repositoryID, "", 200, after)
		if err != nil {
			return nil, err
		}
		for _, asset := range assets {
			if !matchesPatterns(coordinatePatterns, asset.Path, true) || matchesPatterns(protectedPatterns, asset.Path, false) {
				continue
			}
			createdAt := asset.UpdatedAt.UTC()
			if asset.UpdatedAt.IsZero() {
				createdAt = now
			}
			if !createdAt.Before(now.AddDate(0, 0, -policy.KeepDays)) {
				continue
			}
			age := int(now.Sub(createdAt).Hours() / 24)
			if age < 0 {
				age = 0
			}
			candidates = append(candidates, formatmodule.RetentionCandidate{Coordinate: asset.Path, Digest: asset.Digest, CreatedAt: createdAt, Reasons: []string{"age"}, AgeDays: age, VersionType: "asset", CursorID: asset.Path})
		}
		if len(assets) < 200 {
			return candidates, nil
		}
		after = assets[len(assets)-1].Path
	}
}
func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, expression)
	}
	return compiled, nil
}
func matchesPatterns(patterns []*regexp.Regexp, value string, empty bool) bool {
	if len(patterns) == 0 {
		return empty
	}
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
func (m *Module) EnqueuePromotion(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	if m.store == nil || m.jobs == nil {
		return repository.LifecycleJob{}, false, repository.ErrDisabled
	}
	return (rawprotocol.NativePromotion{Store: m.jobs}).Enqueue(ctx, targetID, key, rawprotocol.PromotionPayload{SourceRepositoryID: sourceID, Path: coordinate, Digest: digest}, audit...)
}
func (m *Module) RunPromotionJobs(ctx context.Context, limit int) error {
	if m.jobs == nil {
		return repository.ErrDisabled
	}
	return (rawprotocol.NativePromotion{Store: m.jobs, Intelligence: m.intelligence, Metrics: m.backgroundMetrics}).RunJobs(ctx, limit)
}

// ReleaseMembers implements formatmodule.ReleaseSet. A name@version coordinate of a versioned repository is the release of
// that version: every file of it (ADR 0028). Any other coordinate is one path and its release is that asset.
func (m *Module) ReleaseMembers(ctx context.Context, repositoryID, coordinate string) ([]formatmodule.ReleaseMember, error) {
	if m.store == nil {
		return nil, repository.ErrDisabled
	}
	if versions, ok := m.store.(repository.NativeRawVersionStore); ok {
		if at := strings.LastIndex(coordinate, "@"); at > 0 {
			_, files, err := versions.GetRawVersionWithFiles(ctx, repositoryID, coordinate[:at], coordinate[at+1:])
			if err == nil {
				members := make([]formatmodule.ReleaseMember, 0, len(files))
				for _, file := range files {
					members = append(members, formatmodule.ReleaseMember{Coordinate: file.Path, Digest: file.Digest})
				}
				return members, nil
			}
			if !errors.Is(err, repository.ErrNotFound) {
				return nil, err
			}
		}
	}
	asset, err := m.store.GetRawAsset(ctx, repositoryID, coordinate)
	if err != nil {
		return nil, err
	}
	return []formatmodule.ReleaseMember{{Coordinate: asset.Path, Digest: asset.Digest}}, nil
}
func (m *Module) ReplicationCheckpoints(ctx context.Context, sourceID, coordinate, digest string) ([]repository.ReplicationCheckpoint, error) {
	if m.store == nil {
		return nil, repository.ErrDisabled
	}
	asset, err := m.store.GetRawAsset(ctx, sourceID, coordinate)
	if errors.Is(err, repository.ErrNotFound) || asset.Digest != digest {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []repository.ReplicationCheckpoint{{ObjectKey: asset.ObjectKey, Digest: asset.Digest, Size: asset.Size}}, nil
}
func (m *Module) CollectCache(ctx context.Context) error {
	if m.proxyCache == nil {
		return nil
	}
	return m.proxyCache.CollectGarbage(ctx)
}

func (m *Module) collector() rawmaintenance.Collector {
	return rawmaintenance.Collector{Store: m.jobs, Objects: m.objects, Metrics: m.backgroundMetrics}
}
func (m *Module) StartScheduler(ctx context.Context, interval time.Duration) {
	m.collector().StartScheduler(ctx, interval)
}
func (m *Module) StartReclaimWorker(ctx context.Context, interval time.Duration) {
	m.collector().StartWorker(ctx, interval)
}
func (m *Module) StartPromotionWorker(ctx context.Context, interval time.Duration) {
	(rawprotocol.NativePromotion{Store: m.jobs, Intelligence: m.intelligence, Metrics: m.backgroundMetrics}).Start(ctx, interval)
}
func (m *Module) StartReplicationWorker(ctx context.Context, interval time.Duration) {
	(Replication{Store: m.replications, Source: m.objects, Destination: m.objects, Metrics: m.backgroundMetrics}).Start(ctx, interval)
}
