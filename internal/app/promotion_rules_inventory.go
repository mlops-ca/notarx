package app

import (
	"context"
	"errors"
	"sort"

	"github.com/mlops-ca/notarx/internal/promotionfanout"
	"github.com/mlops-ca/notarx/internal/repository"
)

// dryRunInventory is the read side a dry run needs to list the promotable units of the formats whose search projection
// holds one row per package name rather than one per version.
type dryRunInventory interface {
	SearchNPMPackages(ctx context.Context, repositoryID, prefix string, limit int, after string, matches ...repository.ArtifactSearchMatch) ([]repository.NPMPackageSummary, error)
	ListNPMVersions(ctx context.Context, repositoryID, name string) ([]repository.NPMVersion, error)
	ListPyPIProjects(ctx context.Context, repositoryID, prefix string, limit int, after string, matches ...repository.ArtifactSearchMatch) ([]repository.PyPIProjectSummary, error)
	ListPyPIProjectFiles(ctx context.Context, repositoryID, project string) ([]repository.PyPIFile, error)
	ListGoModules(ctx context.Context, repositoryID, prefix string, limit int, after string) ([]string, error)
	ListGoModuleVersions(ctx context.Context, repositoryID, modulePath string) ([]repository.GoModuleVersion, error)
	GetGoModuleAsset(ctx context.Context, repositoryID, modulePath, version, kind string) (repository.GoModuleAsset, error)
	SearchCargoCrates(ctx context.Context, repositoryID, query string, limit int, after string, includeYanked bool, matches ...repository.ArtifactSearchMatch) ([]repository.CargoCrateSummary, int, error)
	ListCargoPublications(ctx context.Context, repositoryID, name string) ([]repository.CargoPublication, error)
	SearchConanReferences(ctx context.Context, repositoryID, prefix string, limit int, after string, matches ...repository.ArtifactSearchMatch) ([]repository.ConanReference, error)
	ListConanRecipeRevisions(ctx context.Context, repositoryID, reference string) ([]repository.ConanRecipeRevision, error)
	ListOCIManifestNames(ctx context.Context, repositoryID string, limit int, after string) ([]string, error)
	ListOCIManifests(ctx context.Context, repositoryID, name string, limit int, after string) ([]repository.OCIManifest, error)
}

