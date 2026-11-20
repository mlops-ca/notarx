package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
)

// rawVersionPort is the slice of repository.NativeRawVersionStore the management API uses.
type rawVersionPort interface {
	GetRawVersion(ctx context.Context, repositoryID, name, version string) (repository.RawVersion, error)
	ListRawPackages(ctx context.Context, repositoryID, after string, limit int) ([]repository.RawPackage, error)
	ListRawVersions(ctx context.Context, query repository.RawVersionQuery) ([]repository.RawVersion, error)
	ListRawVersionFiles(ctx context.Context, repositoryID, name, version string) ([]repository.RawAsset, error)
	CreateRawVersion(ctx context.Context, version repository.RawVersion, audit ...repository.AuditRecord) (repository.RawVersion, bool, error)
	SetRawVersionProperties(ctx context.Context, repositoryID, name, version string, properties map[string]string, audit ...repository.AuditRecord) (repository.RawVersion, error)
	SealRawVersion(ctx context.Context, input repository.SealRawVersionInput) (repository.RawVersion, error)
}

// rawVersionObjects reads the stored bytes of a file; a checksums file named at seal is read through it.
type rawVersionObjects interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

// maxRawChecksumsBytes bounds the checksums file a seal reads: one line per file of a version at the default cap
// of 1000 files stays far below it.
const maxRawChecksumsBytes = 1 << 20

