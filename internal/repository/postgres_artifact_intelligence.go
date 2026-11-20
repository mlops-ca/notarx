package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const artifactIntelligenceColumns = `repository_id::text,format,coordinate,digest,signatures,sboms,provenance,licenses,vulnerability,version::text,created_at,updated_at,updated_by,body_digest,body_size,body_key`

func (s *PostgresStore) scanArtifactIntelligence(ctx context.Context, scanner interface{ Scan(...any) error }) (ArtifactIntelligence, error) {
	var value ArtifactIntelligence
	var ref payloadRef
	var signatures, sboms, provenance, licenses, vulnerability []byte
	if err := scanner.Scan(&value.RepositoryID, &value.Format, &value.Coordinate, &value.Digest, &signatures, &sboms, &provenance, &licenses, &vulnerability, &value.Version, &value.CreatedAt, &value.UpdatedAt, &value.UpdatedBy, &ref.digest, &ref.size, &ref.key); err != nil {
		return ArtifactIntelligence{}, err
	}
	// The findings of a large vulnerability result live in the object store; the row keeps the counts.
	if ref.external() {
		full, err := s.internalize(ctx, nil, ref)
		if err != nil {
			return ArtifactIntelligence{}, err
		}
		vulnerability = full
	}
	if err := json.Unmarshal(signatures, &value.Signatures); err != nil {
		return ArtifactIntelligence{}, err
	}
	if err := json.Unmarshal(sboms, &value.SBOMs); err != nil {
		return ArtifactIntelligence{}, err
	}
	if len(provenance) > 0 && string(provenance) != "null" {
		value.Provenance = &ArtifactProvenance{}
		if err := json.Unmarshal(provenance, value.Provenance); err != nil {
			return ArtifactIntelligence{}, err
		}
	}
	if err := json.Unmarshal(licenses, &value.Licenses); err != nil {
		return ArtifactIntelligence{}, err
	}
	if len(vulnerability) > 0 && string(vulnerability) != "null" {
		value.Vulnerability = &ArtifactVulnerabilitySummary{}
		if err := json.Unmarshal(vulnerability, value.Vulnerability); err != nil {
			return ArtifactIntelligence{}, err
		}
	}
	return value, nil
}

func (s *PostgresStore) marshalArtifactIntelligence(ctx context.Context, value ArtifactIntelligence) (any, any, any, any, any, payloadRef, error) {
	// Keep collection fields as JSON arrays even when callers omit them in a
	// repository-level write. The API contract uses [] rather than null.
	if value.Signatures == nil {
		value.Signatures = []ArtifactSignature{}
	}
	if value.SBOMs == nil {
		value.SBOMs = []ArtifactSBOM{}
	}
	if value.Licenses == nil {
		value.Licenses = []ArtifactLicense{}
	}
	signatures, err := json.Marshal(value.Signatures)
	if err != nil {
		return nil, nil, nil, nil, nil, payloadRef{}, err
	}
	sboms, err := json.Marshal(value.SBOMs)
	if err != nil {
		return nil, nil, nil, nil, nil, payloadRef{}, err
	}
	provenance, err := json.Marshal(value.Provenance)
	if err != nil {
		return nil, nil, nil, nil, nil, payloadRef{}, err
	}
	licenses, err := json.Marshal(value.Licenses)
	if err != nil {
		return nil, nil, nil, nil, nil, payloadRef{}, err
	}
	vulnerability, err := json.Marshal(value.Vulnerability)
	if err != nil {
		return nil, nil, nil, nil, nil, payloadRef{}, err
	}
	// A vulnerability result above the inline threshold keeps its counts in the row, which the
	// search projections read, and moves the whole document with its findings to the object store.
	var ref payloadRef
	if value.Vulnerability != nil && len(value.Vulnerability.Findings) > 0 {
		_, ref, err = s.externalize(ctx, vulnerability)
		if err != nil {
			return nil, nil, nil, nil, nil, payloadRef{}, err
		}
		if ref.external() {
			summary := *value.Vulnerability
			summary.Findings = nil
			if vulnerability, err = json.Marshal(summary); err != nil {
				return nil, nil, nil, nil, nil, payloadRef{}, err
			}
		}
	}
	return string(signatures), string(sboms), string(provenance), string(licenses), string(vulnerability), ref, nil
}

func (s *PostgresStore) GetArtifactIntelligence(ctx context.Context, repositoryID string, format Format, coordinate, digest string) (ArtifactIntelligence, error) {
	value, err := s.scanArtifactIntelligence(ctx, s.db.QueryRowContext(ctx, `SELECT `+artifactIntelligenceColumns+` FROM artifact_intelligence WHERE repository_id=$1::uuid AND format=$2 AND coordinate=$3 AND digest=$4`, repositoryID, format, coordinate, digest))
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactIntelligence{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) ReplaceArtifactIntelligence(ctx context.Context, value ArtifactIntelligence, expectedVersion string, audit ...AuditRecord) (ArtifactIntelligence, error) {
	signatures, sboms, provenance, licenses, vulnerability, ref, err := s.marshalArtifactIntelligence(ctx, value)
	if err != nil {
		return ArtifactIntelligence{}, err
	}
	query := `INSERT INTO artifact_intelligence (repository_id,format,coordinate,digest,signatures,sboms,provenance,licenses,vulnerability,version,updated_by,body_digest,body_size,body_key)
VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,1,$10,$12::text,$13::bigint,$14::text)
ON CONFLICT (repository_id,format,coordinate,digest) DO UPDATE SET signatures=$5::jsonb,sboms=$6::jsonb,provenance=$7::jsonb,licenses=$8::jsonb,vulnerability=$9::jsonb,version=artifact_intelligence.version+1,updated_by=$10,updated_at=now(),body_digest=$12::text,body_size=$13::bigint,body_key=$14::text
WHERE artifact_intelligence.version=$11::text::bigint
RETURNING ` + artifactIntelligenceColumns
	if expectedVersion == "" {
		expectedVersion = "0"
	}
	bodyDigest, bodySize, bodyKey := ref.columns()
	return runAudited(ctx, s, audit, func(q sqlRunner) (ArtifactIntelligence, error) {
		result, err := s.scanArtifactIntelligence(ctx, q.QueryRowContext(ctx, query, value.RepositoryID, value.Format, value.Coordinate, value.Digest, signatures, sboms, provenance, licenses, vulnerability, value.UpdatedBy, expectedVersion, bodyDigest, bodySize, bodyKey))
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_intelligence WHERE repository_id=$1::uuid AND format=$2 AND coordinate=$3 AND digest=$4)`, value.RepositoryID, value.Format, value.Coordinate, value.Digest).Scan(&exists); queryErr != nil {
				return ArtifactIntelligence{}, queryErr
			}
			if exists {
				return ArtifactIntelligence{}, ErrVersionConflict
			}
			return ArtifactIntelligence{}, ErrNotFound
		}
		return result, err
	})
}