// inventoryCandidates offers one candidate per promotable unit, addressed the way a promotion addresses it: npm, PyPI,
// Go and Cargo name@version with the digest of the version (npm tarball, Go module zip, Cargo crate, one PyPI file),
// Conan reference#revision with the recipe revision digest, OCI the image name with a manifest digest. The coordinate a
// glob or regex sees is therefore the one the promotion validators accept, so a rule matches what it would promote.
func (d *dryRun) inventoryCandidates(ctx context.Context, yield func(dryRunCandidate) (bool, error)) error {
	if d.ports.inventory == nil {
		return errDryRunUnavailable
	}
	id := d.source.ID
	stopped := false
	offer := func(candidate dryRunCandidate) error {
		more, err := yield(candidate)
		if err == nil && !more {
			stopped = true
		}
		return err
	}
	// pages walks the names of one listing page by page; each name is expanded by units until the examination stops.
	pages := func(list func(after string) ([]string, error), units func(name string) error) error {
		after := ""
		for !stopped {
			names, err := list(after)
			if err != nil {
				return err
			}
			for _, name := range names {
				if err = units(name); err != nil || stopped {
					return err
				}
				after = name
			}
			if len(names) < promotionDryRunPageSize {
				return nil
			}
		}
		return nil
	}
	switch d.source.Format {
	case repository.FormatNPM:
		return pages(func(after string) ([]string, error) {
			packages, err := d.ports.inventory.SearchNPMPackages(ctx, id, "", promotionDryRunPageSize, after)
			names := make([]string, len(packages))
			for i, pkg := range packages {
				names[i] = pkg.Name
			}
			return names, err
		}, func(name string) error {
			versions, err := d.ports.inventory.ListNPMVersions(ctx, id, name)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, version := range versions {
				if version.State == "deleted" || version.Digest == "" {
					continue
				}
				if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, name+"@"+version.Version), Digest: version.Digest, CreatedAt: version.CreatedAt}); err != nil || stopped {
					return err
				}
			}
			return nil
		})
	case repository.FormatPyPI:
		return pages(func(after string) ([]string, error) {
			projects, err := d.ports.inventory.ListPyPIProjects(ctx, id, "", promotionDryRunPageSize, after)
			names := make([]string, len(projects))
			for i, project := range projects {
				names[i] = project.Project
			}
			return names, err
		}, func(project string) error {
			files, err := d.ports.inventory.ListPyPIProjectFiles(ctx, id, project)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			// A version is promoted as a whole and named by the digest of one of its files; the first by filename is
			// the stable choice.
			sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
			seen := map[string]bool{}
			for _, file := range files {
				if seen[file.Version] || file.Digest == "" {
					continue
				}
				seen[file.Version] = true
				if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, project+"@"+file.Version), Digest: file.Digest, CreatedAt: file.CreatedAt}); err != nil || stopped {
					return err
				}
			}
			return nil
		})
	case repository.FormatGo:
		return pages(func(after string) ([]string, error) {
			return d.ports.inventory.ListGoModules(ctx, id, "", promotionDryRunPageSize, after)
		}, func(module string) error {
			versions, err := d.ports.inventory.ListGoModuleVersions(ctx, id, module)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, version := range versions {
				zip, assetErr := d.ports.inventory.GetGoModuleAsset(ctx, id, module, version.Version, "zip")
				if errors.Is(assetErr, repository.ErrNotFound) || (assetErr == nil && zip.Digest == "") {
					continue
				}
				if assetErr != nil {
					return assetErr
				}
				if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, module+"@"+version.Version), Digest: zip.Digest, CreatedAt: version.CreatedAt}); err != nil || stopped {
					return err
				}
			}
			return nil
		})
	case repository.FormatCargo:
		return pages(func(after string) ([]string, error) {
			crates, _, err := d.ports.inventory.SearchCargoCrates(ctx, id, "", promotionDryRunPageSize, after, true)
			names := make([]string, len(crates))
			for i, crate := range crates {
				names[i] = crate.Name
			}
			return names, err
		}, func(name string) error {
			publications, err := d.ports.inventory.ListCargoPublications(ctx, id, name)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, publication := range publications {
				if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, name+"@"+publication.Version), Digest: publication.Digest, CreatedAt: publication.PublishedAt}); err != nil || stopped {
					return err
				}
			}
			return nil
		})
	case repository.FormatConan:
		return pages(func(after string) ([]string, error) {
			references, err := d.ports.inventory.SearchConanReferences(ctx, id, "", promotionDryRunPageSize, after)
			names := make([]string, len(references))
			for i, reference := range references {
				names[i] = reference.Reference
			}
			return names, err
		}, func(reference string) error {
			revisions, err := d.ports.inventory.ListConanRecipeRevisions(ctx, id, reference)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, revision := range revisions {
				if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, reference+"#"+revision.Revision), Digest: revision.Digest, CreatedAt: revision.CreatedAt}); err != nil || stopped {
					return err
				}
			}
			return nil
		})
	case repository.FormatOCI:
		return pages(func(after string) ([]string, error) {
			return d.ports.inventory.ListOCIManifestNames(ctx, id, promotionDryRunPageSize, after)
		}, func(name string) error {
			after := ""
			for {
				manifests, err := d.ports.inventory.ListOCIManifests(ctx, id, name, promotionDryRunPageSize, after)
				if err != nil {
					return err
				}
				for _, manifest := range manifests {
					if err = offer(dryRunCandidate{Candidate: promotionfanout.CandidateOf(d.source.Format, name), Digest: manifest.Digest, CreatedAt: manifest.CreatedAt}); err != nil || stopped {
						return err
					}
					after = manifest.Digest
				}
				if len(manifests) < promotionDryRunPageSize {
					return nil
				}
			}
		})
	}
	return errDryRunUnavailable
}
