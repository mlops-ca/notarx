package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mlops-ca/notarx/internal/rawversion"
)

// RawLayout selects how a hosted Raw repository interprets its paths (ADR 0028).
type RawLayout string

const (
	// RawLayoutPath is the default: a path is a mutable pointer to a digest.
	RawLayoutPath RawLayout = "path"
	// RawLayoutVersioned enforces <name>/<version>/<file>, create-only files and
	// sealable versions.
	RawLayoutVersioned RawLayout = "versioned"
)

// Valid reports whether the layout is one of the two documented values.
func (l RawLayout) Valid() bool { return l == RawLayoutPath || l == RawLayoutVersioned }

// RawSealMode selects how versions of a versioned repository are sealed.
type RawSealMode string

const (
	// RawSealExplicit seals only on an explicit seal request.
	RawSealExplicit RawSealMode = "explicit"
	// RawSealIdle also seals an open version that saw no write for the idle period.
	RawSealIdle RawSealMode = "idle"
)

// Valid reports whether the mode is one of the two documented values.
func (m RawSealMode) Valid() bool { return m == RawSealExplicit || m == RawSealIdle }

// RawVersionState is the lifecycle state of a version: open, sealed, tombstoned.
type RawVersionState string

const (
	RawVersionOpen       RawVersionState = "open"
	RawVersionSealed     RawVersionState = "sealed"
	RawVersionTombstoned RawVersionState = "tombstoned"
)

// MaxRawFilesPerVersion is the largest files-per-version cap a deployment can configure; the file read of a version, and
// the size of a seal request, are bounded by it.
const MaxRawFilesPerVersion = rawversion.MaxFilesPerVersion

// DefaultRawMaxFilesPerVersion is the files-per-version cap when the store is not configured (ADR 0028 configuration).
const DefaultRawMaxFilesPerVersion = 1000

// Limits of the per-version property map (ADR 0028, Properties).
const (
	RawVersionMaxProperties     = rawversion.MaxProperties
	RawVersionMaxPropertyBytes  = 8 << 10
	rawVersionMaxPropertyValue  = 256
	rawVersionManifestDigestTag = "sha256:"
)

var (
	// ErrInvalidGenericPath reports a path that is not name/version/file in a versioned repository.
	ErrInvalidGenericPath = errors.New("path is not a valid name/version/file path")
	// ErrVersionFileExists reports a create-only write of an existing file with a different digest.
	ErrVersionFileExists = errors.New("version file already exists with different content")
	// ErrVersionSealed reports a change to a sealed version.
	ErrVersionSealed = errors.New("version is sealed")
	// ErrVersionTombstoned reports a write to a tombstoned version, whose identifier stays burned.
	ErrVersionTombstoned = errors.New("version is tombstoned")
	// ErrVersionFileImmutable reports a file delete in a strict repository.
	ErrVersionFileImmutable = errors.New("file of a strict repository cannot be deleted")
	// ErrVersionFileLimit reports a write beyond the files-per-version cap.
	ErrVersionFileLimit = errors.New("version file count limit reached")
	// ErrVersionTooLarge reports a version with more files than MaxRawFilesPerVersion, whose listing would be truncated.
	ErrVersionTooLarge = errors.New("version has more files than can be listed")
	// ErrVersionEmpty reports a seal of a version without files.
	ErrVersionEmpty = errors.New("version has no files")
	// ErrChecksumMismatch reports an expected file list or checksums file that disagrees with the stored files.
	ErrChecksumMismatch = errors.New("version files do not match the expected checksums")
	// ErrChecksumsChanged reports that the checksums file named at seal changed after the caller read it.
	ErrChecksumsChanged = errors.New("checksums file changed during seal")
	// ErrInvalidVersionProperties reports a property map outside its documented bounds.
	ErrInvalidVersionProperties = errors.New("version properties are invalid")
	// ErrLayoutImmutable reports an attempt to change the layout, seal mode or strict flag of a repository.
	ErrLayoutImmutable = errors.New("repository layout is immutable")
	// ErrRawLayoutMismatch reports an operation that needs the other Raw layout.
	ErrRawLayoutMismatch = errors.New("operation does not apply to this Raw layout")
)

