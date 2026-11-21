package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/aptpublication"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawformat "github.com/mlops-ca/notarx/internal/format/raw"
	"github.com/mlops-ca/notarx/internal/objectstore"
	conanprotocol "github.com/mlops-ca/notarx/internal/protocol/conan"
	mavenprotocol "github.com/mlops-ca/notarx/internal/protocol/maven"
	npmprotocol "github.com/mlops-ca/notarx/internal/protocol/npm"
	ociprotocol "github.com/mlops-ca/notarx/internal/protocol/oci"
	"github.com/mlops-ca/notarx/internal/repository"
)

// promotionModuleDeps are the ports the promotion capabilities of the nine core formats run on. Intelligence and Metrics are
// needed only to run the jobs of a format and Objects to run them or to read the manifests of an OCI release; a registry that
// only enqueues and matches leaves them empty.
type promotionModuleDeps struct {
	Store        GatewayStore
	Objects      objectstore.Store
	Intelligence repository.ArtifactIntelligenceStore
	Metrics      repository.BackgroundOperationMetrics
}

// promotionModule is the registry entry of one format other than Raw: its identity and its Promotion and
// PromotionPublisher capabilities, bound to the format's own enqueue and job runner. Formats that can do more
// (ReleaseSet, Tagged) embed it and add the capability.
type promotionModule struct {
	format  repository.Format
	enqueue func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error)
	run     func(ctx context.Context, limit int) error
}

func (m promotionModule) Format() repository.Format { return m.format }
func (m promotionModule) EnqueuePromotion(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	return m.enqueue(ctx, sourceID, targetID, coordinate, digest, key, audit...)
}
func (m promotionModule) RunPromotionJobs(ctx context.Context, limit int) error {
	return m.run(ctx, limit)
}

// mavenPromotionModule adds the Maven release: the assets of one GAV coordinate.
type mavenPromotionModule struct {
	promotionModule
	store repository.NativeMavenStore
}

// ReleaseMembers implements formatmodule.ReleaseSet.
func (m mavenPromotionModule) ReleaseMembers(ctx context.Context, repositoryID, coordinate, _ string) ([]formatmodule.ReleaseMember, error) {
	assets, err := m.store.ListMavenAssets(ctx, repositoryID, coordinate)
	if err != nil {
		return nil, err
	}
	members := make([]formatmodule.ReleaseMember, 0, len(assets))
	for _, asset := range assets {
		members = append(members, formatmodule.ReleaseMember{Coordinate: asset.Path, Digest: asset.Digest})
	}
	return members, nil
}

// npmPromotionModule adds the dist-tags that point at a version.
type npmPromotionModule struct {
	promotionModule
	store repository.NativeNPMStore
}

// TagName implements formatmodule.Tagged: the package of the version coordinate.
func (m npmPromotionModule) TagName(coordinate string) (string, bool) {
	name, _, ok := parseNPMVersionCoordinate(coordinate)
	return name, ok
}

// TagsOfName implements formatmodule.Tagged: one package read yields the dist-tags of every version.
func (m npmPromotionModule) TagsOfName(ctx context.Context, repositoryID, name string) (formatmodule.TagLookup, error) {
	pkg, err := m.store.GetNPMPackage(ctx, repositoryID, name)
	if err != nil {
		return nil, err
	}
	byVersion := map[string][]string{}
	for tag, version := range pkg.DistTags {
		byVersion[version] = append(byVersion[version], tag)
	}
	return func(coordinate, _ string) []string {
		if _, version, ok := parseNPMVersionCoordinate(coordinate); ok {
			return byVersion[version]
		}
		return nil
	}, nil
}

// ociPromotionModule adds the tags that point at a manifest digest.
type ociPromotionModule struct {
	promotionModule
	releases ociprotocol.ReleaseSet
	store    interface {
		ListOCITags(ctx context.Context, repositoryID, name string, limit int, after string) ([]string, error)
		GetOCIManifest(ctx context.Context, repositoryID, name, reference string) (repository.OCIManifest, error)
	}
}

const ociTagPageSize = 100

// ociImportModule is the registry entry of the OCI importer in the registry the import endpoints use. OCI is served by the
// native handler, not by a format module, so this entry carries the Importer capability and no routes.
type ociImportModule struct{ ociprotocol.NativeImporter }

func (ociImportModule) Format() repository.Format { return repository.FormatOCI }

// ReleaseMembers implements formatmodule.ReleaseSet: the manifest and, for an index, its child manifests.
func (m ociPromotionModule) ReleaseMembers(ctx context.Context, repositoryID, coordinate, digest string) ([]formatmodule.ReleaseMember, error) {
	return m.releases.ReleaseMembers(ctx, repositoryID, coordinate, digest)
}

// TagName implements formatmodule.Tagged: the coordinate of an image is its name.
func (m ociPromotionModule) TagName(coordinate string) (string, bool) {
	return coordinate, coordinate != ""
}

