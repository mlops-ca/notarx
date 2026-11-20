package repository

import (
	"context"
	"encoding/json"
	"strings"
)

// cargoProxyCrateSummary projects only validated cached index rows. The
// protocol may discover more from the upstream; management browse is bounded
// to metadata the Gateway has already verified and retained.
func cargoProxyCrateSummary(index CargoProxyIndex) (CargoCrateSummary, error) {
	rows, err := cargoProxyIndexRows(index.Name, index.Body)
	if err != nil {
		return CargoCrateSummary{}, err
	}
	summary := CargoCrateSummary{Name: index.Name}
	for _, row := range rows {
		var fields struct {
			Version string `json:"vers"`
		}
		if err := json.Unmarshal(row.Immutable, &fields); err != nil || fields.Version == "" {
			return CargoCrateSummary{}, ErrInvalidCargoIdentity
		}
		summary = addCargoCrateVersion(summary, fields.Version, "")
	}
	return summary, nil
}

func (s *MemoryStore) SearchCargoProxyCrates(ctx context.Context, repositoryID, query string, limit int, after string, matches ...ArtifactSearchMatch) ([]CargoCrateSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 201 {
		limit = 10
	}
	afterKey, err := cargoSearchAfterKey(after)
	if err != nil {
		return nil, err
	}
	needle := strings.TrimSpace(query)
	matches = []ArtifactSearchMatch{searchMatch(matches, ArtifactSearchContains)}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byName := make(map[string]CargoCrateSummary)
	for key, index := range s.cargoProxyIndexes {
		if !strings.HasPrefix(key, repositoryID+"\x00") || index.Status != 200 || !searchTextMatches(index.Name, needle, matches) {
			continue
		}
		summary, err := cargoProxyCrateSummary(index)
		if err != nil {
			return nil, err
		}
		byName[strings.TrimPrefix(key, repositoryID+"\x00")] = summary
	}
	items, _ := sortedCargoCrateSummaries(byName, limit, afterKey)
	return items, nil
}

func (s *PostgresStore) SearchCargoProxyCrates(ctx context.Context, repositoryID, query string, limit int, after string, matches ...ArtifactSearchMatch) ([]CargoCrateSummary, error) {
	if limit <= 0 || limit > 201 {
		limit = 10
	}
	afterKey, err := cargoSearchAfterKey(after)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,body,body_digest,body_size,body_key FROM native_cargo_proxy_indexes
		WHERE repository_id=$1::uuid AND status=200 AND `+searchTextSQL("name", "$2", []ArtifactSearchMatch{searchMatch(matches, ArtifactSearchContains)})+`
		AND collision_key>$3 ORDER BY collision_key LIMIT $4`, repositoryID, searchTextPattern(strings.TrimSpace(query), []ArtifactSearchMatch{searchMatch(matches, ArtifactSearchContains)}), afterKey, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	indexes := make([]CargoProxyIndex, 0, limit)
	refs := make([]payloadRef, 0, limit)
	for rows.Next() {
		var index CargoProxyIndex
		var ref payloadRef
		if err := rows.Scan(&index.Name, &index.Body, &ref.digest, &ref.size, &ref.key); err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The index bodies are read after the rows are closed, so the cursor does not wait on the object store.
	_ = rows.Close()
	slots := make([]payloadSlot, len(indexes))
	for i := range indexes {
		slots[i] = payloadSlot{ref: refs[i], set: func(data []byte) { indexes[i].Body = data }}
	}
	if err := s.resolvePayloads(ctx, slots); err != nil {
		return nil, err
	}
	items := make([]CargoCrateSummary, 0, len(indexes))
	for _, index := range indexes {
		summary, err := cargoProxyCrateSummary(index)
		if err != nil {
			return nil, err
		}
		items = append(items, summary)
	}
	return items, nil
}
