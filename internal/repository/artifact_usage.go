package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ArtifactUsageStat is the lifecycle download usage of one artifact address:
// the (repository, format, resource) tuple that clients resolve. The Gateway
// folds it into a durable aggregate on every successful download, so the
// count survives audit log retention and stays queryable for the artifact's
// whole lifecycle.
type ArtifactUsageStat struct {
	Repository        string
	Format            string
	Resource          string
	DownloadCount     int64
	TotalBytes        int64
	FirstDownloadedAt time.Time
	LastDownloadedAt  time.Time
	LastActor         string
}

// ArtifactUsageTotals summarizes one repository's usage aggregates.
type ArtifactUsageTotals struct {
	DownloadCount int64 `json:"downloadCount"`
	TotalBytes    int64 `json:"totalBytes"`
	Resources     int64 `json:"resources"`
}

// ArtifactUsageStore reads the durable download usage aggregates. Writes
// happen implicitly on RecordAudit so every download path is counted without
// each protocol handler having to opt in.
type ArtifactUsageStore interface {
	QueryArtifactUsage(ctx context.Context, repository string, query ArtifactUsageQuery) (ArtifactUsagePage, error)
	ListArtifactUsage(ctx context.Context, repository string, limit int) ([]ArtifactUsageStat, error)
	// WalkArtifactUsage visits every aggregate for one repository. Retention
	// cannot use the bounded management API list without missing candidates.
	WalkArtifactUsage(ctx context.Context, repository string, visit func(ArtifactUsageStat) error) error
	ArtifactUsageTotals(ctx context.Context, repository string) (ArtifactUsageTotals, error)
}

// ArtifactUsageQuery selects one live page. Ordering uses the immutable address
// and format, so download increments cannot move existing rows between pages.
type ArtifactUsageQuery struct {
	Limit int
	Query string
	// PageToken continues a listing from the NextPageToken of the previous page (keyset on resource, format).
	PageToken string
}

func (q ArtifactUsageQuery) Validate(repository string) error {
	if repository == "" || q.Limit < 1 || q.Limit > 500 {
		return fmt.Errorf("repository and limit (1-500) are required")
	}
	if !utf8.ValidString(q.Query) || utf8.RuneCountInString(q.Query) > 512 || strings.ContainsRune(q.Query, 0) {
		return fmt.Errorf("q must be valid text of at most 512 characters without NUL")
	}
	return nil
}

// filterKey binds a page token to the repository and filter that produced it.
func (q ArtifactUsageQuery) filterKey(repository string) string {
	return "usage\x00" + repository + "\x00" + q.Query
}

// afterAddress decodes the page token into the (resource, format) of the last address already served.
func (q ArtifactUsageQuery) afterAddress(repository string) (resource, format string, present bool, err error) {
	if q.PageToken == "" {
		return "", "", false, nil
	}
	keys, err := DecodeKeysetToken(q.PageToken, q.filterKey(repository), 2)
	if err != nil {
		return "", "", false, err
	}
	return keys[0], keys[1], true, nil
}

func (q ArtifactUsageQuery) pageToken(repository string, last ArtifactUsageStat) string {
	return EncodeKeysetToken(q.filterKey(repository), last.Resource, last.Format)
}

// ArtifactUsagePage keeps the filtered total and whole-repository totals distinct.
// Each store captures all of them under one consistent read snapshot.
type ArtifactUsagePage struct {
	Items []ArtifactUsageStat
	// Total counts the addresses matching the query: exact up to ListTotalCap, then a lower bound.
	Total  ListTotal
	Totals ArtifactUsageTotals
	// NextPageToken is set while more addresses follow the page.
	NextPageToken string
}

func artifactUsageAddressLess(a, b ArtifactUsageStat) bool {
	if a.Resource != b.Resource {
		return a.Resource < b.Resource
	}
	return a.Format < b.Format
}

// artifactFormatsCountedForUsage are the formats whose resolved GET audits
// represent artifact downloads. Management bookkeeping reuses the audit log
// with format "management" or "" and must never inflate artifact usage.
var artifactFormatsCountedForUsage = map[Format]bool{
	FormatMaven: true,
	FormatOCI:   true,
	FormatRaw:   true,
	FormatNPM:   true,
	FormatPyPI:  true,
	FormatGo:    true,
	FormatCargo: true,
	FormatAPT:   true,
	FormatConan: true,
}

// IsArtifactDownload reports whether an audit record represents one
// successful content download served to a client: a resolved GET with a 200
// response in a real artifact format. Publishes, metadata writes, management
// operations, and revalidations (HEAD or 304, where no content moved) are not
// counted.
func (a AuditRecord) IsArtifactDownload() bool {
	if a.Outcome != AuditResolved || a.Operation != "get" || a.Status != 200 {
		return false
	}
	if a.Repository == "" || a.Resource == "" {
		return false
	}
	return artifactFormatsCountedForUsage[Format(a.Format)]
}

// UsageIncrement is the usage delta carried by one download audit.
func (a AuditRecord) UsageIncrement() ArtifactUsageStat {
	return ArtifactUsageStat{
		Repository:       a.Repository,
		Format:           a.Format,
		Resource:         a.Resource,
		TotalBytes:       a.Bytes,
		LastDownloadedAt: a.OccurredAt,
		LastActor:        a.Actor,
	}
}

// artifactUsageAddress keys one aggregate in the memory store.
func artifactUsageAddress(repository, format, resource string) string {
	return repository + "\x00" + format + "\x00" + resource
}