// TagsOfName implements formatmodule.Tagged: it resolves every tag of the image once and indexes them by manifest
// digest, so the tags of any digest of the image are a map read.
func (m ociPromotionModule) TagsOfName(ctx context.Context, repositoryID, name string) (formatmodule.TagLookup, error) {
	byDigest := map[string][]string{}
	after := ""
	for {
		page, err := m.store.ListOCITags(ctx, repositoryID, name, ociTagPageSize, after)
		if err != nil {
			return nil, err
		}
		for _, tag := range page {
			manifest, getErr := m.store.GetOCIManifest(ctx, repositoryID, name, tag)
			if errors.Is(getErr, repository.ErrNotFound) {
				continue
			}
			if getErr != nil {
				return nil, getErr
			}
			byDigest[manifest.Digest] = append(byDigest[manifest.Digest], tag)
		}
		if len(page) < ociTagPageSize {
			return func(_, digest string) []string { return byDigest[digest] }, nil
		}
		after = page[len(page)-1]
	}
}

// promotionFormatRegistry registers all nine core formats with their promotion capabilities (ADR 0021, Format
// capabilities and scope). Its identity is fixed in code, so a registration error is a programming bug.
func promotionFormatRegistry(deps promotionModuleDeps) *formatmodule.Registry {
	store := deps.Store
	rawModule := rawformat.New(rawformat.Options{Store: store, Jobs: store, Objects: deps.Objects, Intelligence: deps.Intelligence, BackgroundMetrics: deps.Metrics})
	ociPromotion := ociprotocol.NativePromotion{Store: store, Objects: deps.Objects, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	mavenPromotion := mavenprotocol.NativePromotion{Store: store, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	npmPromotion := npmprotocol.NativePromotion{Store: store, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	pypiPromotion := NativePyPIPromotion{Store: store, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	goPromotion := NativeGoPromotion{Store: store, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	cargoPromotion := NativeCargoPromotion{Store: store, Objects: deps.Objects, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	conanPromotion := conanprotocol.NativePromotion{Store: store, Intelligence: deps.Intelligence, Metrics: deps.Metrics}
	aptDistribution := aptpublication.Distribution{Store: store}
	registry, err := formatmodule.New(
		rawModule,
		ociPromotionModule{store: store, releases: ociprotocol.ReleaseSet{Store: store, Objects: deps.Objects}, promotionModule: promotionModule{format: repository.FormatOCI,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				return ociPromotion.Enqueue(ctx, targetID, key, ociprotocol.PromotionPayload{SourceRepositoryID: sourceID, Name: coordinate, Digest: digest}, audit...)
			}, run: ociPromotion.RunJobs}},
		mavenPromotionModule{store: store, promotionModule: promotionModule{format: repository.FormatMaven,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				promotionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("maven-promotion:"+targetID+":"+key)).String()
				return mavenPromotion.Enqueue(ctx, targetID, key, mavenprotocol.PromotionPayload{SourceRepositoryID: sourceID, Coordinate: coordinate, Digest: digest, PromotionID: promotionID}, audit...)
			}, run: mavenPromotion.RunJobs}},
		npmPromotionModule{store: store, promotionModule: promotionModule{format: repository.FormatNPM,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				packageName, version, _ := parseNPMVersionCoordinate(coordinate)
				return npmPromotion.Enqueue(ctx, targetID, key, npmprotocol.PromotionPayload{SourceRepositoryID: sourceID, PackageName: packageName, Version: version, Digest: digest}, audit...)
			}, run: npmPromotion.RunJobs}},
		promotionModule{format: repository.FormatPyPI,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				project, version, _ := parsePyPIVersionCoordinate(coordinate)
				return pypiPromotion.Enqueue(ctx, targetID, key, PyPIPromotionPayload{SourceRepositoryID: sourceID, Project: project, Version: version, Digest: digest}, audit...)
			}, run: pypiPromotion.RunJobs},
		promotionModule{format: repository.FormatGo,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				modulePath, version, _ := parseGoModuleVersionCoordinate(coordinate)
				return goPromotion.Enqueue(ctx, targetID, key, GoPromotionPayload{SourceRepositoryID: sourceID, Module: modulePath, Version: version, Digest: digest}, audit...)
			}, run: goPromotion.RunJobs},
		promotionModule{format: repository.FormatCargo,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				name, version, _ := splitVersionCoordinate(coordinate)
				return cargoPromotion.Enqueue(ctx, targetID, key, CargoPromotionPayload{SourceRepositoryID: sourceID, Name: name, Version: version, Digest: digest}, audit...)
			}, run: cargoPromotion.RunJobs},
		promotionModule{format: repository.FormatConan,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				reference, revision, _ := strings.Cut(coordinate, "#")
				return conanPromotion.Enqueue(ctx, targetID, key, conanprotocol.PromotionPayload{SourceRepositoryID: sourceID, Reference: reference, Revision: revision, Digest: digest}, audit...)
			}, run: conanPromotion.RunJobs},
		promotionModule{format: repository.FormatAPT,
			enqueue: func(ctx context.Context, sourceID, targetID, coordinate, digest, key string, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
				options := formatmodule.PromotionOptionsFrom(ctx)
				return aptDistribution.EnqueuePromotion(ctx, targetID, key, aptpublication.PromotionPayload{SourceRepositoryID: sourceID, Coordinate: coordinate, Digest: digest, TargetSuite: options.APTTargetSuite, Actor: options.Actor}, audit...)
			}, run: aptDistribution.RunPromotionJobs},
	)
	if err != nil {
		panic(errors.Join(errors.New("promotion format registry"), err))
	}
	return registry
}
