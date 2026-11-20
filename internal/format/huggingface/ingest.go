package huggingface

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
)

// treeFlights shares one tree fetch per commit across every request of the process.
var treeFlights singleflight.Group

// treeFetchTimeout bounds one tree ingest (all pages).
const treeFetchTimeout = 5 * time.Minute

// Hub is the part of the upstream the metadata ingest needs; *Upstream implements it.
type Hub interface {
	Revision(ctx context.Context, repoType, repoName, rev string) (Revision, error)
	Tree(ctx context.Context, repoType, repoName, sha string, maxFiles int) ([]File, error)
}

// Resolution is the commit a revision resolved to.
type Resolution struct {
	Commit Commit
	// Stale is true when the commit comes from an expired ref that upstream could not be
	// asked about; the response carries X-Notarx-Stale: true.
	Stale bool
	// Cached is true when no upstream request was made.
	Cached bool
}

// Ingest resolves refs and captures commit metadata (ADR 0027). Rows are created lazily:
// a ref resolution creates the commit row, the file rows exist only after the tree of the
// commit was first requested and never on ref revalidation.
type Ingest struct {
	Store  *Store
	Hub    Hub
	Config Config
	// Now supplies the clock; nil uses time.Now.
	Now func() time.Time
}

func (i *Ingest) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

// ResolveRevision resolves rev (a branch, a tag or a full commit sha) of the repo to an
// immutable commit. A commit sha is never sent upstream. A fresh ref answers from the
// store; an expired or unknown one is revalidated, and when upstream is unreachable an
// expired ref is served stale if NOTARX_HF_SERVE_STALE allows it. A cache miss never
// fabricates metadata: with nothing cached the upstream error is returned.
func (i *Ingest) ResolveRevision(ctx context.Context, repo Repo, rev string) (Resolution, error) {
	if ValidCommit(rev) {
		commit, err := i.Store.GetCommit(ctx, repo.ID, rev)
		if err == nil {
			_ = i.Store.TouchCommit(ctx, commit.ID)
			return Resolution{Commit: commit, Cached: true}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Resolution{}, err
		}
		// An unknown commit is verified upstream before a row exists for it.
		revision, err := i.Hub.Revision(ctx, repo.RepoType, repo.RepoName, rev)
		if err != nil {
			return Resolution{}, err
		}
		if revision.SHA != rev {
			return Resolution{}, &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("upstream resolved a commit sha to another commit")}
		}
		commit, err = i.ensureCommit(ctx, repo, revision)
		return Resolution{Commit: commit}, err
	}
	if rev == "" || len([]rune(rev)) > 255 {
		return Resolution{}, ErrInvalid
	}
	now := i.now()
	ref, kind, err := i.lookupRef(ctx, repo, rev)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Resolution{}, err
	}
	known := err == nil
	if known && now.Before(ref.ExpiresAt) {
		commit, err := i.Store.GetCommit(ctx, repo.ID, ref.CommitSHA)
		if err == nil {
			_ = i.Store.TouchCommit(ctx, commit.ID)
			return Resolution{Commit: commit, Cached: true}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Resolution{}, err
		}
		// The commit row was collected while its ref stayed: revalidate and recreate it.
	}
	revision, err := i.Hub.Revision(ctx, repo.RepoType, repo.RepoName, rev)
	if err != nil {
		var upstream *UpstreamError
		if known && i.Config.ServeStale && errors.As(err, &upstream) && upstream.Code == CodeUpstreamUnavailable {
			if commit, getErr := i.Store.GetCommit(ctx, repo.ID, ref.CommitSHA); getErr == nil {
				_ = i.Store.TouchCommit(ctx, commit.ID)
				return Resolution{Commit: commit, Stale: true, Cached: true}, nil
			}
		}
		return Resolution{}, err
	}
	commit, err := i.ensureCommit(ctx, repo, revision)
	if err != nil {
		return Resolution{}, err
	}
	if kind == "" {
		kind = RefBranch
	}
	if err := i.Store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: kind, Name: rev, CommitSHA: revision.SHA, CheckedAt: now, ExpiresAt: now.Add(i.Config.RefTTL)}); err != nil {
		return Resolution{}, err
	}
	return Resolution{Commit: commit}, nil
}