// isHostedRaw reports whether repo is a hosted Raw repository; an empty type is hosted, as at creation.
func isHostedRaw(repo HostedRepository) bool {
	return repo.Format == FormatRaw && (repo.Type == "" || repo.Type == RepositoryTypeHosted)
}

// normalizeRawLayout gives hosted Raw repositories their layout defaults and clears the fields on every other
// repository, so the response of a Maven or proxy repository never carries them.
func normalizeRawLayout(repo HostedRepository) HostedRepository {
	if !isHostedRaw(repo) {
		repo.RawLayout, repo.SealMode, repo.RawStrict = "", "", false
		return repo
	}
	if repo.RawLayout == "" {
		repo.RawLayout = RawLayoutPath
	}
	if repo.SealMode == "" {
		repo.SealMode = RawSealExplicit
	}
	return repo
}

// ValidateRawLayout enforces the shape of ADR 0028 on a repository about to be created: only hosted Raw repositories
// carry a layout, and the idle seal mode and the strict flag need the versioned layout. It is the Go twin of the
// hosted_repositories_raw_layout_shape_check constraint, which the memory store lacks.
func ValidateRawLayout(repo HostedRepository) error {
	if !isHostedRaw(repo) {
		if repo.RawLayout != "" || repo.SealMode != "" || repo.RawStrict {
			return ErrRawLayoutMismatch
		}
		return nil
	}
	repo = normalizeRawLayout(repo)
	if !repo.RawLayout.Valid() || !repo.SealMode.Valid() || (repo.RawLayout == RawLayoutPath && (repo.SealMode != RawSealExplicit || repo.RawStrict)) {
		return ErrRawLayoutMismatch
	}
	return nil
}

// rawLayoutColumns returns the values of the raw_layout, seal_mode and raw_strict columns of a repository row;
// repositories that are not hosted Raw store the defaults.
func rawLayoutColumns(repo HostedRepository) (layout, sealMode string, strict bool) {
	if !isHostedRaw(repo) {
		return string(RawLayoutPath), string(RawSealExplicit), false
	}
	repo = normalizeRawLayout(repo)
	return string(repo.RawLayout), string(repo.SealMode), repo.RawStrict
}

// ValidRawVersionName reports whether name is a valid package name of a versioned repository.
func ValidRawVersionName(name string) bool { return rawversion.ValidName(name) }

// ValidRawVersionString reports whether version is a valid version identifier; semver is not required.
func ValidRawVersionString(version string) bool { return rawversion.ValidVersion(version) }

type writeActorKey struct{}

// WithWriteActor records the acting principal on ctx. A first write to a versioned Raw repository creates the open
// version row, and the row keeps the actor as created_by, and the artifact.published event of every format carries
// it as payload.actor. The write methods have no other way to name it without writing an audit row for every write.
func WithWriteActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, writeActorKey{}, actor)
}

// publishActor is the principal WithWriteActor recorded on ctx, empty when none.
func publishActor(ctx context.Context) string {
	actor, _ := ctx.Value(writeActorKey{}).(string)
	return actor
}

// RawVersionPath is a validated <name>/<version>/<file> path.
type RawVersionPath struct{ Name, Version, File string }

// Prefix is the path prefix of the version's files, including the trailing slash.
func (p RawVersionPath) Prefix() string { return p.Name + "/" + p.Version + "/" }

