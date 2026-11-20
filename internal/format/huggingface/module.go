package huggingface

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/artifactquery"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Options configures the module. Store is required.
type Options struct {
	Store *Store
	// Objects holds the blobs; without it the garbage collection is a no-op.
	Objects objectstore.Store
	// GCPause is the pause between full garbage collection batches; zero selects 200 ms.
	GCPause time.Duration
	// Read configures the read protocol (resolve and HEAD under /hf/); nil leaves the module
	// without routes.
	Read *ReadOptions
}

// Module is the huggingface format module. It registers the format and its
// capabilities: Profile, Governance, Browse and Usage. The protocol routes are added
// by the read-path slices (ADR 0027).
type Module struct {
	store   *Store
	objects objectstore.Store
	gcPause time.Duration
	read    *reader
}

// New returns the module.
func New(options Options) (*Module, error) {
	if options.Store == nil || reflect.ValueOf(options.Store).IsNil() || options.Store.DB == nil {
		return nil, errors.New("huggingface module requires a store")
	}
	pause := options.GCPause
	if pause <= 0 {
		pause = 200 * time.Millisecond
	}
	module := &Module{store: options.Store, objects: options.Objects, gcPause: pause}
	if options.Read != nil {
		read, err := newReader(*options.Read, options.Store, options.Objects)
		if err != nil {
			return nil, err
		}
		module.read = read
	}
	return module, nil
}

// EnableRead turns on the read protocol (resolve and HEAD under /hf/). The composition root calls
// it once, before the routes are mounted, with the authentication and authorization ports of the
// gateway.
func (m *Module) EnableRead(options ReadOptions) error {
	read, err := newReader(options, m.store, m.objects)
	if err != nil {
		return err
	}
	m.read = read
	return nil
}

var (
	_ formatmodule.Module           = (*Module)(nil)
	_ formatmodule.Profile          = (*Module)(nil)
	_ formatmodule.Governance       = (*Module)(nil)
	_ formatmodule.Browse           = (*Module)(nil)
	_ formatmodule.Usage            = (*Module)(nil)
	_ formatmodule.CacheMaintenance = (*Module)(nil)
	_ formatmodule.NativeRoutes     = (*Module)(nil)
)

// Format identifies the module.
func (*Module) Format() repository.Format { return repository.FormatHuggingFace }

// Profile returns the capability profile of the format.
func (*Module) Profile() repository.FormatProfile {
	profile, _ := repository.FormatProfileFor(repository.FormatHuggingFace)
	return profile
}

// ValidCoordinate accepts only <type>/<org>/<name>@<sha40>: a commit is the only
// immutable identity, a branch, tag or abbreviated sha is a ref.
func (*Module) ValidCoordinate(value string) bool {
	_, ok := ParseCoordinate(value)
	return ok
}

// Tombstone is not available until hosted repositories exist (ADR 0027 phase two).
func (*Module) Tombstone(context.Context, string, string, ...repository.AuditRecord) error {
	return repository.ErrDisabled
}

// Restore is not available until hosted repositories exist (ADR 0027 phase two).
func (*Module) Restore(context.Context, string, string) error { return repository.ErrDisabled }

// ValidBrowseParent accepts a directory node: browsing descends through the repo id.
func (*Module) ValidBrowseParent(parent repository.ArtifactBrowseParent) bool {
	return parent.Kind == repository.BrowseNodeDirectory && parent.Path != "" && !strings.HasPrefix(parent.Path, "/")
}

// BrowseName returns the display name of a browse segment.
func (*Module) BrowseName(name string) string { return name }

// ValidPrefix reports whether value is a valid coordinate prefix.
func (*Module) ValidPrefix(value string) bool {
	return len(value) <= 512 && !strings.HasPrefix(value, "/") && !strings.ContainsRune(value, '\\') && strings.IndexFunc(value, artifactquery.InvalidRune) < 0
}

// ValidKeyword validates a search keyword.
func (m *Module) ValidKeyword(value string, match repository.ArtifactSearchMatch) bool {
	if match != repository.ArtifactSearchContains {
		return m.ValidPrefix(value)
	}
	return artifactquery.ValidKeyword(value, 512)
}

// SearchQuery returns the query unchanged: coordinates carry no escaping.
func (*Module) SearchQuery(query repository.ArtifactSearchQuery) repository.ArtifactSearchQuery {
	return query
}

// Search lists commits as artifacts, ordered by coordinate.
func (m *Module) Search(ctx context.Context, repositoryID string, query repository.ArtifactSearchQuery, limit int, after string) ([]formatmodule.ArtifactSummary, error) {
	prefix := ""
	if query.Mode == repository.ArtifactSearchByCoordinate {
		prefix = query.Value
	}
	commits, repos, err := m.store.SearchCommits(ctx, repositoryID, prefix, limit, after)
	if err != nil {
		return nil, err
	}
	items := make([]formatmodule.ArtifactSummary, 0, len(commits))
	for i, commit := range commits {
		summary := formatmodule.ArtifactSummary{
			Coordinate: Coordinate{RepoType: repos[i].RepoType, Name: repos[i].RepoName, Commit: commit.SHA}.String(),
			CreatedAt:  commit.CommittedAt,
		}
		if commit.TotalBytes != nil {
			summary.Size = *commit.TotalBytes
		}
		items = append(items, summary)
	}
	return items, nil
}

// UsageKey maps a usage resource to its statistics key: the coordinate itself.
func (*Module) UsageKey(resource string) string { return resource }