// pinWindow is how long a ref resolution pins file reads: a GET that names a mutable ref uses the
// resolution a recent HEAD returned, however short NOTARX_HF_REF_TTL is, so the bytes of a file
// never come from another commit than the one its headers named.
const pinWindow = 15 * time.Minute

// ResolveForRead is ResolveRevision for a byte request. A mutable ref that was resolved within
// pinWindow answers without revalidation; only an unknown or older ref goes upstream.
func (i *Ingest) ResolveForRead(ctx context.Context, repo Repo, rev string) (Resolution, error) {
	if !ValidCommit(rev) && rev != "" {
		if ref, _, err := i.lookupRef(ctx, repo, rev); err == nil && i.now().Sub(ref.CheckedAt) < pinWindow {
			if commit, err := i.Store.GetCommit(ctx, repo.ID, ref.CommitSHA); err == nil {
				_ = i.Store.TouchCommit(ctx, commit.ID)
				return Resolution{Commit: commit, Cached: true}, nil
			}
		}
	}
	return i.ResolveRevision(ctx, repo, rev)
}

// lookupRef finds the stored ref for rev; a tag and a branch may share a name, the branch
// wins as it does on the Hub.
func (i *Ingest) lookupRef(ctx context.Context, repo Repo, rev string) (Ref, string, error) {
	for _, kind := range []string{RefBranch, RefTag} {
		ref, err := i.Store.GetRef(ctx, repo.ID, kind, rev)
		if err == nil {
			return ref, kind, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Ref{}, "", err
		}
	}
	return Ref{}, "", ErrNotFound
}

func (i *Ingest) ensureCommit(ctx context.Context, repo Repo, revision Revision) (Commit, error) {
	return i.Store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: revision.SHA, Source: SourceUpstream, CommittedAt: revision.LastModified})
}

// EnsureTree returns the commit with its file rows, fetching the tree from upstream the
// first time it is requested. A tree above MaxFilesPerCommit is refused with
// ErrTreeTooLarge and nothing is stored. Concurrent requests share one fetch.
func (i *Ingest) EnsureTree(ctx context.Context, repo Repo, commit Commit) (Commit, error) {
	if commit.TreeIngested() {
		return commit, nil
	}
	// The fetch is shared by every waiter, so it outlives the request that started it; the bound
	// keeps a stuck upstream from holding the slot forever.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeFetchTimeout)
	defer cancel()
	result, err, _ := treeFlights.Do(commit.ID, func() (any, error) {
		ctx := fetchCtx
		// Another node may have stored the tree since the caller read the commit.
		current, err := i.Store.GetCommit(ctx, repo.ID, commit.SHA)
		if err != nil {
			return Commit{}, err
		}
		if current.TreeIngested() {
			return current, nil
		}
		files, err := i.Hub.Tree(ctx, repo.RepoType, repo.RepoName, commit.SHA, i.Config.MaxFilesPerCommit)
		if err != nil {
			return Commit{}, err
		}
		if err := i.Store.ReplaceTree(ctx, current.ID, files); err != nil {
			// A concurrent ingest on another node wins on the primary key; its tree is the answer.
			if again, getErr := i.Store.GetCommit(ctx, repo.ID, commit.SHA); getErr == nil && again.TreeIngested() {
				return again, nil
			}
			if errors.Is(err, ErrPathTooLong) {
				return Commit{}, &UpstreamError{Code: CodePathTooLong, Err: err}
			}
			return Commit{}, err
		}
		return i.Store.GetCommit(ctx, repo.ID, commit.SHA)
	})
	if err != nil {
		return Commit{}, err
	}
	return result.(Commit), nil
}