// ParseRawVersionPath splits and validates a Raw path of a versioned repository. The caller has already applied
// v2contract.CanonicalRawPath, so the segments are canonical; this adds the ASCII rules of ADR 0028.
func ParseRawVersionPath(path string) (RawVersionPath, error) {
	if len(path) > rawversion.MaxPathBytes {
		return RawVersionPath{}, ErrInvalidGenericPath
	}
	segments := strings.Split(path, "/")
	if len(segments) < rawversion.MinPathSegments || len(segments) > 2+rawversion.MaxFileSegments {
		return RawVersionPath{}, ErrInvalidGenericPath
	}
	parsed := RawVersionPath{Name: segments[0], Version: segments[1], File: strings.Join(segments[2:], "/")}
	if !ValidRawVersionName(parsed.Name) || !ValidRawVersionString(parsed.Version) || segments[2] == rawversion.ReservedSegment {
		return RawVersionPath{}, ErrInvalidGenericPath
	}
	// The guard also serves writers that never passed the HTTP path canonicalisation (replication, promotion,
	// migration), so the file segments are checked here too.
	for _, segment := range segments[2:] {
		if !rawversion.ValidFileSegment(segment) {
			return RawVersionPath{}, ErrInvalidGenericPath
		}
	}
	return parsed, nil
}

// RawVersionPathRange returns the half-open byte range [from, to) that holds exactly the files of a version:
// "/" + 1 is "0", so "name/version0" is the first path after every "name/version/..." path.
func RawVersionPathRange(name, version string) (from, to string) {
	return name + "/" + version + "/", name + "/" + version + "0"
}

// RawVersion is a version of a versioned Raw repository.
type RawVersion struct {
	ID             string
	RepositoryID   string
	Name           string
	Version        string
	State          RawVersionState
	FileCount      int
	SizeBytes      int64
	ManifestDigest string
	CreatedBy      string
	CreatedAt      time.Time
	// SealedAt is zero until the version is sealed.
	SealedAt time.Time
	// UpdatedAt is the time of the last write to the version (a file, its properties, its seal).
	UpdatedAt  time.Time
	Properties map[string]string
}

// Coordinate is the governance coordinate of the version: name@version.
func (v RawVersion) Coordinate() string { return v.Name + "@" + v.Version }

// RawPackage summarises the versions of one package name in a repository.
type RawPackage struct {
	Name string
	// LatestVersion is the newest sealed version by seal time; empty while none is sealed.
	LatestVersion string
	VersionCount  int
}

// RawVersionProperty is one key/value filter of a version list.
type RawVersionProperty struct{ Key, Value string }

// RawVersionCursor is the keyset position of a version listing: newest seal first, unsealed versions last.
type RawVersionCursor struct {
	// SealedAt is zero for an unsealed version.
	SealedAt time.Time
	ID       string
}

// RawVersionQuery selects versions of one package. Tombstoned versions are never listed.
type RawVersionQuery struct {
	RepositoryID string
	Name         string
	// State filters to open or sealed versions; empty lists both.
	State      RawVersionState
	Properties []RawVersionProperty
	// OpenCreatedBefore, when set, keeps only open versions created before it (stale open versions).
	OpenCreatedBefore time.Time
	After             *RawVersionCursor
	Limit             int
}

// RawVersionFileRef names one expected file of a seal request: Path is relative to the version, SHA256 is hex.
type RawVersionFileRef struct{ Path, SHA256 string }

// SealRawVersionInput is a seal request. Expected, ChecksumsFile and Properties are optional additions to the
// checks the store always makes.
type SealRawVersionInput struct {
	RepositoryID, Name, Version string
	Actor                       string
	// Expected, when non-nil, must equal the stored file set exactly.
	Expected []RawVersionFileRef
	// ChecksumsFile names a stored file (relative path) parsed as sha256sum output by the caller. ChecksumsDigest
	// is the digest the caller read, Checksums its entries (relative path to hex sha256); the store verifies,
	// under the version lock, that the file still has that digest and that every entry matches a stored file.
	ChecksumsFile   string
	ChecksumsDigest string
	Checksums       map[string]string
	// Properties, when non-nil, replaces the property map in the same transaction before it is frozen.
	Properties map[string]string
	// Audit is the audit template; the store records it with the manifest digest as evidence.
	Audit *AuditRecord
}

// RawOpenVersionStats reports open versions for the stale-open metrics (ADR 0028, slice 5).
type RawOpenVersionStats struct {
	// Stale counts open versions created before the threshold.
	Stale int64
	// OldestCreatedAt is the creation time of the oldest open version; zero when none is open.
	OldestCreatedAt time.Time
}

