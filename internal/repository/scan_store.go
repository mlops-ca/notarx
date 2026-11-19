package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mlops-ca/notarx/internal/ids"
)

// ErrScanInvalid reports arguments the scan data store refuses.
var ErrScanInvalid = errors.New("scan data argument is invalid")

// Scan data model enumerations (ADR 0030 section 2).
const (
	ScanSBOMKindCycloneDX = "cyclonedx-json"
	ScanSBOMKindSPDX      = "spdx-json"

	ScanSBOMSourceGenerated = "generated"
	ScanSBOMSourceUploaded  = "uploaded"
	ScanSBOMSourcePromoted  = "promoted"

	ScanStatusClean      = "clean"
	ScanStatusVulnerable = "vulnerable"
	ScanStatusStaleDB    = "stale_db"
	ScanStatusNotScanned = "not_scanned"
	ScanStatusError      = "error"

	ScanFindingAffected   = "affected"
	ScanFindingSuppressed = "suppressed"
	ScanFindingFixed      = "fixed"

	ScannerDBTrivy  = "trivy-db"
	ScannerDBJava   = "java-db"
	ScannerDBChecks = "checks"

	ScannerDBSourceUpstream = "upstream"
	ScannerDBSourceMirror   = "mirror"
	ScannerDBSourceFeed     = "feed"

	VEXFormatOpenVEX = "openvex"
)

// MaxScanFindingBatch is the largest number of findings one InsertScanFindings call accepts (ADR 0030
// section 2: a scan inserts its findings in batches of 1,000).
const MaxScanFindingBatch = 1000

// ScanSBOM is one SBOM body of an artifact. A body above NOTARX_INLINE_MAX_BYTES lives in the object
// store at BodyKey and Body is empty; a small one is kept inline. BodyDigest and BodySize always describe
// the document as received (the object stored at BodyKey); an inline Body is held as jsonb (JSON on
// MySQL, ADR 0030 section 2), so it is the same document but not necessarily the same bytes. Verify or
// re-serve the exact bytes through BodyKey.
type ScanSBOM struct {
	ID               string
	RepositoryID     string
	ArtifactDigest   string
	Kind             string
	Source           string
	Generator        string
	GeneratorVersion string
	SpecVersion      string
	BodyDigest       string
	BodySize         int64
	BodyKey          string
	Body             []byte
	ComponentCount   int
	CreatedAt        time.Time
}

// ScanResult is the current vulnerability result of an artifact. ID is the vulnerability_id its
// findings are stored under.
type ScanResult struct {
	ID               string
	RepositoryID     string
	ArtifactDigest   string
	Scanner          string
	ScannerVersion   string
	DBGenerationID   string
	Status           string
	Critical         int
	High             int
	Medium           int
	Low              int
	Unknown          int
	SuppressedCount  int
	FindingsTotal    int
	Truncated        bool
	NotScannedReason string
	ScannedAt        time.Time
}

// ScanFinding is one finding. SeverityRank is derived from Severity by the store (0 is the worst).
type ScanFinding struct {
	VulnerabilityID  string
	Ordinal          int
	VulnID           string
	Source           string
	Severity         string
	SeverityRank     int
	Component        string
	Version          string
	FixedVersion     string
	Location         string
	Title            string
	Description      string
	URL              string
	CVSSScore        *float64
	CVSSVector       string
	Status           string
	VEXStatus        string
	VEXJustification string
	EPSS             *float64
	KEV              *bool
}

// ScanFindingCursor is the keyset position after a finding of ListScanFindings.
type ScanFindingCursor struct {
	SeverityRank int
	VulnID       string
	Ordinal      int
}

// Cursor returns the position of f.
func (f ScanFinding) Cursor() ScanFindingCursor {
	return ScanFindingCursor{SeverityRank: f.SeverityRank, VulnID: f.VulnID, Ordinal: f.Ordinal}
}

// ScanLicense is one detected licence of an artifact.
type ScanLicense struct {
	RepositoryID   string
	ArtifactDigest string
	SPDXID         string
	Source         string
	Confidence     float64
}

// ScanLicenseCursor is the keyset position after a licence of ListScanLicenses.
type ScanLicenseCursor struct {
	SPDXID string
	Source string
}

// ScanHistoryEntry is one append-only history row.
type ScanHistoryEntry struct {
	ID             string
	RepositoryID   string
	ArtifactDigest string
	ScannerVersion string
	DBGenerationID string
	Status         string
	Critical       int
	High           int
	Medium         int
	Low            int
	Unknown        int
	ScannedAt      time.Time
}

// ScannerDBGeneration is one observed generation of a scanner database.
type ScannerDBGeneration struct {
	ID         string
	Kind       string
	OCIDigest  string
	BuiltAt    time.Time
	ObservedAt time.Time
	Source     string
}

