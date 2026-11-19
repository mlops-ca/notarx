package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mlops-ca/notarx/internal/ids"
)

var _ ScanStore = (*PostgresStore)(nil)

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *PostgresStore) PutScanSBOM(ctx context.Context, sbom ScanSBOM) (ScanSBOM, error) {
	sbom, err := normalizeScanSBOM(sbom)
	if err != nil {
		return ScanSBOM{}, err
	}
	var body any
	if len(sbom.Body) > 0 {
		body = string(sbom.Body)
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO artifact_sbom (id, repository_id, artifact_digest, kind, source, generator, generator_version, spec_version, body_digest, body_size, body_key, body, component_count)
VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13)
ON CONFLICT (repository_id, artifact_digest, kind, source) DO UPDATE SET generator = EXCLUDED.generator, generator_version = EXCLUDED.generator_version,
  spec_version = EXCLUDED.spec_version, body_digest = EXCLUDED.body_digest, body_size = EXCLUDED.body_size, body_key = EXCLUDED.body_key,
  body = EXCLUDED.body, component_count = EXCLUDED.component_count
RETURNING id::text, created_at`,
		sbom.ID, sbom.RepositoryID, sbom.ArtifactDigest, sbom.Kind, sbom.Source, sbom.Generator, sbom.GeneratorVersion, sbom.SpecVersion,
		sbom.BodyDigest, sbom.BodySize, sbom.BodyKey, body, sbom.ComponentCount).Scan(&sbom.ID, &sbom.CreatedAt)
	if err != nil {
		return ScanSBOM{}, err
	}
	return sbom, nil
}

const scanSBOMColumns = `id::text, repository_id::text, artifact_digest, kind, source, generator, generator_version, spec_version, body_digest, body_size, body_key, body::text, component_count, created_at`

type scanRow interface{ Scan(dest ...any) error }

func scanScanSBOM(row scanRow) (ScanSBOM, error) {
	var sbom ScanSBOM
	var body sql.NullString
	err := row.Scan(&sbom.ID, &sbom.RepositoryID, &sbom.ArtifactDigest, &sbom.Kind, &sbom.Source, &sbom.Generator, &sbom.GeneratorVersion,
		&sbom.SpecVersion, &sbom.BodyDigest, &sbom.BodySize, &sbom.BodyKey, &body, &sbom.ComponentCount, &sbom.CreatedAt)
	if body.Valid {
		sbom.Body = []byte(body.String)
	}
	return sbom, err
}

func (s *PostgresStore) GetScanSBOM(ctx context.Context, repositoryID, artifactDigest, kind, source string) (ScanSBOM, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return ScanSBOM{}, err
	}
	sbom, err := scanScanSBOM(s.db.QueryRowContext(ctx, `SELECT `+scanSBOMColumns+` FROM artifact_sbom WHERE repository_id = $1::uuid AND artifact_digest = $2 AND kind = $3 AND source = $4`,
		repository, artifactDigest, kind, source))
	if errors.Is(err, sql.ErrNoRows) {
		return ScanSBOM{}, ErrNotFound
	}
	return sbom, err
}

func (s *PostgresStore) ListScanSBOMs(ctx context.Context, repositoryID, artifactDigest string) ([]ScanSBOM, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+scanSBOMColumns+` FROM artifact_sbom WHERE repository_id = $1::uuid AND artifact_digest = $2 ORDER BY kind, source`, repository, artifactDigest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ScanSBOM{}
	for rows.Next() {
		sbom, err := scanScanSBOM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sbom)
	}
	return out, rows.Err()
}

const scanFindingColumns = 20

func (s *PostgresStore) InsertScanFindings(ctx context.Context, findings []ScanFinding) error {
	if len(findings) == 0 {
		return nil
	}
	if len(findings) > MaxScanFindingBatch {
		return scanInvalid("a batch holds at most %d findings", MaxScanFindingBatch)
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO artifact_vulnerability_finding (vulnerability_id, ordinal, vuln_id, source, severity, severity_rank, component, version, fixed_version, location, title, description, url, cvss_score, cvss_vector, status, vex_status, vex_justification, epss, kev) VALUES `)
	args := make([]any, 0, len(findings)*scanFindingColumns)
	for i, raw := range findings {
		f, err := normalizeScanFinding(raw)
		if err != nil {
			return err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		base := len(args)
		sb.WriteString(fmt.Sprintf("($%d::uuid", base+1))
		for k := 2; k <= scanFindingColumns; k++ {
			sb.WriteString(fmt.Sprintf(",$%d", base+k))
		}
		sb.WriteByte(')')
		args = append(args, f.VulnerabilityID, f.Ordinal, f.VulnID, f.Source, f.Severity, f.SeverityRank, f.Component, f.Version, f.FixedVersion,
			f.Location, f.Title, nullString(f.Description), f.URL, f.CVSSScore, f.CVSSVector, f.Status, nullString(f.VEXStatus),
			nullString(f.VEXJustification), f.EPSS, f.KEV)
	}
	_, err := s.db.ExecContext(ctx, sb.String(), args...)
	return err
}

