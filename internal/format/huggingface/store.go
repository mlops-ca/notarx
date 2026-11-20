package huggingface

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/tasklease"
)

// Dialect selects the SQL variant of the few statements that differ (upserts, placeholders).
type Dialect = tasklease.Dialect

const (
	// Postgres is PostgreSQL, the default store.
	Postgres = tasklease.Postgres
	// MySQL is MySQL 8.4 (ADR 0022).
	MySQL = tasklease.MySQL
)

// Commit sources (hf_commits.source).
const (
	SourceUpstream = "upstream"
	SourceHosted   = "hosted"
	SourcePromoted = "promoted"
)

// Ref kinds (hf_refs.ref_kind).
const (
	RefBranch = "branch"
	RefTag    = "tag"
)

// Errors of the store.
var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = repository.ErrNotFound
	// ErrPathTooLong is returned for a file path above MaxPathLength.
	ErrPathTooLong = errors.New("hugging face file path is too long")
	// ErrInvalid is returned for a value that violates the coordinate rules.
	ErrInvalid = errors.New("invalid hugging face value")
)

// insertBatch is the rows per INSERT of a tree; 500 rows of eight columns stay far below
// the 65535 parameter limit of both engines.
const insertBatch = 500

// Store persists the hf_* tables (ADR 0027). Statements are written once with ? as the
// placeholder and rebound for PostgreSQL; the three upserts are the only dialect branches.
type Store struct {
	DB      *sql.DB
	Dialect Dialect
	// Now supplies the clock; nil uses time.Now.
	Now func() time.Time
}