// NativeRawVersionStore is the version surface of a versioned Raw repository (ADR 0028). File writes and deletes of
// such a repository go through the ordinary Raw store methods, whose store-level guard keeps the version rows
// consistent; these methods read versions and change their state.
type NativeRawVersionStore interface {
	GetRawVersion(ctx context.Context, repositoryID, name, version string) (RawVersion, error)
	// GetRawVersionWithFiles reads a version and its files from one consistent snapshot, so state, counters and the
	// file list always agree.
	GetRawVersionWithFiles(ctx context.Context, repositoryID, name, version string) (RawVersion, []RawAsset, error)
	ListRawPackages(ctx context.Context, repositoryID, after string, limit int) ([]RawPackage, error)
	ListRawVersions(ctx context.Context, query RawVersionQuery) ([]RawVersion, error)
	ListRawVersionFiles(ctx context.Context, repositoryID, name, version string) ([]RawAsset, error)
	// CreateRawVersion creates an open version, or updates the properties of an existing open one. The result
	// reports whether the version was created by this call.
	CreateRawVersion(ctx context.Context, version RawVersion, audit ...AuditRecord) (RawVersion, bool, error)
	SetRawVersionProperties(ctx context.Context, repositoryID, name, version string, properties map[string]string, audit ...AuditRecord) (RawVersion, error)
	SealRawVersion(ctx context.Context, input SealRawVersionInput) (RawVersion, error)
	// SealIdleRawVersions seals open versions of repositories in idle seal mode whose last write is before idleBefore.
	SealIdleRawVersions(ctx context.Context, idleBefore time.Time, limit int) ([]RawVersion, error)
	RawOpenVersionStats(ctx context.Context, staleBefore time.Time) (RawOpenVersionStats, error)
}

// ValidateRawVersionProperties applies the bounds of ADR 0028: at most 32 keys, a lower-case key pattern, values of
// at most 256 bytes of UTF-8 without control or bidirectional characters, 8 KiB in total.
func ValidateRawVersionProperties(properties map[string]string) error {
	if len(properties) > RawVersionMaxProperties {
		return ErrInvalidVersionProperties
	}
	total := 0
	for key, value := range properties {
		if !rawversion.ValidPropertyKey(key) || len(value) > rawVersionMaxPropertyValue || !utf8.ValidString(value) || strings.IndexFunc(value, rawversion.InvalidRune) >= 0 {
			return ErrInvalidVersionProperties
		}
		total += len(key) + len(value)
	}
	if total > RawVersionMaxPropertyBytes {
		return ErrInvalidVersionProperties
	}
	return nil
}