func (s *PostgresStore) SwapScanResult(ctx context.Context, result ScanResult) (ScanSwap, error) {
	result, err := normalizeScanResult(result)
	if err != nil {
		return ScanSwap{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScanSwap{}, err
	}
	defer func() { _ = tx.Rollback() }()
	args := []any{result.ID, result.RepositoryID, result.ArtifactDigest, result.Scanner, result.ScannerVersion, nullString(result.DBGenerationID), result.Status,
		result.Critical, result.High, result.Medium, result.Low, result.Unknown, result.SuppressedCount, result.FindingsTotal, result.Truncated,
		nullString(result.NotScannedReason), result.ScannedAt}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO artifact_vulnerability (id, repository_id, artifact_digest, scanner, scanner_version, db_generation_id, status, critical, high, medium, low, unknown, suppressed_count, findings_total, truncated, not_scanned_reason, scanned_at)
VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT (repository_id, artifact_digest) DO NOTHING`, args...)
	if err != nil {
		return ScanSwap{}, err
	}
	swap := ScanSwap{}
	if n, err := inserted.RowsAffected(); err != nil {
		return ScanSwap{}, err
	} else if n == 1 {
		swap.HistoryAppended = true
	} else {
		var previous ScanResult
		if err := tx.QueryRowContext(ctx, `SELECT id::text, status, critical, high, medium, low, unknown FROM artifact_vulnerability WHERE repository_id = $1::uuid AND artifact_digest = $2 FOR UPDATE`,
			result.RepositoryID, result.ArtifactDigest).Scan(&previous.ID, &previous.Status, &previous.Critical, &previous.High, &previous.Medium, &previous.Low, &previous.Unknown); err != nil {
			return ScanSwap{}, err
		}
		swap.PreviousID = previous.ID
		swap.HistoryAppended = scanCountsChanged(previous, result)
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_vulnerability SET id = $1::uuid, scanner = $4, scanner_version = $5, db_generation_id = $6::uuid, status = $7, critical = $8, high = $9, medium = $10, low = $11, unknown = $12,
  suppressed_count = $13, findings_total = $14, truncated = $15, not_scanned_reason = $16, scanned_at = $17 WHERE repository_id = $2::uuid AND artifact_digest = $3`, args...); err != nil {
			return ScanSwap{}, err
		}
	}
	if swap.HistoryAppended {
		entry := scanHistoryEntry(result, ids.New())
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_scan_history (id, repository_id, artifact_digest, scanner_version, db_generation_id, status, critical, high, medium, low, unknown, scanned_at)
VALUES ($1::uuid,$2::uuid,$3,$4,$5::uuid,$6,$7,$8,$9,$10,$11,$12)`, entry.ID, entry.RepositoryID, entry.ArtifactDigest, entry.ScannerVersion, nullString(entry.DBGenerationID),
			entry.Status, entry.Critical, entry.High, entry.Medium, entry.Low, entry.Unknown, entry.ScannedAt); err != nil {
			return ScanSwap{}, err
		}
	}
	return swap, tx.Commit()
}

func (s *PostgresStore) GetScanResult(ctx context.Context, repositoryID, artifactDigest string) (ScanResult, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return ScanResult{}, err
	}
	var r ScanResult
	var generation, reason sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT id::text, repository_id::text, artifact_digest, scanner, scanner_version, db_generation_id::text, status, critical, high, medium, low, unknown, suppressed_count, findings_total, truncated, not_scanned_reason, scanned_at
FROM artifact_vulnerability WHERE repository_id = $1::uuid AND artifact_digest = $2`, repository, artifactDigest).Scan(&r.ID, &r.RepositoryID, &r.ArtifactDigest, &r.Scanner,
		&r.ScannerVersion, &generation, &r.Status, &r.Critical, &r.High, &r.Medium, &r.Low, &r.Unknown, &r.SuppressedCount, &r.FindingsTotal, &r.Truncated, &reason, &r.ScannedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ScanResult{}, ErrNotFound
	}
	r.DBGenerationID, r.NotScannedReason = generation.String, reason.String
	return r, err
}