// NewStore returns a store over db.
func NewStore(db *sql.DB, dialect Dialect) *Store { return &Store{DB: db, Dialect: dialect} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// rebind converts ? placeholders to $n for PostgreSQL.
func (s *Store) rebind(query string) string {
	if s.Dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) exec(ctx context.Context, q queryer, query string, args ...any) (sql.Result, error) {
	return q.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) row(ctx context.Context, q queryer, query string, args ...any) *sql.Row {
	return q.QueryRowContext(ctx, s.rebind(query), args...)
}

// Repo is one Hugging Face repo id of a notarx repository.
type Repo struct {
	ID, RepositoryID, RepoType, RepoName string
	CreatedAt                            time.Time
}

// Commit is an immutable commit. FileCount and TotalBytes are nil until the tree has been
// ingested, which tells a commit with an empty tree from one not yet read.
type Commit struct {
	ID, HFRepoID, SHA, ParentSHA, Message, Author string
	CommittedAt                                   time.Time
	Source                                        string
	FileCount                                     *int
	TotalBytes                                    *int64
	LastReadAt                                    time.Time
}

// TreeIngested reports whether the file rows of the commit exist.
func (c Commit) TreeIngested() bool { return c.FileCount != nil }

// File is one file of a commit. SHA256 is empty for a regular file whose bytes have not
// been read: the Hub tree names it by its git blob id only.
type File struct {
	CommitID, Path string
	Size           int64
	SHA256         string
	LFS            bool
	GitBlobID      string
	PickleFlag     bool
	Cached         bool
}

// Ref is a mutable ref and its last upstream check.
type Ref struct {
	HFRepoID, Kind, Name, CommitSHA string
	CheckedAt, ExpiresAt            time.Time
}

const repoColumns = "id, repository_id, repo_type, repo_name, created_at"

func scanRepo(r interface{ Scan(...any) error }) (Repo, error) {
	var repo Repo
	err := r.Scan(&repo.ID, &repo.RepositoryID, &repo.RepoType, &repo.RepoName, &repo.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return repo, err
}

// GetRepo returns the repo row of (repositoryID, repoType, repoName).
func (s *Store) GetRepo(ctx context.Context, repositoryID, repoType, repoName string) (Repo, error) {
	return scanRepo(s.row(ctx, s.DB, "SELECT "+repoColumns+" FROM hf_repos WHERE repository_id = ? AND repo_type = ? AND repo_name = ?", repositoryID, repoType, repoName))
}

// EnsureRepo returns the repo row, creating it on first use.
func (s *Store) EnsureRepo(ctx context.Context, repositoryID, repoType, repoName string) (Repo, error) {
	if !ValidRepoType(repoType) || !ValidRepoName(repoName) || !ids.Valid(repositoryID) {
		return Repo{}, ErrInvalid
	}
	if repo, err := s.GetRepo(ctx, repositoryID, repoType, repoName); !errors.Is(err, ErrNotFound) {
		return repo, err
	}
	_, err := s.exec(ctx, s.DB, "INSERT INTO hf_repos (id, repository_id, repo_type, repo_name, created_at) VALUES (?, ?, ?, ?, ?)", ids.New(), repositoryID, repoType, repoName, s.now())
	if err != nil {
		// A concurrent creator wins on the unique key; its row is the answer.
		if repo, getErr := s.GetRepo(ctx, repositoryID, repoType, repoName); getErr == nil {
			return repo, nil
		}
		return Repo{}, err
	}
	return s.GetRepo(ctx, repositoryID, repoType, repoName)
}

const commitColumns = "id, hf_repo_id, commit_sha, parent_sha, message, author, committed_at, source, file_count, total_bytes, last_read_at"

func scanCommit(r interface{ Scan(...any) error }) (Commit, error) {
	var (
		c                       Commit
		parent, message, author sql.NullString
		fileCount, totalBytes   sql.NullInt64
	)
	err := r.Scan(&c.ID, &c.HFRepoID, &c.SHA, &parent, &message, &author, &c.CommittedAt, &c.Source, &fileCount, &totalBytes, &c.LastReadAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Commit{}, ErrNotFound
	}
	if err != nil {
		return Commit{}, err
	}
	c.ParentSHA, c.Message, c.Author = parent.String, message.String, author.String
	if fileCount.Valid {
		n := int(fileCount.Int64)
		c.FileCount = &n
	}
	if totalBytes.Valid {
		c.TotalBytes = &totalBytes.Int64
	}
	return c, nil
}

// GetCommit returns the commit with the full sha in the repo.
func (s *Store) GetCommit(ctx context.Context, hfRepoID, sha string) (Commit, error) {
	return scanCommit(s.row(ctx, s.DB, "SELECT "+commitColumns+" FROM hf_commits WHERE hf_repo_id = ? AND commit_sha = ?", hfRepoID, sha))
}

// EnsureCommit returns the commit row, creating it from in on first sight. A commit is
// immutable, so an existing row is returned unchanged; its tree is set by ReplaceTree.
func (s *Store) EnsureCommit(ctx context.Context, in Commit) (Commit, error) {
	if !ValidCommit(in.SHA) || (in.ParentSHA != "" && !ValidCommit(in.ParentSHA)) || in.HFRepoID == "" {
		return Commit{}, ErrInvalid
	}
	switch in.Source {
	case SourceUpstream, SourceHosted, SourcePromoted:
	default:
		return Commit{}, ErrInvalid
	}
	if existing, err := s.GetCommit(ctx, in.HFRepoID, in.SHA); !errors.Is(err, ErrNotFound) {
		return existing, err
	}
	now := s.now()
	if in.CommittedAt.IsZero() {
		in.CommittedAt = now
	}
	_, err := s.exec(ctx, s.DB, "INSERT INTO hf_commits (id, hf_repo_id, commit_sha, parent_sha, message, author, committed_at, source, last_read_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		ids.New(), in.HFRepoID, in.SHA, nullString(in.ParentSHA), nullString(in.Message), nullString(in.Author), in.CommittedAt.UTC(), in.Source, now)
	if err != nil {
		if existing, getErr := s.GetCommit(ctx, in.HFRepoID, in.SHA); getErr == nil {
			return existing, nil
		}
		return Commit{}, err
	}
	return s.GetCommit(ctx, in.HFRepoID, in.SHA)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TouchCommit records a read of the commit; retention measures age on last_read_at.
func (s *Store) TouchCommit(ctx context.Context, commitID string) error {
	_, err := s.exec(ctx, s.DB, "UPDATE hf_commits SET last_read_at = ? WHERE id = ?", s.now(), commitID)
	return err
}

// ReplaceTree stores the complete file list of a commit and sets its file_count and
// total_bytes in one transaction. It is the only writer of an upstream tree.
func (s *Store) ReplaceTree(ctx context.Context, commitID string, files []File) error {
	var total int64
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		if len([]rune(f.Path)) > MaxPathLength {
			return ErrPathTooLong
		}
		if !ValidPath(f.Path) || f.Size < 0 || (f.SHA256 != "" && !ValidDigest(f.SHA256)) || (f.GitBlobID != "" && !ValidCommit(f.GitBlobID)) {
			return ErrInvalid
		}
		if _, dup := seen[f.Path]; dup {
			return ErrInvalid
		}
		seen[f.Path] = struct{}{}
		total += f.Size
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.exec(ctx, tx, "DELETE FROM hf_files WHERE commit_id = ?", commitID); err != nil {
		return err
	}
	for start := 0; start < len(files); start += insertBatch {
		end := min(start+insertBatch, len(files))
		var b strings.Builder
		b.WriteString("INSERT INTO hf_files (commit_id, path, size, sha256, lfs, git_blob_id, pickle_flag, cached) VALUES ")
		args := make([]any, 0, (end-start)*8)
		for i, f := range files[start:end] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("(?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, commitID, f.Path, f.Size, nullString(f.SHA256), f.LFS, nullString(f.GitBlobID), f.PickleFlag, f.Cached)
		}
		if _, err := s.exec(ctx, tx, b.String(), args...); err != nil {
			return err
		}
	}
	result, err := s.exec(ctx, tx, "UPDATE hf_commits SET file_count = ?, total_bytes = ? WHERE id = ?", len(files), total, commitID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// A digest the repository already holds (the same LFS file in an earlier commit) is cached
	// already: the new commit needs neither a fetch nor a second reservation.
	if _, err := s.exec(ctx, tx, "UPDATE hf_files SET cached = ? WHERE commit_id = ? AND sha256 IN (SELECT b.sha256 FROM hf_blobs b JOIN hf_commits c ON c.hf_repo_id = b.hf_repo_id WHERE c.id = ?)", true, commitID, commitID); err != nil {
		return err
	}
	return tx.Commit()
}

const fileColumns = "commit_id, path, size, sha256, lfs, git_blob_id, pickle_flag, cached"

func scanFile(r interface{ Scan(...any) error }) (File, error) {
	var f File
	var sha, blob sql.NullString
	err := r.Scan(&f.CommitID, &f.Path, &f.Size, &sha, &f.LFS, &blob, &f.PickleFlag, &f.Cached)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	f.SHA256, f.GitBlobID = sha.String, blob.String
	return f, err
}

// GetFile returns one file of a commit.
func (s *Store) GetFile(ctx context.Context, commitID, path string) (File, error) {
	return scanFile(s.row(ctx, s.DB, "SELECT "+fileColumns+" FROM hf_files WHERE commit_id = ? AND path = ?", commitID, path))
}

// ListFiles returns up to limit files of a commit whose path sorts after the cursor, in
// primary key order (keyset pagination, no extra index).
func (s *Store) ListFiles(ctx context.Context, commitID, after string, limit int) ([]File, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, s.rebind("SELECT "+fileColumns+" FROM hf_files WHERE commit_id = ? AND path > ? ORDER BY path LIMIT ?"), commitID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetRef returns a ref of the repo, or ErrNotFound.
func (s *Store) GetRef(ctx context.Context, hfRepoID, kind, name string) (Ref, error) {
	var r Ref
	err := s.row(ctx, s.DB, "SELECT hf_repo_id, ref_kind, ref_name, commit_sha, checked_at, expires_at FROM hf_refs WHERE hf_repo_id = ? AND ref_kind = ? AND ref_name = ?", hfRepoID, kind, name).
		Scan(&r.HFRepoID, &r.Kind, &r.Name, &r.CommitSHA, &r.CheckedAt, &r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Ref{}, ErrNotFound
	}
	return r, err
}

// PutRef records the commit a ref resolved to and when the answer expires.
func (s *Store) PutRef(ctx context.Context, ref Ref) error {
	if (ref.Kind != RefBranch && ref.Kind != RefTag) || ref.Name == "" || len([]rune(ref.Name)) > 255 || !ValidCommit(ref.CommitSHA) {
		return ErrInvalid
	}
	query := "INSERT INTO hf_refs (hf_repo_id, ref_kind, ref_name, commit_sha, checked_at, expires_at) VALUES (?, ?, ?, ?, ?, ?) "
	if s.Dialect == MySQL {
		query += "ON DUPLICATE KEY UPDATE commit_sha = VALUES(commit_sha), checked_at = VALUES(checked_at), expires_at = VALUES(expires_at)"
	} else {
		query += "ON CONFLICT (hf_repo_id, ref_kind, ref_name) DO UPDATE SET commit_sha = EXCLUDED.commit_sha, checked_at = EXCLUDED.checked_at, expires_at = EXCLUDED.expires_at"
	}
	_, err := s.exec(ctx, s.DB, query, ref.HFRepoID, ref.Kind, ref.Name, ref.CommitSHA, ref.CheckedAt.UTC(), ref.ExpiresAt.UTC())
	return err
}

// HasBlob reports whether the repo already holds the digest.
func (s *Store) HasBlob(ctx context.Context, hfRepoID, sha256 string) (bool, error) {
	var one int
	err := s.row(ctx, s.DB, "SELECT 1 FROM hf_blobs WHERE hf_repo_id = ? AND sha256 = ?", hfRepoID, sha256).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CommitBlob records that the bytes of digest sha256 are in the object store: it inserts
// the hf_blobs row, marks every file of the repo with that digest as cached and sets the
// digest of file (commitID, path), then reserves the bytes with capacity.Reserve as the
// last statement before the commit (ADR 0023 section 3), so a failed or over-quota
// reservation rolls everything back and holds nothing. The digest is counted once per
// repository: a row that already exists reserves nothing.
//
// capacity.Reserve is PostgreSQL SQL today; on MySQL it fails until the counters are
// ported.
func (s *Store) CommitBlob(ctx context.Context, repositoryID, hfRepoID, commitID, path, sha256 string, size int64) error {
	if !ValidDigest(sha256) || size < 0 {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	insert := "INSERT INTO hf_blobs (hf_repo_id, sha256, size, created_at) VALUES (?, ?, ?, ?) "
	if s.Dialect == MySQL {
		insert += "ON DUPLICATE KEY UPDATE hf_repo_id = hf_repo_id"
	} else {
		insert += "ON CONFLICT (hf_repo_id, sha256) DO NOTHING"
	}
	result, err := s.exec(ctx, tx, insert, hfRepoID, sha256, size, s.now())
	if err != nil {
		return err
	}
	inserted, _ := result.RowsAffected()
	if _, err := s.exec(ctx, tx, "UPDATE hf_files SET sha256 = ?, cached = ? WHERE commit_id = ? AND path = ?", sha256, true, commitID, path); err != nil {
		return err
	}
	if _, err := s.exec(ctx, tx, "UPDATE hf_files SET cached = ? WHERE sha256 = ? AND commit_id IN (SELECT id FROM hf_commits WHERE hf_repo_id = ?)", true, sha256, hfRepoID); err != nil {
		return err
	}
	if inserted > 0 {
		if err := capacity.Reserve(ctx, tx, repositoryID, size, 1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BlobHeldAnywhere reports whether any repository holds the digest.
func (s *Store) BlobHeldAnywhere(ctx context.Context, sha256 string) (bool, error) {
	var one int
	err := s.row(ctx, s.DB, "SELECT 1 FROM hf_blobs WHERE sha256 = ? LIMIT 1", sha256).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UnreferencedBlobs lists up to limit digests of the repo that no hf_files row of the repo
// references any more.
func (s *Store) UnreferencedBlobs(ctx context.Context, hfRepoID string, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, s.rebind("SELECT b.sha256 FROM hf_blobs b WHERE b.hf_repo_id = ? AND NOT EXISTS (SELECT 1 FROM hf_files f JOIN hf_commits c ON c.id = f.commit_id WHERE c.hf_repo_id = b.hf_repo_id AND f.sha256 = b.sha256) ORDER BY b.sha256 LIMIT ?"), hfRepoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, rows.Err()
}

// DeleteBlob removes the hf_blobs row of the repo and releases its bytes in the same
// transaction. It deletes nothing, and reports false, while an hf_files row of the repo
// still references the digest. otherRepos reports whether another repo still holds the
// digest, in which case the object must stay in the object store.
func (s *Store) DeleteBlob(ctx context.Context, repositoryID, hfRepoID, sha256 string) (deleted, otherRepos bool, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var size int64
	err = s.row(ctx, tx, "SELECT size FROM hf_blobs WHERE hf_repo_id = ? AND sha256 = ?", hfRepoID, sha256).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var referenced int
	err = s.row(ctx, tx, "SELECT 1 FROM hf_files f JOIN hf_commits c ON c.id = f.commit_id WHERE c.hf_repo_id = ? AND f.sha256 = ? LIMIT 1", hfRepoID, sha256).Scan(&referenced)
	if err == nil {
		return false, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	if _, err := s.exec(ctx, tx, "DELETE FROM hf_blobs WHERE hf_repo_id = ? AND sha256 = ?", hfRepoID, sha256); err != nil {
		return false, false, err
	}
	var other int
	switch err := s.row(ctx, tx, "SELECT 1 FROM hf_blobs WHERE sha256 = ? LIMIT 1", sha256).Scan(&other); {
	case err == nil:
		otherRepos = true
	case !errors.Is(err, sql.ErrNoRows):
		return false, false, err
	}
	if err := capacity.Reserve(ctx, tx, repositoryID, -size, -1); err != nil {
		return false, false, err
	}
	return true, otherRepos, tx.Commit()
}

// ListRepos returns up to limit repos whose id sorts after the cursor, for the sweeps that walk every
// repo (blob garbage collection).
func (s *Store) ListRepos(ctx context.Context, after string, limit int) ([]Repo, error) {
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := s.DB.QueryContext(ctx, s.rebind("SELECT "+repoColumns+" FROM hf_repos WHERE id > ? ORDER BY id LIMIT ?"), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []Repo
	for rows.Next() {
		repo, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

// MaxObjectBytes returns the per-repository object size limit of a notarx repository, or 0
// when only the deployment limit applies.
func (s *Store) MaxObjectBytes(ctx context.Context, repositoryID string) (int64, error) {
	var limit sql.NullInt64
	err := s.row(ctx, s.DB, "SELECT max_object_bytes FROM hf_repository_settings WHERE repository_id = ?", repositoryID).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return limit.Int64, err
}

// SetMaxObjectBytes sets, or with 0 clears, the per-repository object size limit.
func (s *Store) SetMaxObjectBytes(ctx context.Context, repositoryID string, limit int64) error {
	if limit < 0 || !ids.Valid(repositoryID) {
		return ErrInvalid
	}
	query := "INSERT INTO hf_repository_settings (repository_id, max_object_bytes, updated_at) VALUES (?, ?, ?) "
	if s.Dialect == MySQL {
		query += "ON DUPLICATE KEY UPDATE max_object_bytes = VALUES(max_object_bytes), updated_at = VALUES(updated_at)"
	} else {
		query += "ON CONFLICT (repository_id) DO UPDATE SET max_object_bytes = EXCLUDED.max_object_bytes, updated_at = EXCLUDED.updated_at"
	}
	var value any
	if limit > 0 {
		value = limit
	}
	_, err := s.exec(ctx, s.DB, query, repositoryID, value, s.now())
	return err
}

// Headroom reports capacity.ErrQuotaExceeded when extraBytes more would exceed the quota of the
// repository, without reserving anything: the early check at the start of a fetch. The
// authoritative reservation is CommitBlob. PostgreSQL only until the counters are ported.
func (s *Store) Headroom(ctx context.Context, repositoryID string, extraBytes int64) error {
	return capacity.Headroom(ctx, s.DB, repositoryID, extraBytes)
}

// ForgetBlob removes a blob row whose object is gone from the object store, releases its bytes
// and marks every file of the repository with that digest as not cached, so the next read
// fetches it again. It is the repair path of a cached file whose object has disappeared.
func (s *Store) ForgetBlob(ctx context.Context, repositoryID, hfRepoID, sha256 string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.exec(ctx, tx, "UPDATE hf_files SET cached = ? WHERE sha256 = ? AND commit_id IN (SELECT id FROM hf_commits WHERE hf_repo_id = ?)", false, sha256, hfRepoID); err != nil {
		return err
	}
	var size int64
	err = s.row(ctx, tx, "SELECT size FROM hf_blobs WHERE hf_repo_id = ? AND sha256 = ?", hfRepoID, sha256).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx, tx, "DELETE FROM hf_blobs WHERE hf_repo_id = ? AND sha256 = ?", hfRepoID, sha256); err != nil {
		return err
	}
	if err := capacity.Reserve(ctx, tx, repositoryID, -size, -1); err != nil {
		return err
	}
	return tx.Commit()
}

// SearchCommits lists commits of a notarx repository for browsing, ordered by repo type,
// repo name and commit sha, after the cursor (a coordinate). term filters
// "<repo_type>/<org>/<name>" by prefix, or by substring when contains is set.
func (s *Store) SearchCommits(ctx context.Context, repositoryID, term string, contains bool, limit int, after string) ([]Commit, []Repo, error) {
	query := "SELECT r.id, r.repository_id, r.repo_type, r.repo_name, r.created_at, " + prefixed("c", commitColumns) +
		" FROM hf_commits c JOIN hf_repos r ON r.id = c.hf_repo_id WHERE r.repository_id = ?"
	args := []any{repositoryID}
	if term != "" {
		query += " AND CONCAT(r.repo_type, '/', r.repo_name) LIKE ? ESCAPE '!'"
		if contains {
			args = append(args, "%"+likeEscape(term)+"%")
		} else {
			args = append(args, likeEscape(term)+"%")
		}
	}
	if after != "" {
		cursor, ok := ParseCoordinate(after)
		if !ok {
			return nil, nil, ErrInvalid
		}
		query += " AND (r.repo_type, r.repo_name, c.commit_sha) > (?, ?, ?)"
		args = append(args, cursor.RepoType, cursor.Name, cursor.Commit)
	}
	query += " ORDER BY r.repo_type, r.repo_name, c.commit_sha LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var commits []Commit
	var repos []Repo
	for rows.Next() {
		var repo Repo
		var c Commit
		var parent, message, author sql.NullString
		var fileCount, totalBytes sql.NullInt64
		if err := rows.Scan(&repo.ID, &repo.RepositoryID, &repo.RepoType, &repo.RepoName, &repo.CreatedAt,
			&c.ID, &c.HFRepoID, &c.SHA, &parent, &message, &author, &c.CommittedAt, &c.Source, &fileCount, &totalBytes, &c.LastReadAt); err != nil {
			return nil, nil, err
		}
		c.ParentSHA, c.Message, c.Author = parent.String, message.String, author.String
		if fileCount.Valid {
			n := int(fileCount.Int64)
			c.FileCount = &n
		}
		if totalBytes.Valid {
			c.TotalBytes = &totalBytes.Int64
		}
		commits, repos = append(commits, c), append(repos, repo)
	}
	return commits, repos, rows.Err()
}

func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ", ")
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ", ")
}

// likeEscape escapes LIKE wildcards with ! as the escape character, which both engines
// read the same way (a backslash is a string escape in MySQL literals).
func likeEscape(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}