// VEXDocument is a stored VEX document of a repository; the body lives in the object store.
type VEXDocument struct {
	ID           string
	RepositoryID string
	Format       string
	BodyDigest   string
	BodyKey      string
	Author       string
	CreatedAt    time.Time
	CreatedBy    string
}

// ScanSwap is the outcome of SwapScanResult.
type ScanSwap struct {
	// PreviousID is the vulnerability_id that was replaced, empty on the first scan. Its findings are
	// removed with SweepScanFindings.
	PreviousID string
	// HistoryAppended reports whether a history row was written.
	HistoryAppended bool
	// Superseded reports that the current result is newer than the one offered (an older, slower scan
	// finished last); nothing was changed and the offered findings are left to the abandoned-id sweep.
	Superseded bool
}

// ScanStore persists the SBOM and vulnerability data of ADR 0030 section 2.
type ScanStore interface {
	// PutScanSBOM stores the SBOM, replacing the row with the same (repository, digest, kind, source),
	// and returns the stored row. The id and created_at of an existing row are kept.
	PutScanSBOM(ctx context.Context, sbom ScanSBOM) (ScanSBOM, error)
	GetScanSBOM(ctx context.Context, repositoryID, artifactDigest, kind, source string) (ScanSBOM, error)
	ListScanSBOMs(ctx context.Context, repositoryID, artifactDigest string) ([]ScanSBOM, error)

	// InsertScanFindings stores at most MaxScanFindingBatch findings under their vulnerability id. It is
	// not visible through GetScanResult until SwapScanResult points the artifact at that id.
	InsertScanFindings(ctx context.Context, findings []ScanFinding) error
	// SwapScanResult makes result the current result of its artifact in one short transaction and
	// appends a history row on the first scan or when the status or a severity count changed.
	SwapScanResult(ctx context.Context, result ScanResult) (ScanSwap, error)
	GetScanResult(ctx context.Context, repositoryID, artifactDigest string) (ScanResult, error)
	// ListScanFindings pages the findings of a vulnerability id, worst severity first, after cursor.
	ListScanFindings(ctx context.Context, vulnerabilityID string, after *ScanFindingCursor, limit int) ([]ScanFinding, error)
	// ListAbandonedScanVulnerabilityIDs returns up to limit vulnerability ids that hold findings, are not the
	// current result of any artifact and were created before olderThan (ids are UUIDv7, so their age is in
	// the id): findings of replaced results whose sweep did not finish, and of scans that never reached
	// SwapScanResult. Feed each id to SweepScanFindings.
	ListAbandonedScanVulnerabilityIDs(ctx context.Context, olderThan time.Time, limit int) ([]string, error)
	// SweepScanFindings deletes at most limit findings of a replaced vulnerability id and returns how
	// many it deleted; callers repeat until it returns 0. It refuses the id of a current result.
	SweepScanFindings(ctx context.Context, vulnerabilityID string, limit int) (int, error)

	ReplaceScanLicenses(ctx context.Context, repositoryID, artifactDigest string, licenses []ScanLicense) error
	ListScanLicenses(ctx context.Context, repositoryID, artifactDigest string, after *ScanLicenseCursor, limit int) ([]ScanLicense, error)

	// ListScanHistory returns the newest history rows of an artifact first.
	ListScanHistory(ctx context.Context, repositoryID, artifactDigest string, limit int) ([]ScanHistoryEntry, error)

	// RecordScannerDBGeneration stores a generation, or moves observed_at of the known (kind, digest).
	RecordScannerDBGeneration(ctx context.Context, generation ScannerDBGeneration) (ScannerDBGeneration, error)
	// LatestScannerDBGeneration returns the generation of the kind with the newest built_at.
	LatestScannerDBGeneration(ctx context.Context, kind string) (ScannerDBGeneration, error)

	PutVEXDocument(ctx context.Context, document VEXDocument) (VEXDocument, error)
	ListVEXDocuments(ctx context.Context, repositoryID string) ([]VEXDocument, error)
}

var scanSeverityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "unknown": 4}

func scanInvalid(format string, args ...any) error {
	return errors.Join(ErrScanInvalid, fmt.Errorf(format, args...))
}

func scanUUID(name, value string) (string, error) {
	canonical, ok := canonicalUUID(value)
	if !ok {
		return "", scanInvalid("%s must be a UUID", name)
	}
	return canonical, nil
}

func scanOptionalUUID(name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return scanUUID(name, value)
}

// scanBounded counts characters, as the varchar limits of the schema do.
func scanBounded(name, value string, maximum int, required bool) error {
	if required && value == "" {
		return scanInvalid("%s is required", name)
	}
	if utf8.RuneCountInString(value) > maximum {
		return scanInvalid("%s is longer than %d characters", name, maximum)
	}
	return nil
}

