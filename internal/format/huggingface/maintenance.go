package huggingface

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// BlobKey is the object store key of a blob. Hugging Face blobs have their own namespace, so
// collecting a digest here can never delete an object another format references.
func BlobKey(sha256 string) string {
	if len(sha256) < 2 {
		return "huggingface/blobs/" + sha256
	}
	return "huggingface/blobs/" + sha256[:2] + "/" + sha256
}

const (
	// gcBatch is the digests removed per statement round of one repo.
	gcBatch = 100
	// gcRepoPage is the repos read per page of the sweep.
	gcRepoPage = 100
)

// CollectCache is the blob garbage collection of the format (ADR 0027): a digest is deleted
// only when no hf_files row of its repository references it, its bytes are released from the
// repository counter in the same transaction, and the object is removed from the object
// store only when no other repository holds the digest. Unreferenced commits are removed by
// the retention janitor (hf_commits entry), which is what leaves digests unreferenced.
func (m *Module) CollectCache(ctx context.Context) error {
	if m.objects == nil {
		return nil
	}
	var errs []error
	after := ""
	for {
		repos, err := m.store.ListRepos(ctx, after, gcRepoPage)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, repo := range repos {
			if err := m.collectRepo(ctx, repo); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				errs = append(errs, fmt.Errorf("repo %s: %w", repo.ID, err))
			}
		}
		if len(repos) < gcRepoPage {
			return errors.Join(errs...)
		}
		after = repos[len(repos)-1].ID
	}
}

func (m *Module) collectRepo(ctx context.Context, repo Repo) error {
	for {
		digests, err := m.store.UnreferencedBlobs(ctx, repo.ID, gcBatch)
		if err != nil {
			return err
		}
		removed := 0
		for _, digest := range digests {
			deleted, otherRepos, err := m.store.DeleteBlob(ctx, repo.RepositoryID, repo.ID, digest)
			if err != nil {
				return err
			}
			if !deleted {
				continue
			}
			removed++
			if otherRepos {
				continue
			}
			// A fetch in another repository may have stored this object after the transaction above;
			// the check narrows that window, and a cached file whose object is missing repairs itself.
			if held, err := m.store.BlobHeldAnywhere(ctx, digest); err != nil {
				return err
			} else if held {
				continue
			}
			if err := m.objects.Delete(ctx, BlobKey(digest)); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
				return err
			}
		}
		// A short or fruitless batch ends the repo; a full batch is followed by another after a
		// pause, so the sweep never competes with request traffic.
		if len(digests) < gcBatch || removed == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.gcPause):
		}
	}
}