func (s *PostgresStore) ListScanFindings(ctx context.Context, vulnerabilityID string, after *ScanFindingCursor, limit int) ([]ScanFinding, error) {
	id, err := scanUUID("vulnerability id", vulnerabilityID)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	query := `SELECT vulnerability_id::text, ordinal, vuln_id, source, severity, severity_rank, component, version, fixed_version, location, title, description, url, cvss_score, cvss_vector, status, vex_status, vex_justification, epss, kev
FROM artifact_vulnerability_finding WHERE vulnerability_id = $1::uuid`
	args := []any{id}
	if after != nil {
		query += ` AND (severity_rank, vuln_id, ordinal) > ($2::smallint, $3::varchar, $4::integer)`
		args = append(args, after.SeverityRank, after.VulnID, after.Ordinal)
	}
	query += fmt.Sprintf(` ORDER BY severity_rank, vuln_id, ordinal LIMIT $%d`, len(args)+1)
	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ScanFinding{}
	for rows.Next() {
		var f ScanFinding
		var description, vexStatus, vexJustification sql.NullString
		var cvss, epss sql.NullFloat64
		var kev sql.NullBool
		if err := rows.Scan(&f.VulnerabilityID, &f.Ordinal, &f.VulnID, &f.Source, &f.Severity, &f.SeverityRank, &f.Component, &f.Version, &f.FixedVersion, &f.Location,
			&f.Title, &description, &f.URL, &cvss, &f.CVSSVector, &f.Status, &vexStatus, &vexJustification, &epss, &kev); err != nil {
			return nil, err
		}
		f.Description, f.VEXStatus, f.VEXJustification = description.String, vexStatus.String, vexJustification.String
		if cvss.Valid {
			f.CVSSScore = &cvss.Float64
		}
		if epss.Valid {
			f.EPSS = &epss.Float64
		}
		if kev.Valid {
			f.KEV = &kev.Bool
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SweepScanFindings(ctx context.Context, vulnerabilityID string, limit int) (int, error) {
	id, err := scanUUID("vulnerability id", vulnerabilityID)
	if err != nil {
		return 0, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return 0, err
	}
	// The guard and the delete are one statement, so a concurrent swap cannot make the id current
	// between them without the delete seeing it.
	result, err := s.db.ExecContext(ctx, `DELETE FROM artifact_vulnerability_finding WHERE vulnerability_id = $1::uuid AND ordinal IN (
  SELECT ordinal FROM artifact_vulnerability_finding WHERE vulnerability_id = $1::uuid
    AND NOT EXISTS (SELECT 1 FROM artifact_vulnerability WHERE id = $1::uuid) LIMIT $2)`, id, limit)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func (s *PostgresStore) ReplaceScanLicenses(ctx context.Context, repositoryID, artifactDigest string, licenses []ScanLicense) error {
	repository, unique, err := normalizeScanLicenses(repositoryID, artifactDigest, licenses)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_license WHERE repository_id = $1::uuid AND artifact_digest = $2`, repository, artifactDigest); err != nil {
		return err
	}
	for start := 0; start < len(unique); start += 1000 {
		chunk := unique[start:min(start+1000, len(unique))]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO artifact_license (repository_id, artifact_digest, spdx_id, source, confidence) VALUES `)
		args := make([]any, 0, len(chunk)*5)
		for i, l := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(fmt.Sprintf("($%d::uuid,$%d,$%d,$%d,$%d)", i*5+1, i*5+2, i*5+3, i*5+4, i*5+5))
			args = append(args, repository, artifactDigest, l.SPDXID, l.Source, l.Confidence)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) ListScanLicenses(ctx context.Context, repositoryID, artifactDigest string, after *ScanLicenseCursor, limit int) ([]ScanLicense, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	query := `SELECT repository_id::text, artifact_digest, spdx_id, source, confidence FROM artifact_license WHERE repository_id = $1::uuid AND artifact_digest = $2`
	args := []any{repository, artifactDigest}
	if after != nil {
		query += ` AND (spdx_id, source) > ($3::varchar, $4::varchar)`
		args = append(args, after.SPDXID, after.Source)
	}
	query += fmt.Sprintf(` ORDER BY spdx_id, source LIMIT $%d`, len(args)+1)
	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ScanLicense{}
	for rows.Next() {
		var l ScanLicense
		if err := rows.Scan(&l.RepositoryID, &l.ArtifactDigest, &l.SPDXID, &l.Source, &l.Confidence); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListScanHistory(ctx context.Context, repositoryID, artifactDigest string, limit int) ([]ScanHistoryEntry, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, repository_id::text, artifact_digest, scanner_version, db_generation_id::text, status, critical, high, medium, low, unknown, scanned_at
FROM artifact_scan_history WHERE repository_id = $1::uuid AND artifact_digest = $2 ORDER BY scanned_at DESC, id DESC LIMIT $3`, repository, artifactDigest, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ScanHistoryEntry{}
	for rows.Next() {
		var h ScanHistoryEntry
		var generation sql.NullString
		if err := rows.Scan(&h.ID, &h.RepositoryID, &h.ArtifactDigest, &h.ScannerVersion, &generation, &h.Status, &h.Critical, &h.High, &h.Medium, &h.Low, &h.Unknown, &h.ScannedAt); err != nil {
			return nil, err
		}
		h.DBGenerationID = generation.String
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RecordScannerDBGeneration(ctx context.Context, generation ScannerDBGeneration) (ScannerDBGeneration, error) {
	generation, err := normalizeScannerDBGeneration(generation)
	if err != nil {
		return ScannerDBGeneration{}, err
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO scanner_db_generation (id, kind, oci_digest, built_at, observed_at, source) VALUES ($1::uuid,$2,$3,$4,$5,$6)
ON CONFLICT (kind, oci_digest) DO UPDATE SET observed_at = EXCLUDED.observed_at
RETURNING id::text, built_at, observed_at, source`, generation.ID, generation.Kind, generation.OCIDigest, generation.BuiltAt, generation.ObservedAt, generation.Source).
		Scan(&generation.ID, &generation.BuiltAt, &generation.ObservedAt, &generation.Source)
	return generation, err
}

func (s *PostgresStore) LatestScannerDBGeneration(ctx context.Context, kind string) (ScannerDBGeneration, error) {
	if err := scanOneOf("kind", kind, ScannerDBTrivy, ScannerDBJava, ScannerDBChecks); err != nil {
		return ScannerDBGeneration{}, err
	}
	var g ScannerDBGeneration
	err := s.db.QueryRowContext(ctx, `SELECT id::text, kind, oci_digest, built_at, observed_at, source FROM scanner_db_generation WHERE kind = $1 ORDER BY built_at DESC, id DESC LIMIT 1`, kind).
		Scan(&g.ID, &g.Kind, &g.OCIDigest, &g.BuiltAt, &g.ObservedAt, &g.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return ScannerDBGeneration{}, ErrNotFound
	}
	return g, err
}

func (s *PostgresStore) PutVEXDocument(ctx context.Context, document VEXDocument) (VEXDocument, error) {
	document, err := normalizeVEXDocument(document)
	if err != nil {
		return VEXDocument{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO repository_vex_document (id, repository_id, format, body_digest, body_key, author, created_at, created_by) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8::uuid)`,
		document.ID, document.RepositoryID, document.Format, document.BodyDigest, document.BodyKey, document.Author, document.CreatedAt, nullString(document.CreatedBy))
	return document, err
}

func (s *PostgresStore) ListVEXDocuments(ctx context.Context, repositoryID string) ([]VEXDocument, error) {
	repository, err := scanUUID("repository id", repositoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, repository_id::text, format, body_digest, body_key, author, created_at, created_by::text FROM repository_vex_document WHERE repository_id = $1::uuid ORDER BY created_at DESC, id DESC`, repository)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VEXDocument{}
	for rows.Next() {
		var d VEXDocument
		var createdBy sql.NullString
		if err := rows.Scan(&d.ID, &d.RepositoryID, &d.Format, &d.BodyDigest, &d.BodyKey, &d.Author, &d.CreatedAt, &createdBy); err != nil {
			return nil, err
		}
		d.CreatedBy = createdBy.String
		d.CreatedAt = d.CreatedAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}
