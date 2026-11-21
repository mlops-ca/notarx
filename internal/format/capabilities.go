package format

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

// These are independently queried capabilities, not a monolithic interface.
// They contain domain records only; HTTP DTOs stay at the management boundary.
type Profile interface {
	Profile() repository.FormatProfile
}
type Scanner interface {
	ResolveArtifactScan(context.Context, string, repository.ArtifactScanPayload) (scanning.Artifact, error)
}

type RetentionCandidate struct {
	Coordinate, Digest, CursorID, VersionType string
	CreatedAt                                 time.Time
	Reasons                                   []string
	AgeDays                                   int
}

// Retention plans and applies retention. Its Tombstone is the Governance method of the same
// module, which is why it accepts the audit records of a management tombstone; retention is a
// system action and passes none.
type Retention interface {
	PlanRetention(context.Context, string, repository.RepositoryRetentionPolicy, time.Time) ([]RetentionCandidate, error)
	Tombstone(context.Context, string, string, ...repository.AuditRecord) error
}
type Governance interface {
	ValidCoordinate(string) bool
	Tombstone(context.Context, string, string, ...repository.AuditRecord) error
	Restore(context.Context, string, string) error
}
type Promotion interface {
	EnqueuePromotion(context.Context, string, string, string, string, string, ...repository.AuditRecord) (repository.LifecycleJob, bool, error)
}

// PromotionPublisher extends Promotion with the format's own execution of a promotion job: it claims the format's
// queued promotion jobs and publishes each artifact into its target. A format that can enqueue a promotion but not
// publish it does not implement it.
type PromotionPublisher interface {
	Promotion
	RunPromotionJobs(context.Context, int) error
}

// PromotionOptions carries the request details only some formats need: the acting principal and, for APT, the suite
// the artifact is published into. A caller sets them with WithPromotionOptions before EnqueuePromotion.
type PromotionOptions struct {
	Actor          string
	APTTargetSuite string
}

type promotionOptionsKey struct{}

// WithPromotionOptions returns a context that carries options to EnqueuePromotion.
func WithPromotionOptions(ctx context.Context, options PromotionOptions) context.Context {
	return context.WithValue(ctx, promotionOptionsKey{}, options)
}

// PromotionOptionsFrom returns the options of ctx, or the zero value.
func PromotionOptionsFrom(ctx context.Context) PromotionOptions {
	options, _ := ctx.Value(promotionOptionsKey{}).(PromotionOptions)
	return options
}

// BlobDescriptor names one content-addressed object of a unit of publication.
type BlobDescriptor struct {
	Digest, ObjectKey string
	Size              int64
}

// ExportedUnit is one immutable publication unit: its manifest and the descriptors of the blobs it references.
type ExportedUnit struct {
	Format             repository.Format
	Coordinate, Digest string
	Manifest           []byte
	Blobs              []BlobDescriptor
}

// Exporter returns the publication unit of an artifact so another repository or instance can import it (ADR 0021,
// Format capabilities and scope).
type Exporter interface {
	ExportUnit(ctx context.Context, repositoryID, coordinate, digest string) (ExportedUnit, error)
}

// Importer publishes an exported unit into a repository, reading each blob through open. It must be idempotent for a
// digest already present and report a different digest at the same coordinate as a conflict.
type Importer interface {
	ImportUnit(ctx context.Context, repositoryID string, unit ExportedUnit, open func(BlobDescriptor) (io.ReadCloser, error)) error
}

// Tagged lists the mutable tags that point at an artifact (OCI tags, npm dist-tags).
type Tagged interface {
	Tags(ctx context.Context, repositoryID, coordinate, digest string) ([]string, error)
}

// ReleaseMember is one artifact of a release.
type ReleaseMember struct{ Coordinate, Digest string }

// ReleaseSet returns the members of the release the coordinate identifies; the format package decides what a release
// is, and nothing is inferred generically.
type ReleaseSet interface {
	ReleaseMembers(ctx context.Context, repositoryID, coordinate string) ([]ReleaseMember, error)
}
type Replication interface {
	ReplicationCheckpoints(context.Context, string, string, string) ([]repository.ReplicationCheckpoint, error)
}

type ArtifactSummary struct {
	Coordinate, Digest, ContentType string
	Size                            int64
	CreatedAt                       time.Time
}
type Browse interface {
	ValidBrowseParent(repository.ArtifactBrowseParent) bool
	BrowseName(string) string
	ValidPrefix(string) bool
	ValidKeyword(string, repository.ArtifactSearchMatch) bool
	SearchQuery(repository.ArtifactSearchQuery) repository.ArtifactSearchQuery
	Search(context.Context, string, repository.ArtifactSearchQuery, int, string) ([]ArtifactSummary, error)
}
type Usage interface{ UsageKey(string) string }
type CacheMaintenance interface{ CollectCache(context.Context) error }
type Background interface {
	StartScheduler(context.Context, time.Duration)
	StartReclaimWorker(context.Context, time.Duration)
	StartPromotionWorker(context.Context, time.Duration)
	StartReplicationWorker(context.Context, time.Duration)
}

// NativeAsset is a verified immutable publication unit. Migration-specific
// manifests, lease state and error envelopes stay in their owning domain.
type NativeAsset struct {
	RepositoryID, Path, ObjectKey, Digest string
	Size                                  int64
}
type ImmutablePublication interface {
	ValidatePublication(string, []NativeAsset) error
	LockPublication(context.Context, []NativeAsset) (context.Context, func(), error)
	StageVerifiedPublication(context.Context, NativeAsset, io.Reader) error
	PublishPublication(context.Context, NativeAsset, ...repository.AuditRecord) error
	ReadPublication(context.Context, string, string) (NativeAsset, error)
}

var ErrPublicationIdentity = errors.New("invalid native publication identity")
var ErrPublicationPath = errors.New("unsupported native publication path")
var ErrPublicationStage = errors.New("native publication staging failed")
var ErrPublicationUpload = errors.New("native publication upload failed")
