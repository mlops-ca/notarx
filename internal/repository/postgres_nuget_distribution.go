package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/durableactor"
	"sort"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
	dist "github.com/mlops-ca/notarx/internal/nugetdistribution"
)

const nuGetReplicationColumns = `id::text,plan_digest,source_repository_id::text,target_repository_id::text,actor,actor_kind,credential_id,version::text,state,lease_owner,lease_token,lease_fence,lease_until,created_at,updated_at`

type nuGetReplicationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadNuGetReplication(ctx context.Context, q nuGetReplicationQuery, id string, lock bool) (dist.Job, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var j dist.Job
	var until sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT `+nuGetReplicationColumns+` FROM native_nuget_replication_jobs WHERE id=$1::uuid`+suffix, id).Scan(&j.ID, &j.PlanDigest, &j.SourceRepositoryID, &j.TargetRepositoryID, &j.Actor, &j.ActorKind, &j.CredentialID, &j.Version, &j.State, &j.Lease.Owner, &j.Lease.Token, &j.Lease.Fence, &until, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, dist.ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.Lease.JobID = j.ID
	if until.Valid {
		j.Lease.Until = until.Time
	}
	rows, err := q.QueryContext(ctx, `SELECT item_index,package_id,version,digest,size,source_governance_version::text,state,attempts,code,COALESCE(target_session_id::text,''),result,listed,completed_at FROM native_nuget_replication_items WHERE job_id=$1::uuid ORDER BY item_index`, id)
	if err != nil {
		return j, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var i dist.Item
		var listed sql.NullBool
		var done sql.NullTime
		if err = rows.Scan(&i.Index, &i.PackageID, &i.Version, &i.Digest, &i.Size, &i.SourceGovernanceVersion, &i.State, &i.Attempts, &i.Code, &i.TargetSessionID, &i.Result, &listed, &done); err != nil {
			return j, err
		}
		if listed.Valid {
			i.Listed = &listed.Bool
		}
		if done.Valid {
			i.CompletedAt = &done.Time
		}
		j.Items = append(j.Items, i)
	}
	return j, rows.Err()
}
func (s *PostgresStore) nuGetReplicationTransaction(ctx context.Context, lease dist.Lease) (*sql.Tx, dist.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dist.Job{}, err
	}
	j, err := loadNuGetReplication(ctx, tx, lease.JobID, true)
	if err == nil && !nuGetReplicationLease(j, lease) {
		err = dist.ErrLeaseLost
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, j, err
	}
	return tx, j, nil
}
func nuGetReplicationActorTx(ctx context.Context, tx *sql.Tx, j dist.Job, a *dist.Authority) error {
	if !durableactor.ActorValid(j.ActorKind, j.Actor, j.CredentialID) {
		return dist.ErrAccessDenied
	}
	a.Actor = j.Actor
	a.Kind = j.ActorKind
	r, err := durableActorAuthorityTx(ctx, tx, j.ActorKind, j.Actor, j.CredentialID, nuGetReplicationKeyRole)
	if err != nil {
		return err
	}
	a.Role, a.MustChangePassword, a.ValidUntil, a.Active = r.Role, r.MustChangePassword, r.ValidUntil, r.Active
	return nil
}
func nuGetReplicationAuthorityTx(ctx context.Context, tx *sql.Tx, j dist.Job) (dist.Authority, error) {
	a := dist.Authority{}
	ids := []string{j.SourceRepositoryID, j.TargetRepositoryID}
	sort.Strings(ids)
	for _, id := range ids {
		var repo HostedRepository
		err := tx.QueryRowContext(ctx, `SELECT id::text,name,format,repo_type,state FROM hosted_repositories WHERE id=$1::uuid FOR UPDATE`, id).Scan(&repo.ID, &repo.Name, &repo.Format, &repo.Type, &repo.State)
		if errors.Is(err, sql.ErrNoRows) {
			return a, dist.ErrNotFound
		}
		if err != nil {
			return a, err
		}
		if repo.Format != FormatNuGet || repo.Type != RepositoryTypeHosted || repo.State != RepositoryActive {
			return a, dist.ErrNotFound
		}
		// Replacements and individual grant writes lock this same row. Creating
		// the default row also closes the otherwise-unlocked absent-set case.
		if _, err = tx.ExecContext(ctx, `INSERT INTO repository_grant_sets(repository_id,version) VALUES($1,1) ON CONFLICT DO NOTHING`, id); err != nil {
			return a, err
		}
		var version string
		if err = tx.QueryRowContext(ctx, `SELECT version::text FROM repository_grant_sets WHERE repository_id=$1::uuid FOR SHARE`, id).Scan(&version); err != nil {
			return a, err
		}
		set, err := loadRepositoryGrants(ctx, tx, id, version)
		if err != nil {
			return a, err
		}
		authority := nuGetReplicationGrantSet(repo, set)
		if id == j.SourceRepositoryID {
			a.Source = authority
		} else {
			a.Target = authority
		}
	}
	if err := nuGetReplicationActorTx(ctx, tx, j, &a); err != nil {
		return a, err
	}
	return a, nil
}

// nuGetReplicationSourceTx locks the source publication. Its nuspec stays in the object store when it is
// externalised: ref names it, and payloadsEqual compares it with another payload without a read.
func (s *PostgresStore) nuGetReplicationSourceTx(ctx context.Context, tx *sql.Tx, j dist.Job, index int) (NuGetPublication, payloadRef, error) {
	if index < 0 || index >= len(j.Items) {
		return NuGetPublication{}, payloadRef{}, dist.ErrInvalid
	}
	i := j.Items[index]
	p, ref, err := scanNuGetPublicationRef(tx.QueryRowContext(ctx, `SELECT `+nuGetPublicationColumns+` FROM native_nuget_publications WHERE repository_id=$1::uuid AND package_id=$2 AND version=$3 FOR SHARE`, j.SourceRepositoryID, i.PackageID, i.Version))
	if errors.Is(err, ErrNotFound) {
		return p, ref, dist.ErrSourceChanged
	}
	if err != nil {
		return p, ref, err
	}
	return p, ref, nuGetReplicationSource(p, i)
}
func saveNuGetReplicationItem(ctx context.Context, tx *sql.Tx, j dist.Job, index int) error {
	i := j.Items[index]
	_, err := tx.ExecContext(ctx, `UPDATE native_nuget_replication_items SET state=$3,attempts=$4,code=$5,target_session_id=NULLIF($6,'')::uuid,result=$7,listed=$8,completed_at=$9 WHERE job_id=$1::uuid AND item_index=$2`, j.ID, index, i.State, i.Attempts, i.Code, i.TargetSessionID, i.Result, i.Listed, i.CompletedAt)
	return err
}
func saveNuGetReplicationProgress(ctx context.Context, tx *sql.Tx, j dist.Job, lease dist.Lease, validUntil time.Time) error {
	var expiry any
	if !validUntil.IsZero() {
		expiry = validUntil
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_nuget_replication_jobs SET version=version+1,updated_at=clock_timestamp() WHERE id=$1::uuid AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_fence=$4 AND lease_until>clock_timestamp() AND ($5::timestamptz IS NULL OR $5>clock_timestamp())`, j.ID, lease.Owner, lease.Token, lease.Fence, expiry)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		if !validUntil.IsZero() && !validUntil.After(time.Now()) {
			return dist.ErrAccessDenied
		}
		return dist.ErrLeaseLost
	}
	return nil
}
func (s *PostgresStore) CreateNuGetReplication(ctx context.Context, j dist.Job, check dist.Check) (dist.Job, bool, error) {
	if !dist.ValidNewJob(j) {
		return dist.Job{}, false, dist.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dist.Job{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "nuget-replication-create:"+j.ID); err != nil {
		return dist.Job{}, false, err
	}
	a, err := nuGetReplicationAuthorityTx(ctx, tx, j)
	if err != nil {
		return dist.Job{}, false, err
	}
	if err = nuGetReplicationCheck(ctx, check, a); err != nil {
		return dist.Job{}, false, err
	}
	prior, err := loadNuGetReplication(ctx, tx, j.ID, false)
	if err == nil {
		if prior.PlanDigest != j.PlanDigest || prior.Actor != j.Actor || prior.ActorKind != j.ActorKind || prior.CredentialID != j.CredentialID {
			return dist.Job{}, false, dist.ErrConflict
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, dist.ErrNotFound) {
		return dist.Job{}, false, err
	}
	extra := int64(0)
	for i, item := range j.Items {
		if _, _, err = s.nuGetReplicationSourceTx(ctx, tx, j, i); err != nil {
			return dist.Job{}, false, err
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_nuget_publications WHERE repository_id=$1::uuid AND package_id=$2 AND version=$3)`, j.TargetRepositoryID, item.PackageID, item.Version).Scan(&exists); err != nil {
			return dist.Job{}, false, err
		}
		if !exists {
			extra += item.Size
		}
	}
	// Pre-flight only: each item reserves its bytes through its own session when it runs. Sessions of the
	// target past their deadline release their bytes first, so abandoned ones do not refuse the job.
	if err = releaseExpiredNuGetSessions(ctx, tx, `SELECT id FROM native_nuget_publication_sessions WHERE repository_id=$1::uuid AND state IN ('reserved','staged') AND expires_at<=now()`, j.TargetRepositoryID); err != nil {
		return dist.Job{}, false, err
	}
	if err = capacity.Headroom(ctx, tx, j.TargetRepositoryID, extra); err != nil {
		return dist.Job{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_nuget_replication_jobs(id,plan_digest,source_repository_id,target_repository_id,actor,actor_kind,credential_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,'queued')`, j.ID, j.PlanDigest, j.SourceRepositoryID, j.TargetRepositoryID, j.Actor, j.ActorKind, j.CredentialID)
	if err != nil {
		return dist.Job{}, false, err
	}
	for _, i := range j.Items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO native_nuget_replication_items(job_id,item_index,package_id,version,digest,size,source_governance_version,state) VALUES($1,$2,$3,$4,$5,$6,$7,'pending')`, j.ID, i.Index, i.PackageID, i.Version, i.Digest, i.Size, i.SourceGovernanceVersion); err != nil {
			return dist.Job{}, false, err
		}
	}
	j, err = loadNuGetReplication(ctx, tx, j.ID, false)
	if err != nil {
		return j, false, err
	}
	if err = insertAudit(ctx, tx, nuGetReplicationAudit(j, a.Target.Name, "create", 202, nil)); err != nil {
		return dist.Job{}, false, err
	}
	return j, false, tx.Commit()
}
func (s *PostgresStore) GetNuGetReplication(ctx context.Context, id string) (dist.Job, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return dist.Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	j, err := loadNuGetReplication(ctx, tx, id, false)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *PostgresStore) RetryNuGetReplication(ctx context.Context, id, expected string, indexes []int, key string, check dist.Check) (dist.Job, bool, error) {
	digest, err := dist.RetryDigest(expected, indexes)
	if err != nil || key == "" || len(key) > 200 || strings.ContainsAny(key, "\x00\r\n") {
		return dist.Job{}, false, dist.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dist.Job{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	j, err := loadNuGetReplication(ctx, tx, id, true)
	if err != nil {
		return j, false, err
	}
	a, err := nuGetReplicationAuthorityTx(ctx, tx, j)
	if err != nil {
		return dist.Job{}, false, err
	}
	if err = nuGetReplicationCheck(ctx, check, a); err != nil {
		return dist.Job{}, false, err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM native_nuget_replication_retries WHERE job_id=$1::uuid AND request_key=$2`, id, key).Scan(&prior)
	if err == nil {
		if prior != digest {
			return dist.Job{}, false, dist.ErrConflict
		}
		return j, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dist.Job{}, false, err
	}
	j, err = nuGetReplicationRetry(j, expected, indexes)
	if err != nil {
		return dist.Job{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_nuget_replication_jobs SET state='queued',version=version+1,lease_owner='',lease_token='',lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return dist.Job{}, false, err
	}
	for _, index := range indexes {
		if err = saveNuGetReplicationItem(ctx, tx, j, index); err != nil {
			return dist.Job{}, false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_nuget_replication_retries(job_id,request_key,payload_digest) VALUES($1,$2,$3)`, id, key, digest); err != nil {
		return dist.Job{}, false, err
	}
	if err = insertAudit(ctx, tx, nuGetReplicationAudit(j, a.Target.Name, "retry", 202, nil)); err != nil {
		return dist.Job{}, false, err
	}
	j, err = loadNuGetReplication(ctx, tx, id, false)
	if err != nil {
		return j, false, err
	}
	return j, false, tx.Commit()
}
func (s *PostgresStore) ClaimNuGetReplication(ctx context.Context, owner string, d time.Duration) (dist.Job, error) {
	if owner == "" || len(owner) > 512 || !dist.ValidDuration(d) {
		return dist.Job{}, dist.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dist.Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM native_nuget_replication_jobs WHERE state='queued' OR state='running' AND lease_until<=clock_timestamp() ORDER BY created_at,native_nuget_replication_jobs.id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return dist.Job{}, dist.ErrNotFound
	}
	if err != nil {
		return dist.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_nuget_replication_jobs SET state='running',version=version+1,lease_owner=$2,lease_token=$3,lease_fence=lease_fence+1,lease_until=clock_timestamp()+$4::bigint*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1::uuid`, id, owner, ids.New(), d.Milliseconds()); err != nil {
		return dist.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_nuget_replication_items SET state='pending' WHERE job_id=$1::uuid AND state='running'`, id); err != nil {
		return dist.Job{}, err
	}
	j, err := loadNuGetReplication(ctx, tx, id, false)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *PostgresStore) RenewNuGetReplication(ctx context.Context, lease dist.Lease, d time.Duration) (dist.Lease, error) {
	if !dist.ValidDuration(d) {
		return dist.Lease{}, dist.ErrInvalid
	}
	var until time.Time
	err := s.db.QueryRowContext(ctx, `UPDATE native_nuget_replication_jobs SET lease_until=clock_timestamp()+$5::bigint*interval '1 millisecond' WHERE id=$1::uuid AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_fence=$4 AND lease_until>clock_timestamp() RETURNING lease_until`, lease.JobID, lease.Owner, lease.Token, lease.Fence, d.Milliseconds()).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return dist.Lease{}, dist.ErrLeaseLost
	}
	lease.Until = until
	return lease, err
}
func (s *PostgresStore) BindNuGetReplicationSession(ctx context.Context, lease dist.Lease, index int, sessionID string, check dist.Check) (dist.Job, error) {
	tx, j, err := s.nuGetReplicationTransaction(ctx, lease)
	if err != nil {
		return dist.Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := nuGetReplicationAuthorityTx(ctx, tx, j)
	if err != nil {
		return dist.Job{}, err
	}
	if err = nuGetReplicationCheck(ctx, check, a); err != nil {
		return dist.Job{}, err
	}
	source, _, err := s.nuGetReplicationSourceTx(ctx, tx, j, index)
	if err != nil {
		return dist.Job{}, err
	}
	session, err := scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid FOR SHARE`, sessionID))
	if err != nil {
		return dist.Job{}, err
	}
	if session.RepositoryID != j.TargetRepositoryID || session.PackageID != source.PackageID || session.Version != source.Version || session.Digest != source.Digest || session.Size != source.Size || session.SourceVersion != "" {
		return dist.Job{}, dist.ErrTargetBlocked
	}
	item := j.Items[index]
	if item.State != "pending" && item.State != "running" {
		return dist.Job{}, dist.ErrConflict
	}
	if item.State != "running" {
		item.Attempts++
	}
	item.State = "running"
	item.TargetSessionID = session.ID
	j.Items[index] = item
	if err = saveNuGetReplicationItem(ctx, tx, j, index); err != nil {
		return dist.Job{}, err
	}
	if err = saveNuGetReplicationProgress(ctx, tx, j, lease, a.ValidUntil); err != nil {
		return dist.Job{}, err
	}
	j, err = loadNuGetReplication(ctx, tx, j.ID, false)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *PostgresStore) CompleteNuGetReplicationItem(ctx context.Context, lease dist.Lease, index int, proof dist.Commit, check dist.Check) (dist.Job, error) {
	// The target's nuspec reaches the object store before the transaction opens (see CommitNuGetPublication).
	staged, err := s.stageNuGetReplication(ctx, lease, proof.SessionID)
	if err != nil {
		return dist.Job{}, err
	}
	tx, j, err := s.nuGetReplicationTransaction(ctx, lease)
	if err != nil {
		return dist.Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := nuGetReplicationAuthorityTx(ctx, tx, j)
	if err != nil {
		return dist.Job{}, err
	}
	if err = nuGetReplicationCheck(ctx, check, a); err != nil {
		return dist.Job{}, err
	}
	source, sourceRef, err := s.nuGetReplicationSourceTx(ctx, tx, j, index)
	if err != nil {
		return dist.Job{}, err
	}
	item := j.Items[index]
	if item.State == "succeeded" {
		return j, tx.Commit()
	}
	if item.State != "running" || item.TargetSessionID != proof.SessionID {
		return dist.Job{}, dist.ErrConflict
	}
	session, err := scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid FOR UPDATE`, proof.SessionID))
	if err != nil {
		return dist.Job{}, err
	}
	if session.RepositoryID != j.TargetRepositoryID {
		return dist.Job{}, dist.ErrTargetBlocked
	}
	target, targetRef, err := scanNuGetPublicationRef(tx.QueryRowContext(ctx, `SELECT `+nuGetPublicationColumns+` FROM native_nuget_publications WHERE repository_id=$1::uuid AND package_id=$2 AND version=$3 FOR UPDATE`, j.TargetRepositoryID, item.PackageID, item.Version))
	if err == nil {
		if err = nuGetReplicationExisting(source, target, payloadsEqual(source.Nuspec, sourceRef, target.Nuspec, targetRef)); err != nil {
			return dist.Job{}, err
		}
		if target.SessionID != session.ID {
			return dist.Job{}, dist.ErrTargetBlocked
		}
		item.Result = "already_present"
	} else if errors.Is(err, ErrNotFound) {
		if err = nuGetReplicationSession(source, session, j.TargetRepositoryID, payloadsEqual(source.Nuspec, sourceRef, session.Nuspec, payloadRef{})); err != nil {
			return dist.Job{}, err
		}
		if session.Actor != j.Actor || session.State != "staged" || !session.ExpiresAt.After(time.Now()) || !validNuGetStaging(session) {
			return dist.Job{}, dist.ErrTargetBlocked
		}
		if _, err = tx.ExecContext(ctx, `UPDATE native_nuget_publication_sessions SET state='committed',committed_at=clock_timestamp() WHERE id=$1::uuid`, session.ID); err != nil {
			return dist.Job{}, err
		}
		target, err = s.insertNuGetPublicationFromSession(ctx, tx, session, staged)
		if err != nil {
			return dist.Job{}, err
		}
		if err = insertAudit(ctx, tx, nuGetPublicationAudit(target, a.Target.Name)); err != nil {
			return dist.Job{}, err
		}
		if err = capacity.Reserve(ctx, tx, target.RepositoryID, 0, 1); err != nil {
			return dist.Job{}, err
		}
		item.Result = "published"
	} else {
		return dist.Job{}, err
	}
	now := time.Now().UTC()
	item.State = "succeeded"
	item.Code = ""
	listed := target.Governance.Listed
	item.Listed = &listed
	item.CompletedAt = &now
	j.Items[index] = item
	j.Version = dist.NextVersion(j.Version)
	if err = saveNuGetReplicationItem(ctx, tx, j, index); err != nil {
		return dist.Job{}, err
	}
	if err = saveNuGetReplicationProgress(ctx, tx, j, lease, a.ValidUntil); err != nil {
		return dist.Job{}, err
	}
	if err = insertAudit(ctx, tx, nuGetReplicationAudit(j, a.Target.Name, "item", 200, &item)); err != nil {
		return dist.Job{}, err
	}
	j, err = loadNuGetReplication(ctx, tx, j.ID, false)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *PostgresStore) FailNuGetReplicationItem(ctx context.Context, lease dist.Lease, index int, code string) error {
	if !dist.ValidFailureCode(code) {
		return dist.ErrInvalid
	}
	tx, j, err := s.nuGetReplicationTransaction(ctx, lease)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if index < 0 || index >= len(j.Items) || j.Items[index].State == "succeeded" {
		return dist.ErrConflict
	}
	item := j.Items[index]
	if item.State == "pending" {
		item.Attempts++
	}
	item.State = "failed"
	item.Code = code
	now := time.Now().UTC()
	item.CompletedAt = &now
	j.Items[index] = item
	j.Version = dist.NextVersion(j.Version)
	if err = saveNuGetReplicationItem(ctx, tx, j, index); err != nil {
		return err
	}
	if err = saveNuGetReplicationProgress(ctx, tx, j, lease, time.Time{}); err != nil {
		return err
	}
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM hosted_repositories WHERE id=$1::uuid`, j.TargetRepositoryID).Scan(&name); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, nuGetReplicationAudit(j, name, "item", dist.FailureStatus(code), &item)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PostgresStore) FinishNuGetReplication(ctx context.Context, lease dist.Lease) (dist.Job, error) {
	tx, j, err := s.nuGetReplicationTransaction(ctx, lease)
	if err != nil {
		return j, err
	}
	defer func() { _ = tx.Rollback() }()
	j.State = "succeeded"
	for _, item := range j.Items {
		if item.State == "pending" || item.State == "running" {
			return dist.Job{}, dist.ErrConflict
		}
		if item.State == "failed" {
			j.State = "failed"
		}
	}
	if err = saveNuGetReplicationProgress(ctx, tx, j, lease, time.Time{}); err != nil {
		return dist.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_nuget_replication_jobs SET state=$2,lease_owner='',lease_token='',lease_until=NULL WHERE id=$1::uuid`, j.ID, j.State); err != nil {
		return dist.Job{}, err
	}
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM hosted_repositories WHERE id=$1::uuid`, j.TargetRepositoryID).Scan(&name); err != nil {
		return dist.Job{}, err
	}
	j.Version = dist.NextVersion(j.Version)
	status := 200
	if j.State == "failed" {
		status = 409
	}
	if err = insertAudit(ctx, tx, nuGetReplicationAudit(j, name, "complete", status, nil)); err != nil {
		return dist.Job{}, err
	}
	j, err = loadNuGetReplication(ctx, tx, j.ID, false)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (s *PostgresStore) AuthorizeNuGetReplicationItem(ctx context.Context, lease dist.Lease, index int, check dist.Check) error {
	tx, j, err := s.nuGetReplicationTransaction(ctx, lease)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := nuGetReplicationAuthorityTx(ctx, tx, j)
	if err != nil {
		return err
	}
	if err = nuGetReplicationCheck(ctx, check, a); err != nil {
		return err
	}
	if _, _, err = s.nuGetReplicationSourceTx(ctx, tx, j, index); err != nil {
		return err
	}
	return tx.Commit()
}

// stageNuGetReplication stages the nuspec of the target session a replication worker is completing, once its lease
// is live and the session belongs to the job's target repository and actor, the checks the transaction makes.
func (s *PostgresStore) stageNuGetReplication(ctx context.Context, lease dist.Lease, sessionID string) (stagedPayload, error) {
	session, err := s.GetNuGetSession(ctx, sessionID)
	if err != nil {
		return stagedPayload{}, nil
	}
	var live bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_nuget_replication_jobs WHERE id=$1::uuid AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_fence=$4 AND lease_until>clock_timestamp() AND target_repository_id=$5::uuid AND actor=$6)`, lease.JobID, lease.Owner, lease.Token, lease.Fence, session.RepositoryID, session.Actor).Scan(&live)
	if err != nil || !live {
		return stagedPayload{}, nil
	}
	return s.stageNuGetNuspec(ctx, session, session.Actor)
}
