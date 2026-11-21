package repository

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// ImportSessionState is the lifecycle of one import session (ADR 0021, Remote targets). A session is open while blobs are
// uploaded, committing while the format publishes it, then committed; aborted and expired are the other final states.
type ImportSessionState string

const (
	ImportSessionOpen       ImportSessionState = "open"
	ImportSessionCommitting ImportSessionState = "committing"
	ImportSessionCommitted  ImportSessionState = "committed"
	ImportSessionAborted    ImportSessionState = "aborted"
	ImportSessionExpired    ImportSessionState = "expired"
)

// Live reports whether the session still holds staging capacity: it counts against the limits and the reaper leaves it
// alone until it expires.
func (s ImportSessionState) Live() bool {
	return s == ImportSessionOpen || s == ImportSessionCommitting
}

// Typed refusals of an import session, so the endpoint can answer each with its own status and code.
var (
	ErrImportBlobTooLarge      = errors.New("import blob exceeds the maximum blob size")
	ErrImportSessionTooLarge   = errors.New("import session exceeds the maximum session size")
	ErrImportTooManyBlobs      = errors.New("import session exceeds the maximum number of blobs")
	ErrImportTooManySessions   = errors.New("principal has too many open import sessions")
	ErrImportStagingQuota      = errors.New("import staging quota is exhausted")
	ErrImportSessionClosed     = errors.New("import session is not open")
	ErrImportBlobSizeMismatch  = errors.New("import blob size differs from the size claimed")
	ErrImportBlobsMissing      = errors.New("import session still has blobs to upload")
	ErrImportInvalidBlobClaims = errors.New("import blob claims are invalid")
)

// ImportLimits are the limits a session is created under (NOTARX_IMPORT_*, ADR 0021 "Configuration"). A zero field is
// not enforced.
type ImportLimits struct {
	MaxBlobBytes, MaxSessionBytes int64
	MaxBlobsPerSession            int
	MaxOpenSessionsPerPrincipal   int
	// StagingQuotaBytes caps the claimed bytes of every live session of the instance together.
	StagingQuotaBytes int64
}

// ImportSession is the durable state of one push into a repository. Everything the sender says (coordinate, digest,
// manifest, chain path, evidence) is a claim recorded as received; the importer verifies it against the bytes.
type ImportSession struct {
	ID, TargetRepositoryID string
	// Principal is the actor that created the session; the open-session limit is per principal.
	Principal      string
	Format         Format
	Coordinate     string
	Digest         string
	FormatManifest json.RawMessage
	IdempotencyKey string
	ChainPath      []PromotionChainHop
	// Evidence holds the sender's unverified claims, a JSON object, or nil.
	Evidence json.RawMessage
	State    ImportSessionState
	// Bytes is the total of the claimed blob sizes.
	Bytes                           int64
	CreatedAt, UpdatedAt, ExpiresAt time.Time
}

// ImportBlob is one blob of a session. Size is the claimed size; RecordImportBlob refuses bytes of another size, so a
// verified blob has exactly this size.
type ImportBlob struct {
	SessionID, Digest string
	Size              int64
	// StagingKey names the staging object, assigned when the session is created and emptied once the object is deleted.
	StagingKey string
	Verified   bool
}

// ImportStagingKey is the object key a blob of a session is staged under.
func ImportStagingKey(sessionID, digest string) string {
	return "native/imports/" + sessionID + "/" + digest
}