func scanFinite(name string, values ...*float64) error {
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return scanInvalid("%s must be a finite number", name)
		}
	}
	return nil
}

// truncateRunes cuts value to at most maximum characters.
func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}

// scanVulnerabilityIDFloor is the smallest UUID whose UUIDv7 timestamp is t: every id created before t sorts
// below it as a uuid and as its canonical string.
func scanVulnerabilityIDFloor(t time.Time) string {
	ms := uint64(max(t.UnixMilli(), 0))
	return fmt.Sprintf("%08x-%04x-0000-0000-000000000000", ms>>16, ms&0xffff)
}

func cloneScanFinding(f ScanFinding) ScanFinding {
	if f.CVSSScore != nil {
		v := *f.CVSSScore
		f.CVSSScore = &v
	}
	if f.EPSS != nil {
		v := *f.EPSS
		f.EPSS = &v
	}
	if f.KEV != nil {
		v := *f.KEV
		f.KEV = &v
	}
	return f
}

func scanOneOf(name, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return scanInvalid("%s must be one of %s", name, strings.Join(allowed, ", "))
}

func scanLimit(limit int) (int, error) {
	if limit < 1 || limit > 10000 {
		return 0, scanInvalid("limit must be between 1 and 10000")
	}
	return limit, nil
}

func scanCounts(values ...int) error {
	for _, value := range values {
		if value < 0 {
			return scanInvalid("counts must not be negative")
		}
	}
	return nil
}

func normalizeScanSBOM(s ScanSBOM) (ScanSBOM, error) {
	var err error
	if s.RepositoryID, err = scanUUID("repository id", s.RepositoryID); err != nil {
		return s, err
	}
	for _, check := range []error{
		scanBounded("artifact digest", s.ArtifactDigest, 80, true),
		scanOneOf("kind", s.Kind, ScanSBOMKindCycloneDX, ScanSBOMKindSPDX),
		scanOneOf("source", s.Source, ScanSBOMSourceGenerated, ScanSBOMSourceUploaded, ScanSBOMSourcePromoted),
		scanBounded("generator", s.Generator, 64, false),
		scanBounded("generator version", s.GeneratorVersion, 32, false),
		scanBounded("spec version", s.SpecVersion, 16, false),
		scanBounded("body digest", s.BodyDigest, 80, true),
		scanCounts(s.ComponentCount),
	} {
		if check != nil {
			return s, check
		}
	}
	if s.BodySize < 0 {
		return s, scanInvalid("body size must not be negative")
	}
	if len(s.Body) == 0 && s.BodyKey == "" {
		return s, scanInvalid("an SBOM needs an inline body or an object store key")
	}
	if len(s.Body) > 0 && (!json.Valid(s.Body) || bytes.Contains(s.Body, []byte(`\u0000`))) {
		return s, scanInvalid("an inline SBOM body must be valid JSON without NUL escapes")
	}
	if s.ID == "" {
		s.ID = ids.New()
	} else if s.ID, err = scanUUID("id", s.ID); err != nil {
		return s, err
	}
	return s, nil
}

func normalizeScanResult(r ScanResult) (ScanResult, error) {
	var err error
	if r.ID, err = scanUUID("vulnerability id", r.ID); err != nil {
		return r, err
	}
	if r.RepositoryID, err = scanUUID("repository id", r.RepositoryID); err != nil {
		return r, err
	}
	if r.DBGenerationID, err = scanOptionalUUID("db generation id", r.DBGenerationID); err != nil {
		return r, err
	}
	for _, check := range []error{
		scanBounded("artifact digest", r.ArtifactDigest, 80, true),
		scanBounded("scanner", r.Scanner, 32, true),
		scanBounded("scanner version", r.ScannerVersion, 32, false),
		scanOneOf("status", r.Status, ScanStatusClean, ScanStatusVulnerable, ScanStatusStaleDB, ScanStatusNotScanned, ScanStatusError),
		scanCounts(r.Critical, r.High, r.Medium, r.Low, r.Unknown, r.SuppressedCount, r.FindingsTotal),
	} {
		if check != nil {
			return r, check
		}
	}
	if r.ScannedAt.IsZero() {
		r.ScannedAt = time.Now()
	}
	r.ScannedAt = r.ScannedAt.UTC().Truncate(time.Microsecond)
	return r, nil
}