var rawVersionPropertyFilter = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}:`)

// rawVersionManagementHandler owns the package and version API of versioned Raw repositories (ADR 0028). It has
// no byte upload path: files travel through the Raw data plane.
type rawVersionManagementHandler struct {
	managementScope
	versions rawVersionPort
	objects  rawVersionObjects
	now      func() time.Time
}

func (h rawVersionManagementHandler) clock() time.Time {
	if h.now != nil {
		return h.now().UTC()
	}
	return time.Now().UTC()
}

// scoped runs handler for a versioned Raw repository the caller has operation on.
func (h rawVersionManagementHandler) scoped(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, operation RepositoryOperation, handler func(Principal, repository.HostedRepository)) {
	h.withRepositoryScope(w, r, id.String(), operation, func(principal Principal, repo repository.HostedRepository) {
		if h.versions == nil {
			writeHostedProblem(w, http.StatusNotImplemented, "not_supported", "versioned Raw packages are not available")
			return
		}
		if repo.Format != repository.FormatRaw || repo.Type != repository.RepositoryTypeHosted || repo.RawLayout != repository.RawLayoutVersioned {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "layout_mismatch", "the repository does not have the versioned layout")
			return
		}
		handler(principal, repo)
	})
}

func writeRawVersionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "version not found")
	case errors.Is(err, repository.ErrVersionSealed):
		writeHostedProblem(w, http.StatusConflict, "version_sealed", "the version is sealed")
	case errors.Is(err, repository.ErrVersionTombstoned):
		writeHostedProblem(w, http.StatusConflict, "version_tombstoned", "the version is tombstoned and its identifier stays burned")
	case errors.Is(err, repository.ErrChecksumsChanged):
		writeHostedProblem(w, http.StatusConflict, "version_conflict", "the checksums file changed while the version was being sealed; retry")
	case errors.Is(err, repository.ErrInvalidGenericPath):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "invalid_generic_path", "name or version is not valid for a versioned repository")
	case errors.Is(err, repository.ErrInvalidVersionProperties):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "invalid_properties", "properties exceed their bounds: at most 32 keys, 8 KiB, values of at most 256 bytes of plain text")
	case errors.Is(err, repository.ErrVersionEmpty):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "version_empty", "a version without files cannot be sealed")
	case errors.Is(err, repository.ErrChecksumMismatch):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the stored files do not match the expected files or checksums")
	case errors.Is(err, repository.ErrRawLayoutMismatch):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "layout_mismatch", "the repository does not have the versioned layout")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "versioned package operation failed")
	}
}

func validRawVersionIdentity(w http.ResponseWriter, name, version string, forWrite bool) bool {
	if repository.ValidRawVersionName(name) && repository.ValidRawVersionString(version) {
		return true
	}
	if forWrite {
		writeRawVersionError(w, repository.ErrInvalidGenericPath)
	} else {
		writeRawVersionError(w, repository.ErrNotFound)
	}
	return false
}

func rawVersionResponse(version repository.RawVersion, files []repository.RawAsset) adminopenapi.RawVersion {
	response := adminopenapi.RawVersion{
		Name: version.Name, Version: version.Version, State: adminopenapi.RawVersionState(version.State),
		FileCount: version.FileCount, SizeBytes: version.SizeBytes, Properties: adminopenapi.RawVersionPropertyMap(version.Properties),
		CreatedAt: version.CreatedAt, LastWriteAt: version.UpdatedAt,
	}
	if response.Properties == nil {
		response.Properties = adminopenapi.RawVersionPropertyMap{}
	}
	if version.CreatedBy != "" {
		response.CreatedBy = &version.CreatedBy
	}
	if version.ManifestDigest != "" {
		response.ManifestDigest = &version.ManifestDigest
	}
	if !version.SealedAt.IsZero() {
		sealedAt := version.SealedAt
		response.SealedAt = &sealedAt
	}
	if files != nil {
		prefix, _ := repository.RawVersionPathRange(version.Name, version.Version)
		listed := make([]adminopenapi.RawVersionFile, 0, len(files))
		for _, file := range files {
			entry := adminopenapi.RawVersionFile{Path: strings.TrimPrefix(file.Path, prefix), Sha256: strings.TrimPrefix(file.Digest, "sha256:"), Size: file.Size}
			if file.ContentType != "" {
				contentType := file.ContentType
				entry.ContentType = &contentType
			}
			listed = append(listed, entry)
		}
		response.Files = &listed
	}
	return response
}

func (h rawVersionManagementHandler) ListRawPackages(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, params adminopenapi.ListRawPackagesParams) {
	h.scoped(w, r, id, RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		pageSize := 50
		if params.PageSize != nil {
			pageSize = *params.PageSize
		}
		if pageSize < 1 || pageSize > 200 {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageSize must be between 1 and 200")
			return
		}
		filter := "raw-packages:" + repo.ID
		after := ""
		if params.PageToken != nil && *params.PageToken != "" {
			keys, err := repository.DecodeKeysetToken(*params.PageToken, filter, 1)
			if err != nil {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
				return
			}
			after = keys[0]
		}
		packages, err := h.versions.ListRawPackages(r.Context(), repo.ID, after, pageSize+1)
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		page := adminopenapi.RawPackagePage{Items: make([]adminopenapi.RawPackage, 0, pageSize)}
		if len(packages) > pageSize {
			packages = packages[:pageSize]
			token := repository.EncodeKeysetToken(filter, packages[len(packages)-1].Name)
			page.NextPageToken = &token
		}
		for _, pkg := range packages {
			item := adminopenapi.RawPackage{Name: pkg.Name, VersionCount: pkg.VersionCount}
			if pkg.LatestVersion != "" {
				latest := pkg.LatestVersion
				item.LatestVersion = &latest
			}
			page.Items = append(page.Items, item)
		}
		writeNativeMavenJSON(w, http.StatusOK, page)
	})
}

func (h rawVersionManagementHandler) ListRawVersions(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, params adminopenapi.ListRawVersionsParams) {
	h.scoped(w, r, id, RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		if !repository.ValidRawVersionName(name) {
			writeRawVersionError(w, repository.ErrNotFound)
			return
		}
		query := repository.RawVersionQuery{RepositoryID: repo.ID, Name: name, Limit: 51}
		pageSize := 50
		if params.PageSize != nil {
			pageSize = *params.PageSize
		}
		if pageSize < 1 || pageSize > 200 {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageSize must be between 1 and 200")
			return
		}
		query.Limit = pageSize + 1
		if params.State != nil {
			state := repository.RawVersionState(*params.State)
			if state != repository.RawVersionOpen && state != repository.RawVersionSealed {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "state must be open or sealed")
				return
			}
			query.State = state
		}
		filterParts := []string{"raw-versions", repo.ID, name, string(query.State)}
		if params.Property != nil {
			if len(*params.Property) > repository.RawVersionMaxProperties {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "too many property filters")
				return
			}
			for _, raw := range *params.Property {
				if !rawVersionPropertyFilter.MatchString(raw) {
					writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "property must be key:value with a key of lower case letters, digits, dot, underscore and dash")
					return
				}
				key, value, _ := strings.Cut(raw, ":")
				query.Properties = append(query.Properties, repository.RawVersionProperty{Key: key, Value: value})
				filterParts = append(filterParts, "p="+raw)
			}
		}
		if params.OlderThanDays != nil {
			if query.State != repository.RawVersionOpen || *params.OlderThanDays < 1 {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "olderThanDays needs state=open and a positive number of days")
				return
			}
			query.OpenCreatedBefore = h.clock().Add(-time.Duration(*params.OlderThanDays) * 24 * time.Hour)
			filterParts = append(filterParts, "days="+strconv.Itoa(*params.OlderThanDays))
		}
		filter := strings.Join(filterParts, "\x00")
		if params.PageToken != nil && *params.PageToken != "" {
			keys, err := repository.DecodeKeysetToken(*params.PageToken, filter, 2)
			if err != nil {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
				return
			}
			if !ids.Valid(keys[1]) {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
				return
			}
			cursor := repository.RawVersionCursor{ID: keys[1]}
			if keys[0] != "" {
				sealedAt, parseErr := time.Parse(time.RFC3339Nano, keys[0])
				if parseErr != nil {
					writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
					return
				}
				cursor.SealedAt = sealedAt
			}
			query.After = &cursor
		}
		versions, err := h.versions.ListRawVersions(r.Context(), query)
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		page := adminopenapi.RawVersionPage{Items: make([]adminopenapi.RawVersion, 0, pageSize)}
		if len(versions) > pageSize {
			versions = versions[:pageSize]
			last := versions[len(versions)-1]
			sealedAt := ""
			if !last.SealedAt.IsZero() {
				sealedAt = last.SealedAt.UTC().Format(time.RFC3339Nano)
			}
			token := repository.EncodeKeysetToken(filter, sealedAt, last.ID)
			page.NextPageToken = &token
		}
		for _, version := range versions {
			page.Items = append(page.Items, rawVersionResponse(version, nil))
		}
		writeNativeMavenJSON(w, http.StatusOK, page)
	})
}

func (h rawVersionManagementHandler) GetRawVersion(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, false) {
			return
		}
		found, files, ok := h.versionWithFiles(w, r, repo, name, version)
		if !ok {
			return
		}
		writeNativeMavenJSON(w, http.StatusOK, rawVersionResponse(found, files))
	})
}

func (h rawVersionManagementHandler) versionWithFiles(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, name, version string) (repository.RawVersion, []repository.RawAsset, bool) {
	found, err := h.versions.GetRawVersion(r.Context(), repo.ID, name, version)
	if err != nil {
		writeRawVersionError(w, err)
		return repository.RawVersion{}, nil, false
	}
	files, err := h.versions.ListRawVersionFiles(r.Context(), repo.ID, name, version)
	if err != nil {
		writeRawVersionError(w, err)
		return repository.RawVersion{}, nil, false
	}
	if files == nil {
		files = []repository.RawAsset{}
	}
	return found, files, true
}

type rawVersionBody struct {
	Properties *map[string]string `json:"properties"`
}

// decodeOptionalBody reads a JSON body that may be absent. An empty body is the zero value.
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(into); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	return true
}

func (h rawVersionManagementHandler) CreateRawVersion(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryWrite, func(principal Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, true) {
			return
		}
		var body rawVersionBody
		if !decodeOptionalBody(w, r, &body) {
			return
		}
		request := repository.RawVersion{RepositoryID: repo.ID, Name: name, Version: version, CreatedBy: principal.Actor}
		if body.Properties != nil {
			request.Properties = *body.Properties
			if request.Properties == nil {
				request.Properties = map[string]string{}
			}
		}
		created, isNew, err := h.versions.CreateRawVersion(r.Context(), request, repository.RawVersionAuditRecord(repo.Name, principal.Actor, "raw.version.create", name+"@"+version))
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		status := http.StatusOK
		if isNew {
			status = http.StatusCreated
		}
		writeNativeMavenJSON(w, status, rawVersionResponse(created, nil))
	})
}

func (h rawVersionManagementHandler) SetRawVersionProperties(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryWrite, func(principal Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, false) {
			return
		}
		var body rawVersionBody
		if !decodeOptionalBody(w, r, &body) {
			return
		}
		if body.Properties == nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "properties are required")
			return
		}
		updated, err := h.versions.SetRawVersionProperties(r.Context(), repo.ID, name, version, *body.Properties, repository.RawVersionAuditRecord(repo.Name, principal.Actor, "raw.version.properties", name+"@"+version))
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		writeNativeMavenJSON(w, http.StatusOK, rawVersionResponse(updated, nil))
	})
}

type rawVersionSealBody struct {
	Files         *[]adminopenapi.RawVersionSealFile `json:"files"`
	ChecksumsFile string                             `json:"checksumsFile"`
	Properties    *map[string]string                 `json:"properties"`
}

func (h rawVersionManagementHandler) SealRawVersion(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryWrite, func(principal Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, false) {
			return
		}
		var body rawVersionSealBody
		if !decodeOptionalBody(w, r, &body) {
			return
		}
		audit := repository.RawVersionAuditRecord(repo.Name, principal.Actor, "raw.version.seal", "")
		input := repository.SealRawVersionInput{RepositoryID: repo.ID, Name: name, Version: version, Actor: principal.Actor, Audit: &audit}
		if body.Files != nil {
			input.Expected = make([]repository.RawVersionFileRef, 0, len(*body.Files))
			for _, file := range *body.Files {
				input.Expected = append(input.Expected, repository.RawVersionFileRef{Path: file.Path, SHA256: file.Sha256})
			}
		}
		if body.Properties != nil {
			input.Properties = *body.Properties
			if input.Properties == nil {
				input.Properties = map[string]string{}
			}
		}
		if body.ChecksumsFile != "" {
			if !h.readChecksums(w, r, repo, &input, body.ChecksumsFile) {
				return
			}
		}
		sealed, err := h.versions.SealRawVersion(r.Context(), input)
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		files, err := h.versions.ListRawVersionFiles(r.Context(), repo.ID, name, version)
		if err != nil {
			writeRawVersionError(w, err)
			return
		}
		writeNativeMavenJSON(w, http.StatusOK, rawVersionResponse(sealed, files))
	})
}

// readChecksums reads the stored checksums file named at seal and parses it as sha256sum output. The read happens
// before the seal takes the version lock; the store re-checks under the lock that the file still has the digest
// read here.
func (h rawVersionManagementHandler) readChecksums(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, input *repository.SealRawVersionInput, file string) bool {
	if h.objects == nil {
		writeHostedProblem(w, http.StatusNotImplemented, "not_supported", "checksums files cannot be read")
		return false
	}
	files, err := h.versions.ListRawVersionFiles(r.Context(), repo.ID, input.Name, input.Version)
	if err != nil {
		writeRawVersionError(w, err)
		return false
	}
	prefix, _ := repository.RawVersionPathRange(input.Name, input.Version)
	var stored *repository.RawAsset
	for i := range files {
		if strings.TrimPrefix(files[i].Path, prefix) == file {
			stored = &files[i]
			break
		}
	}
	if stored == nil || stored.Size > maxRawChecksumsBytes {
		writeHostedProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the checksums file is not a stored file of the version, or is too large")
		return false
	}
	reader, _, err := h.objects.Open(r.Context(), stored.ObjectKey)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read checksums file failed")
		return false
	}
	defer func() { _ = reader.Close() }()
	entries, ok := parseSHA256Sums(io.LimitReader(reader, maxRawChecksumsBytes+1))
	if !ok {
		writeHostedProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the checksums file is not valid sha256sum output")
		return false
	}
	input.ChecksumsFile, input.ChecksumsDigest, input.Checksums = file, stored.Digest, entries
	return true
}

var sha256sumLine = regexp.MustCompile(`^([A-Fa-f0-9]{64}) [ *](.+)$`)

// parseSHA256Sums parses sha256sum output: "<hex>  <path>" or "<hex> *<path>" per line, blank lines and comments
// ignored. Any other line makes the file invalid.
func parseSHA256Sums(reader io.Reader) (map[string]string, bool) {
	entries := map[string]string{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := sha256sumLine.FindStringSubmatch(line)
		if match == nil {
			return nil, false
		}
		path := strings.TrimPrefix(match[2], "./")
		if existing, duplicate := entries[path]; duplicate && !strings.EqualFold(existing, match[1]) {
			return nil, false
		}
		entries[path] = strings.ToLower(match[1])
	}
	return entries, scanner.Err() == nil && len(entries) > 0
}

func (h rawVersionManagementHandler) GetRawVersionManifest(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, false) {
			return
		}
		found, files, ok := h.versionWithFiles(w, r, repo, name, version)
		if !ok {
			return
		}
		if found.State != repository.RawVersionSealed {
			writeHostedProblem(w, http.StatusConflict, "version_not_sealed", "only a sealed version has a manifest")
			return
		}
		manifest, digest := repository.RawVersionManifest(name, version, files, found.Properties)
		if digest != found.ManifestDigest {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "the stored files no longer match the sealed manifest digest")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+digest+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(manifest)
	})
}

func (h rawVersionManagementHandler) GetRawVersionChecksums(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, name adminopenapi.RawPackageName, version adminopenapi.RawVersionIdentifier) {
	h.scoped(w, r, id, RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		if !validRawVersionIdentity(w, name, version, false) {
			return
		}
		_, files, ok := h.versionWithFiles(w, r, repo, name, version)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, repository.RawVersionChecksums(name, version, files))
	})
}
