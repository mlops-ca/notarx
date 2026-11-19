package repository

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

var _ ScanStore = (*MemoryStore)(nil)

type memoryScanState struct {
	sboms       map[[4]string]ScanSBOM
	results     map[[2]string]ScanResult
	currentIDs  map[string]bool
	findings    map[string][]ScanFinding
	licenses    map[[2]string][]ScanLicense
	history     []ScanHistoryEntry
	generations map[[2]string]ScannerDBGeneration
	vex         []VEXDocument
}

func newMemoryScanState() *memoryScanState {
	return &memoryScanState{sboms: map[[4]string]ScanSBOM{}, results: map[[2]string]ScanResult{}, currentIDs: map[string]bool{},
		findings: map[string][]ScanFinding{}, licenses: map[[2]string][]ScanLicense{}, generations: map[[2]string]ScannerDBGeneration{}}
}

func cloneScanSBOM(s ScanSBOM) ScanSBOM {
	s.Body = slices.Clone(s.Body)
	return s
}

func (s *MemoryStore) PutScanSBOM(_ context.Context, sbom ScanSBOM) (ScanSBOM, error) {
	sbom, err := normalizeScanSBOM(sbom)
	if err != nil {
		return ScanSBOM{}, err
	}
	key := [4]string{sbom.RepositoryID, sbom.ArtifactDigest, sbom.Kind, sbom.Source}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.scan.sboms[key]; ok {
		sbom.ID, sbom.CreatedAt = existing.ID, existing.CreatedAt
	} else if sbom.CreatedAt.IsZero() {
		sbom.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.scan.sboms[key] = cloneScanSBOM(sbom)
	return cloneScanSBOM(sbom), nil
}

func (s *MemoryStore) GetScanSBOM(_ context.Context, repositoryID, artifactDigest, kind, source string) (ScanSBOM, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return ScanSBOM{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sbom, ok := s.scan.sboms[[4]string{repository, artifactDigest, kind, source}]
	if !ok {
		return ScanSBOM{}, ErrNotFound
	}
	return cloneScanSBOM(sbom), nil
}

func (s *MemoryStore) ListScanSBOMs(_ context.Context, repositoryID, artifactDigest string) ([]ScanSBOM, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ScanSBOM{}
	for key, sbom := range s.scan.sboms {
		if key[0] == repository && key[1] == artifactDigest {
			out = append(out, cloneScanSBOM(sbom))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

func (s *MemoryStore) InsertScanFindings(_ context.Context, findings []ScanFinding) error {
	if len(findings) == 0 {
		return nil
	}
	if len(findings) > MaxScanFindingBatch {
		return scanInvalid("a batch holds at most %d findings", MaxScanFindingBatch)
	}
	normalized := make([]ScanFinding, len(findings))
	for i, f := range findings {
		n, err := normalizeScanFinding(f)
		if err != nil {
			return err
		}
		normalized[i] = n
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	taken := map[[2]any]bool{}
	for _, f := range normalized {
		taken[[2]any{f.VulnerabilityID, f.Ordinal}] = true
	}
	if len(taken) != len(normalized) {
		return scanInvalid("a batch repeats an ordinal")
	}
	for _, f := range normalized {
		for _, existing := range s.scan.findings[f.VulnerabilityID] {
			if existing.Ordinal == f.Ordinal {
				return scanInvalid("finding %s/%d already exists", f.VulnerabilityID, f.Ordinal)
			}
		}
	}
	for _, f := range normalized {
		s.scan.findings[f.VulnerabilityID] = append(s.scan.findings[f.VulnerabilityID], cloneScanFinding(f))
	}
	return nil
}

func (s *MemoryStore) SwapScanResult(_ context.Context, result ScanResult) (ScanSwap, error) {
	result, err := normalizeScanResult(result)
	if err != nil {
		return ScanSwap{}, err
	}
	key := [2]string{result.RepositoryID, result.ArtifactDigest}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.scan.results[key]
	swap := ScanSwap{}
	if existed && previous.ScannedAt.After(result.ScannedAt) {
		return ScanSwap{Superseded: true}, nil
	}
	if existed {
		swap.PreviousID = previous.ID
		delete(s.scan.currentIDs, previous.ID)
	}
	s.scan.results[key] = result
	s.scan.currentIDs[result.ID] = true
	if !existed || scanCountsChanged(previous, result) {
		s.scan.history = append(s.scan.history, scanHistoryEntry(result, ids.New()))
		swap.HistoryAppended = true
	}
	return swap, nil
}

func (s *MemoryStore) GetScanResult(_ context.Context, repositoryID, artifactDigest string) (ScanResult, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return ScanResult{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.scan.results[[2]string{repository, artifactDigest}]
	if !ok {
		return ScanResult{}, ErrNotFound
	}
	return result, nil
}

func (s *MemoryStore) ListScanFindings(_ context.Context, vulnerabilityID string, after *ScanFindingCursor, limit int) ([]ScanFinding, error) {
	id, err := scanUUID("vulnerability id", vulnerabilityID)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := slices.Clone(s.scan.findings[id])
	sort.Slice(all, func(i, j int) bool { return scanFindingLess(all[i].Cursor(), all[j].Cursor()) })
	out := []ScanFinding{}
	for _, f := range all {
		if after != nil && !scanFindingLess(*after, f.Cursor()) {
			continue
		}
		out = append(out, cloneScanFinding(f))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) ListAbandonedScanVulnerabilityIDs(_ context.Context, olderThan time.Time, limit int) ([]string, error) {
	limit, err := scanLimit(limit)
	if err != nil {
		return nil, err
	}
	floor := scanVulnerabilityIDFloor(olderThan)
	s.mu.RLock()
	defer s.mu.RUnlock()
	candidates := []string{}
	for id, rows := range s.scan.findings {
		if len(rows) > 0 && id < floor && !s.scan.currentIDs[id] {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func scanFindingLess(a, b ScanFindingCursor) bool {
	if a.SeverityRank != b.SeverityRank {
		return a.SeverityRank < b.SeverityRank
	}
	if a.VulnID != b.VulnID {
		return a.VulnID < b.VulnID
	}
	return a.Ordinal < b.Ordinal
}

func (s *MemoryStore) SweepScanFindings(_ context.Context, vulnerabilityID string, limit int) (int, error) {
	id, err := scanUUID("vulnerability id", vulnerabilityID)
	if err != nil {
		return 0, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scan.currentIDs[id] {
		return 0, scanInvalid("vulnerability id %s is the current result", id)
	}
	rows := s.scan.findings[id]
	n := min(limit, len(rows))
	if n == len(rows) {
		delete(s.scan.findings, id)
	} else {
		s.scan.findings[id] = rows[n:]
	}
	return n, nil
}

func (s *MemoryStore) ReplaceScanLicenses(_ context.Context, repositoryID, artifactDigest string, licenses []ScanLicense) error {
	repository, unique, err := normalizeScanLicenses(repositoryID, artifactDigest, licenses)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]string{repository, artifactDigest}
	if len(unique) == 0 {
		delete(s.scan.licenses, key)
		return nil
	}
	s.scan.licenses[key] = unique
	return nil
}

func (s *MemoryStore) ListScanLicenses(_ context.Context, repositoryID, artifactDigest string, after *ScanLicenseCursor, limit int) ([]ScanLicense, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := slices.Clone(s.scan.licenses[[2]string{repository, artifactDigest}])
	sort.Slice(all, func(i, j int) bool {
		if all[i].SPDXID != all[j].SPDXID {
			return all[i].SPDXID < all[j].SPDXID
		}
		return all[i].Source < all[j].Source
	})
	out := []ScanLicense{}
	for _, l := range all {
		if after != nil && (l.SPDXID < after.SPDXID || (l.SPDXID == after.SPDXID && l.Source <= after.Source)) {
			continue
		}
		out = append(out, l)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) ListScanHistory(_ context.Context, repositoryID, artifactDigest string, limit int) ([]ScanHistoryEntry, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return nil, err
	}
	if limit, err = scanLimit(limit); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ScanHistoryEntry{}
	for _, h := range s.scan.history {
		if h.RepositoryID == repository && h.ArtifactDigest == artifactDigest {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ScannedAt.After(out[j].ScannedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) RecordScannerDBGeneration(_ context.Context, generation ScannerDBGeneration) (ScannerDBGeneration, error) {
	generation, err := normalizeScannerDBGeneration(generation)
	if err != nil {
		return ScannerDBGeneration{}, err
	}
	key := [2]string{generation.Kind, generation.OCIDigest}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.scan.generations[key]; ok {
		existing.ObservedAt = generation.ObservedAt
		s.scan.generations[key] = existing
		return existing, nil
	}
	s.scan.generations[key] = generation
	return generation, nil
}

func (s *MemoryStore) LatestScannerDBGeneration(_ context.Context, kind string) (ScannerDBGeneration, error) {
	if err := scanOneOf("kind", kind, ScannerDBTrivy, ScannerDBJava, ScannerDBChecks); err != nil {
		return ScannerDBGeneration{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest ScannerDBGeneration
	for key, g := range s.scan.generations {
		if key[0] == kind && (latest.ID == "" || g.BuiltAt.After(latest.BuiltAt) || (g.BuiltAt.Equal(latest.BuiltAt) && g.ID > latest.ID)) {
			latest = g
		}
	}
	if latest.ID == "" {
		return ScannerDBGeneration{}, ErrNotFound
	}
	return latest, nil
}

func (s *MemoryStore) PutVEXDocument(_ context.Context, document VEXDocument) (VEXDocument, error) {
	document, err := normalizeVEXDocument(document)
	if err != nil {
		return VEXDocument{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scan.vex = append(s.scan.vex, document)
	return document, nil
}

func (s *MemoryStore) ListVEXDocuments(_ context.Context, repositoryID string) ([]VEXDocument, error) {
	repository, err := scanUUID("repository id", repositoryID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []VEXDocument{}
	for _, d := range s.scan.vex {
		if d.RepositoryID == repository {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