// ImportSessionStore persists import sessions and their blobs.
type ImportSessionStore interface {
	// CreateImportSession records the session and its claimed blobs under limits, atomically. session needs ID,
	// TargetRepositoryID, Principal, IdempotencyKey, CreatedAt and ExpiresAt; its state is open and its Bytes the total
	// of the blobs, which are deduplicated by digest. A request that repeats the (target, idempotency key) of an existing
	// session with the same principal, format, coordinate and digest returns that session with replayed=true and checks
	// no limit; the same key for anything else is ErrIdempotencyConflict. A refused limit returns its typed error and
	// writes nothing. Live sessions with an ExpiresAt after session.CreatedAt count against the limits.
	CreateImportSession(ctx context.Context, session ImportSession, blobs []ImportBlob, limits ImportLimits) (stored ImportSession, replayed bool, err error)
	// GetImportSession returns ErrNotFound for an unknown id.
	GetImportSession(ctx context.Context, id string) (ImportSession, error)
	// MissingImportBlobs lists the blobs of the session that are not verified yet, by digest.
	MissingImportBlobs(ctx context.Context, sessionID string) ([]ImportBlob, error)
	// RecordImportBlob marks the blob verified once its bytes are staged and hashed. It refuses a session that is not
	// open or has expired (ErrImportSessionClosed), a digest the session did not claim (ErrNotFound) and a size other
	// than the claimed one (ErrImportBlobSizeMismatch); recording a verified blob again changes nothing.
	RecordImportBlob(ctx context.Context, sessionID, digest string, size int64, now time.Time) error
	// BeginImportCommit moves an open session to committing once every blob is verified (ErrImportBlobsMissing
	// otherwise). A session that is already committing stays so; any other state is ErrImportSessionClosed.
	BeginImportCommit(ctx context.Context, sessionID string, now time.Time) error
	// CompleteImportSession moves a committing session to committed (ErrImportSessionClosed otherwise).
	CompleteImportSession(ctx context.Context, sessionID string, now time.Time) error
	// AbortImportSession moves an open or committing session to aborted; an aborted session stays so, a committed or
	// expired one is ErrImportSessionClosed. Its staging objects are removed by the reaper.
	AbortImportSession(ctx context.Context, sessionID string, now time.Time) error
	// ReapImportSessions finishes up to limit sessions that need it: a live session past its expiry becomes expired, and
	// the staging keys of every expired, aborted or committed session are handed to remove, then emptied. A session whose
	// remove fails is left as it is for the next pass and its error is returned with the count of the sessions finished.
	// Passes of several nodes do not wait for or repeat one another.
	ReapImportSessions(ctx context.Context, now time.Time, limit int, remove func(ctx context.Context, keys []string) error) (int, error)
}

// dedupeImportBlobs validates the claims of a session and returns them deduplicated by digest, in digest order, with
// the total bytes. A digest claimed twice with different sizes is invalid, as is an empty digest or a negative size.
func dedupeImportBlobs(blobs []ImportBlob) ([]ImportBlob, int64, error) {
	sizes := make(map[string]int64, len(blobs))
	var total int64
	for _, blob := range blobs {
		if blob.Digest == "" || blob.Size < 0 {
			return nil, 0, ErrImportInvalidBlobClaims
		}
		if size, seen := sizes[blob.Digest]; seen {
			if size != blob.Size {
				return nil, 0, ErrImportBlobSizeMismatch
			}
			continue
		}
		sizes[blob.Digest] = blob.Size
		total += blob.Size
	}
	digests := make([]string, 0, len(sizes))
	for digest := range sizes {
		digests = append(digests, digest)
	}
	slices.Sort(digests)
	out := make([]ImportBlob, 0, len(digests))
	for _, digest := range digests {
		out = append(out, ImportBlob{Digest: digest, Size: sizes[digest]})
	}
	return out, total, nil
}

// checkImportLimits is the per-session part of the limit check, shared by both stores: blob size, blob count and
// session bytes. The open-session and staging-quota limits depend on the other sessions and are checked by the store.
func checkImportLimits(blobs []ImportBlob, total int64, limits ImportLimits) error {
	if limits.MaxBlobsPerSession > 0 && len(blobs) > limits.MaxBlobsPerSession {
		return ErrImportTooManyBlobs
	}
	if limits.MaxBlobBytes > 0 {
		for _, blob := range blobs {
			if blob.Size > limits.MaxBlobBytes {
				return ErrImportBlobTooLarge
			}
		}
	}
	if limits.MaxSessionBytes > 0 && total > limits.MaxSessionBytes {
		return ErrImportSessionTooLarge
	}
	return nil
}

// equivalentImportRequest reports whether a stored session is the session a request describes: a replay.
func equivalentImportRequest(stored, request ImportSession) bool {
	return stored.Principal == request.Principal && stored.Format == request.Format && stored.Coordinate == request.Coordinate && stored.Digest == request.Digest
}