// RawVersionManifest builds the canonical manifest of a version and its digest (ADR 0028, Manifest). files are the
// version's asset rows; their paths are made relative to the version. Keys are sorted, there is no insignificant
// whitespace and the encoding is UTF-8, so the digest is a function of the content alone.
func RawVersionManifest(name, version string, files []RawAsset, properties map[string]string) (manifest []byte, digest string) {
	prefix, _ := RawVersionPathRange(name, version)
	type entry struct {
		path, sha256 string
		size         int64
	}
	entries := make([]entry, 0, len(files))
	for _, file := range files {
		entries = append(entries, entry{path: strings.TrimPrefix(file.Path, prefix), sha256: strings.TrimPrefix(file.Digest, rawVersionManifestDigestTag), size: file.Size})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var out bytes.Buffer
	out.WriteString(`{"files":[`)
	for i, e := range entries {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"path":`)
		out.Write(rawVersionJSONString(e.path))
		out.WriteString(`,"sha256":`)
		out.Write(rawVersionJSONString(e.sha256))
		out.WriteString(`,"size":`)
		sizeJSON, _ := json.Marshal(e.size)
		out.Write(sizeJSON)
		out.WriteByte('}')
	}
	out.WriteString(`],"name":`)
	out.Write(rawVersionJSONString(name))
	out.WriteString(`,"properties":{`)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(rawVersionJSONString(key))
		out.WriteByte(':')
		out.Write(rawVersionJSONString(properties[key]))
	}
	out.WriteString(`},"version":`)
	out.Write(rawVersionJSONString(version))
	out.WriteByte('}')
	sum := sha256.Sum256(out.Bytes())
	return out.Bytes(), rawVersionManifestDigestTag + hex.EncodeToString(sum[:])
}

// rawVersionJSONString encodes s as a JSON string without HTML escaping.
func rawVersionJSONString(s string) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// RawVersionChecksums renders the sha256sum -c compatible text of a version: "<hex>  <path>", sorted by path.
func RawVersionChecksums(name, version string, files []RawAsset) string {
	prefix, _ := RawVersionPathRange(name, version)
	sorted := append([]RawAsset(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var out strings.Builder
	for _, file := range sorted {
		out.WriteString(strings.TrimPrefix(file.Digest, rawVersionManifestDigestTag))
		out.WriteString("  ")
		out.WriteString(strings.TrimPrefix(file.Path, prefix))
		out.WriteByte('\n')
	}
	return out.String()
}

// verifyRawVersionSeal checks a seal request against the stored files of the version. files are the version's
// asset rows (full paths).
func verifyRawVersionSeal(input SealRawVersionInput, files []RawAsset) error {
	prefix, _ := RawVersionPathRange(input.Name, input.Version)
	byPath := make(map[string]string, len(files))
	for _, file := range files {
		byPath[strings.TrimPrefix(file.Path, prefix)] = strings.TrimPrefix(file.Digest, rawVersionManifestDigestTag)
	}
	if input.Expected != nil {
		if len(input.Expected) != len(byPath) {
			return ErrChecksumMismatch
		}
		seen := make(map[string]bool, len(input.Expected))
		for _, expected := range input.Expected {
			digest, ok := byPath[expected.Path]
			if !ok || seen[expected.Path] || !strings.EqualFold(digest, expected.SHA256) {
				return ErrChecksumMismatch
			}
			seen[expected.Path] = true
		}
	}
	if input.ChecksumsFile != "" {
		digest, ok := byPath[input.ChecksumsFile]
		if !ok || !strings.EqualFold(digest, strings.TrimPrefix(input.ChecksumsDigest, rawVersionManifestDigestTag)) {
			return ErrChecksumsChanged
		}
		for path, want := range input.Checksums {
			if have, ok := byPath[path]; !ok || !strings.EqualFold(have, want) {
				return ErrChecksumMismatch
			}
		}
	}
	return nil
}

// RawVersionAuditRecord builds the audit record of a version transition: security evidence, so it is class sync.
func RawVersionAuditRecord(repositoryName, actor, operation, coordinate string) AuditRecord {
	return AuditRecord{Class: AuditClassSync, Repository: repositoryName, Outcome: AuditResolved, Actor: actor, OccurredAt: time.Now().UTC(), Format: string(FormatRaw), Resource: coordinate, Operation: operation, Status: 200, CacheDisposition: "bypass"}
}

// rawVersionIdleSealAudit is the audit template of the idle sweep; the store fills the coordinate.
func rawVersionIdleSealAudit(repositoryName string) *AuditRecord {
	record := RawVersionAuditRecord(repositoryName, rawVersionSweepActor, "raw.version.seal", "")
	return &record
}

// rawVersionSweepActor is the actor recorded for versions the idle sweep seals.
const rawVersionSweepActor = "system:raw-idle-seal"

// rawVersionAudit returns the audit record of a version transition with the manifest digest as evidence.
func rawVersionAudit(template *AuditRecord, version RawVersion) []AuditRecord {
	if template == nil {
		return nil
	}
	record := *template
	record.Resource = version.Coordinate()
	record.Evidence = map[string]string{"coordinate": version.Coordinate()}
	if version.ManifestDigest != "" {
		record.Evidence["manifestDigest"] = version.ManifestDigest
	}
	return []AuditRecord{record}
}

func cloneRawVersionProperties(properties map[string]string) map[string]string {
	cloned := make(map[string]string, len(properties))
	for key, value := range properties {
		cloned[key] = value
	}
	return cloned
}

func equalRawVersionProperties(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