func normalizeScanFinding(f ScanFinding) (ScanFinding, error) {
	var err error
	if f.VulnerabilityID, err = scanUUID("vulnerability id", f.VulnerabilityID); err != nil {
		return f, err
	}
	rank, ok := scanSeverityRank[f.Severity]
	if !ok {
		return f, scanInvalid("severity %q is not known", f.Severity)
	}
	f.SeverityRank = rank
	f.Description = truncateRunes(f.Description, 4096)
	for _, check := range []error{
		scanFinite("cvss score and epss", f.CVSSScore, f.EPSS),
		scanBounded("vuln id", f.VulnID, 128, true),
		scanBounded("source", f.Source, 32, false),
		scanBounded("vex status", f.VEXStatus, 32, false),
		scanOneOf("status", f.Status, ScanFindingAffected, ScanFindingSuppressed, ScanFindingFixed),
		scanCounts(f.Ordinal),
	} {
		if check != nil {
			return f, check
		}
	}
	return f, nil
}

func normalizeScanDigest(repositoryID, artifactDigest string) (string, error) {
	canonical, err := scanUUID("repository id", repositoryID)
	if err != nil {
		return "", err
	}
	return canonical, scanBounded("artifact digest", artifactDigest, 80, true)
}

func normalizeScanLicenses(repositoryID, artifactDigest string, licenses []ScanLicense) (string, []ScanLicense, error) {
	repository, err := normalizeScanDigest(repositoryID, artifactDigest)
	if err != nil {
		return "", nil, err
	}
	seen := make(map[[2]string]bool, len(licenses))
	unique := make([]ScanLicense, 0, len(licenses))
	for _, l := range licenses {
		if err := errors.Join(scanBounded("spdx id", l.SPDXID, 128, true), scanBounded("source", l.Source, 32, true)); err != nil {
			return "", nil, err
		}
		if math.IsNaN(l.Confidence) || l.Confidence < 0 || l.Confidence > 1 {
			return "", nil, scanInvalid("confidence must be between 0 and 1")
		}
		key := [2]string{l.SPDXID, l.Source}
		if seen[key] {
			continue
		}
		seen[key] = true
		l.RepositoryID, l.ArtifactDigest = repository, artifactDigest
		unique = append(unique, l)
	}
	return repository, unique, nil
}

func normalizeScannerDBGeneration(g ScannerDBGeneration) (ScannerDBGeneration, error) {
	for _, check := range []error{
		scanOneOf("kind", g.Kind, ScannerDBTrivy, ScannerDBJava, ScannerDBChecks),
		scanOneOf("source", g.Source, ScannerDBSourceUpstream, ScannerDBSourceMirror, ScannerDBSourceFeed),
		scanBounded("oci digest", g.OCIDigest, 80, true),
	} {
		if check != nil {
			return g, check
		}
	}
	var err error
	if g.ID == "" {
		g.ID = ids.New()
	} else if g.ID, err = scanUUID("id", g.ID); err != nil {
		return g, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if g.ObservedAt.IsZero() {
		g.ObservedAt = now
	}
	if g.BuiltAt.IsZero() {
		g.BuiltAt = g.ObservedAt
	}
	g.BuiltAt, g.ObservedAt = g.BuiltAt.UTC().Truncate(time.Microsecond), g.ObservedAt.UTC().Truncate(time.Microsecond)
	return g, nil
}

func normalizeVEXDocument(d VEXDocument) (VEXDocument, error) {
	var err error
	if d.RepositoryID, err = scanUUID("repository id", d.RepositoryID); err != nil {
		return d, err
	}
	if d.CreatedBy, err = scanOptionalUUID("created by", d.CreatedBy); err != nil {
		return d, err
	}
	for _, check := range []error{
		scanOneOf("format", d.Format, VEXFormatOpenVEX),
		scanBounded("body digest", d.BodyDigest, 80, true),
		scanBounded("body key", d.BodyKey, 1024, true),
	} {
		if check != nil {
			return d, check
		}
	}
	if d.ID == "" {
		d.ID = ids.New()
	} else if d.ID, err = scanUUID("id", d.ID); err != nil {
		return d, err
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	d.CreatedAt = d.CreatedAt.UTC().Truncate(time.Microsecond)
	return d, nil
}

// scanCountsChanged reports whether a history row is due for next given the previous current result.
func scanCountsChanged(previous, next ScanResult) bool {
	return previous.Status != next.Status || previous.Critical != next.Critical || previous.High != next.High ||
		previous.Medium != next.Medium || previous.Low != next.Low || previous.Unknown != next.Unknown
}

func scanHistoryEntry(r ScanResult, id string) ScanHistoryEntry {
	return ScanHistoryEntry{ID: id, RepositoryID: r.RepositoryID, ArtifactDigest: r.ArtifactDigest, ScannerVersion: r.ScannerVersion,
		DBGenerationID: r.DBGenerationID, Status: r.Status, Critical: r.Critical, High: r.High, Medium: r.Medium, Low: r.Low,
		Unknown: r.Unknown, ScannedAt: r.ScannedAt}
}
