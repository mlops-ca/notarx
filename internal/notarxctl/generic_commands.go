package notarxctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
)

// The commands of versioned Raw repositories (ADR 0028): a package is name, a version is a sealable set of files,
// and the digest of its canonical manifest identifies the release.

const (
	genericChecksumsFile  = "SHA256SUMS"
	genericMaxFileDepth   = 8
	genericMaxProperties  = 32
	genericVersionsPrefix = "packages/"
)

var (
	genericNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	genericVersionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`)
	genericPropertyKeyExpr = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	genericSHA256Pattern   = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
)

// VerificationError marks bytes or documents that do not match the digest the server published for them. It exits
// with code 1 and the stable envelope code verification_failed; nothing that failed verification is left in place.
type VerificationError struct {
	Message string
	Hint    string
}

func (e *VerificationError) Error() string { return e.Message }

// genericIdentity is the repository, package and version every generic command addresses.
type genericIdentity struct {
	repo, name, version string
}

func (g *genericIdentity) flags(fs *flag.FlagSet) {
	fs.StringVar(&g.repo, "repo", "", "versioned Raw repository ID")
	fs.StringVar(&g.name, "name", "", "package name (lower case letters, digits, dot, underscore, dash)")
	fs.StringVar(&g.version, "version", "", "version identifier")
}

func (g genericIdentity) validate(needVersion bool) error {
	if err := requireRepo(g.repo); err != nil {
		return err
	}
	if err := requireFlag("name", g.name, "pass the package name, for example --name tool"); err != nil {
		return err
	}
	if !genericNamePattern.MatchString(g.name) {
		return &UsageError{Message: fmt.Sprintf("invalid package name %q", g.name), Hint: "use 1-128 lower case letters, digits, dot, underscore or dash, starting with a letter or digit"}
	}
	if !needVersion {
		return nil
	}
	if err := requireFlag("version", g.version, "pass the version, for example --version 1.4.2"); err != nil {
		return err
	}
	if !genericVersionPattern.MatchString(g.version) {
		return &UsageError{Message: fmt.Sprintf("invalid version %q", g.version), Hint: "use 1-128 letters, digits, dot, plus, tilde, underscore-free dash, starting with a letter or digit"}
	}
	return nil
}

func (g genericIdentity) versionPath() string {
	return repositorySubPath(g.repo, genericVersionsPrefix+url.PathEscape(g.name)+"/versions/"+url.PathEscape(g.version))
}

func (g genericIdentity) prefix() string { return g.name + "/" + g.version }

// parseGenericProperties reads repeated key=value flags into the property map of a version.
func parseGenericProperties(values stringList) (map[string]string, error) {
	properties := map[string]string{}
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || !genericPropertyKeyExpr.MatchString(key) {
			return nil, &UsageError{Message: fmt.Sprintf("invalid property %q", raw), Hint: "use --property key=value with a key of lower case letters, digits, dot, underscore and dash"}
		}
		if _, duplicate := properties[key]; duplicate {
			return nil, &UsageError{Message: fmt.Sprintf("property %q is given twice", key), Hint: "pass each key once"}
		}
		properties[key] = value
	}
	if len(properties) > genericMaxProperties {
		return nil, &UsageError{Message: fmt.Sprintf("%d properties exceed the limit of %d", len(properties), genericMaxProperties), Hint: "a version carries at most 32 properties"}
	}
	return properties, nil
}

// genericTarget reads the repository and checks that it is an active hosted Raw repository with the versioned layout.
func genericTarget(ctx context.Context, client *apiClient, id string) (adminopenapi.Repository, error) {
	var repo adminopenapi.Repository
	if err := client.getJSON(ctx, repositoryPath(id), nil, &repo); err != nil {
		return repo, err
	}
	if !strings.EqualFold(repo.Id, id) || repo.Name == "" {
		return repo, &APIError{Code: "invalid_response", HTTPStatus: http.StatusOK, Message: "repository response does not identify the requested target"}
	}
	if repo.Format != adminopenapi.FormatRaw || repositoryType(repo) != string(adminopenapi.RepositoryTypeHosted) || repo.State != adminopenapi.RepositoryStateActive {
		return repo, &UsageError{Message: fmt.Sprintf("repository %s is not an active Raw Hosted repository", repo.Name), Hint: "generic packages live in a Raw Hosted repository created with layout versioned"}
	}
	if repo.Layout == nil || *repo.Layout != adminopenapi.RawLayoutVersioned {
		return repo, &UsageError{Message: fmt.Sprintf("repository %s does not have the versioned layout", repo.Name), Hint: "use notarxctl publish raw for a path repository; the layout is chosen at creation"}
	}
	return repo, nil
}

// genericObjects maps the push sources to repository paths <name>/<version>/<file>.
func genericObjects(id genericIdentity, sources []string) ([]localObject, error) {
	var objects []localObject
	seen := map[string]string{}
	for _, source := range sources {
		info, err := os.Lstat(source)
		if err != nil {
			return nil, &UsageError{Message: fmt.Sprintf("cannot read %s", source), Hint: "pass existing files or directories"}
		}
		remote := id.prefix()
		if info.Mode().IsRegular() {
			remote += "/" + filepath.Base(source)
		}
		collected, err := collectRawObjects(source, remote)
		if err != nil {
			return nil, err
		}
		for _, object := range collected {
			relative := strings.TrimPrefix(object.remote, id.prefix()+"/")
			segments := strings.Split(relative, "/")
			if len(segments) > genericMaxFileDepth || segments[0] == ".notarx" {
				return nil, &UsageError{Message: fmt.Sprintf("%s maps to %q, which a versioned repository does not accept", object.file, relative), Hint: "files sit at most eight directories deep and .notarx is reserved"}
			}
			if previous, duplicate := seen[object.remote]; duplicate {
				return nil, &UsageError{Message: fmt.Sprintf("%s and %s both map to %q", previous, object.file, relative), Hint: "each file of a version has one path"}
			}
			seen[object.remote] = object.file
			objects = append(objects, object)
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].remote < objects[j].remote })
	return objects, nil
}

// checksumsObject renders the SHA256SUMS file of the objects as sha256sum -c reads it and stages it in a temporary
// file so it uploads like any other file; the returned cleanup removes it.
func checksumsObject(id genericIdentity, objects []localObject) (localObject, func(), error) {
	var content strings.Builder
	for _, object := range objects {
		fmt.Fprintf(&content, "%s  %s\n", strings.TrimPrefix(object.digest, "sha256:"), strings.TrimPrefix(object.remote, id.prefix()+"/"))
	}
	dir, err := os.MkdirTemp("", "notarxctl-sums-")
	if err != nil {
		return localObject{}, nil, &UsageError{Message: "cannot stage the checksums file", Hint: "check the temporary directory"}
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, genericChecksumsFile)
	if err = os.WriteFile(file, []byte(content.String()), 0o600); err != nil {
		cleanup()
		return localObject{}, nil, &UsageError{Message: "cannot stage the checksums file", Hint: "check the temporary directory"}
	}
	object, err := digestFile(file)
	if err != nil {
		cleanup()
		return localObject{}, nil, err
	}
	object.remote = id.prefix() + "/" + genericChecksumsFile
	return object, cleanup, nil
}

// genericPushResult is what push reports: the objects it sent and the state of the version afterwards.
type genericPushResult struct {
	Repository     string         `json:"repository"`
	Name           string         `json:"name"`
	Version        string         `json:"version"`
	State          string         `json:"state"`
	ManifestDigest string         `json:"manifestDigest,omitempty"`
	Objects        []objectResult `json:"objects"`
}

func (r genericPushResult) human(w io.Writer) error {
	if len(r.Objects) > 0 {
		rows := [][]string{{"STATUS", "PATH", "SIZE", "DIGEST"}}
		for _, object := range r.Objects {
			status := object.Status
			if object.Error != "" {
				status += ": " + object.Error
			}
			size := object.Size
			rows = append(rows, []string{status, object.Path, formatBytes(&size), shortDigest(object.Digest)})
		}
		if err := writeColumns(w, rows); err != nil {
			return err
		}
	}
	line := fmt.Sprintf("%s@%s is %s", r.Name, r.Version, r.State)
	if r.ManifestDigest != "" {
		line += ", manifest " + r.ManifestDigest
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// putGenericObject uploads one file of a version. The Digest header makes the server verify the bytes. Files are
// create-only: the same bytes again are a replay and answer 200, other bytes at the path are a 409 conflict.
func putGenericObject(ctx context.Context, client *apiClient, target string, object localObject) (replayed bool, err error) {
	response, err := client.do(ctx, http.MethodPut, target, requestOptions{
		idempotent: true,
		stream:     object.open(),
		length:     object.size,
		header:     http.Header{"Digest": {"sha-256=" + base64Std(object.raw)}},
	})
	if err != nil {
		return false, err
	}
	if response.StatusCode >= 300 {
		return false, parseAPIError(response, client.token)
	}
	if normalizeRawETag(response.Header.Get("ETag")) != object.digest {
		return false, incompleteResponse(response, client, "generic package upload")
	}
	return response.StatusCode == http.StatusOK, nil
}

func genericConflictCode(err error) (string, bool) {
	var api *APIError
	if errors.As(err, &api) && api.HTTPStatus == http.StatusConflict {
		return api.Code, true
	}
	return "", false
}

func genericPushCommand() command {
	var id genericIdentity
	var properties, extra stringList
	var checksums, noSeal, resumable bool
	var parallel int
	var chunkSize int64
	return command{
		name:        "generic push",
		synopsis:    "upload files as one version of a package and seal it",
		usage:       "notarxctl generic push SOURCE --repo ID --name N --version V [--add PATH]... [--property k=v]... [--checksums] [--no-seal] [--parallel N] [--resumable] [--chunk-size BYTES] [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "SOURCE",
		setFlags: func(fs *flag.FlagSet) {
			id.flags(fs)
			fs.Var(&extra, "add", "another file or directory of the same version (repeatable)")
			fs.Var(&properties, "property", "version property key=value, frozen at seal (repeatable)")
			fs.BoolVar(&checksums, "checksums", false, "write a SHA256SUMS file and have the server verify it at seal")
			fs.BoolVar(&noSeal, "no-seal", false, "leave the version open instead of sealing it")
			fs.IntVar(&parallel, "parallel", 4, "uploads in flight (1-16)")
			fs.BoolVar(&resumable, "resumable", false, "upload in bounded offset-addressed chunks")
			fs.Int64Var(&chunkSize, "chunk-size", 4<<20, "resumable chunk bytes (1-16777216, default 4194304)")
		},
		meta: commandMeta{
			Resource:  "artifact",
			Scenarios: []string{"ci"},
			Stage:     "m2",
			Endpoints: []string{
				"GET " + repositoriesPath + "/{repositoryId}",
				"PUT " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}",
				"POST " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}:seal",
				"PUT /raw/{repository}/{path}",
				"POST /raw/{repository}/{path}",
				"PATCH /raw/{repository}/{path}",
			},
			Mutating:     true,
			MinimumRole:  "repositories:write",
			OutputSchema: "GenericPushResult",
			DryRun:       "client-plan",
			Retry:        true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := id.validate(true); err != nil {
				return err
			}
			if parallel < 1 || parallel > maxPublishParallel {
				return &UsageError{Message: "--parallel must be 1-16", Hint: "for example --parallel 4"}
			}
			if chunkSize < 1 || chunkSize > 16<<20 || (!resumable && env.flagSet["chunk-size"]) {
				return &UsageError{Message: "--chunk-size requires --resumable and must be 1-16777216", Hint: "for example --resumable --chunk-size 4194304"}
			}
			props, err := parseGenericProperties(properties)
			if err != nil {
				return err
			}
			objects, err := genericObjects(id, append([]string{env.args[0]}, extra...))
			if err != nil {
				return err
			}
			for _, object := range objects {
				if object.size > maxRawObjectBytes {
					return &UsageError{Message: fmt.Sprintf("%s is larger than the 1 GiB a Raw upload accepts", object.file), Hint: "split the file"}
				}
				if checksums && strings.TrimPrefix(object.remote, id.prefix()+"/") == genericChecksumsFile {
					return &UsageError{Message: "a file named " + genericChecksumsFile + " conflicts with --checksums", Hint: "drop --checksums to publish your own checksums file, or rename the file"}
				}
			}
			uploads := objects
			if checksums {
				sums, cleanup, err := checksumsObject(id, objects)
				if err != nil {
					return err
				}
				defer cleanup()
				uploads = append(slices.Clone(objects), sums)
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, env.globals.timeout)
			defer cancel()
			repo, err := genericTarget(ctx, client, id.repo)
			if err != nil {
				return err
			}
			result := genericPushResult{Repository: repo.Name, Name: id.name, Version: id.version, State: "open", Objects: make([]objectResult, len(uploads))}
			if len(props) > 0 && !env.globals.dryRun {
				body, _ := json.Marshal(map[string]any{"properties": props})
				if _, err = client.sendJSON(ctx, http.MethodPut, id.versionPath(), requestOptions{idempotent: true, body: body}, nil); err != nil {
					return err
				}
			}
			var wait sync.WaitGroup
			slots := make(chan struct{}, parallel)
			for index, object := range uploads {
				result.Objects[index] = objectResult{Path: object.remote, Coordinate: rawCoordinate(object.remote), File: object.file, Size: object.size, Digest: object.digest}
				wait.Add(1)
				go func(item *objectResult, object localObject) {
					defer wait.Done()
					slots <- struct{}{}
					defer func() { <-slots }()
					if env.globals.dryRun {
						item.Status = "planned"
						return
					}
					target := rawObjectPath(repo.Name, object.remote)
					var uploadErr error
					replayed := false
					if resumable {
						uploadErr = putResumableRawObject(ctx, client, target, object, chunkSize, "", item)
					} else {
						replayed, uploadErr = putGenericObject(ctx, client, target, object)
					}
					switch code, conflict := genericConflictCode(uploadErr); {
					case uploadErr == nil && replayed:
						item.Status = "unchanged"
					case uploadErr == nil:
						item.Status = "uploaded"
					case conflict:
						item.Status, item.Code, item.Error = "conflict", code, redactText(uploadErr.Error(), client.token)
					default:
						item.Status, item.Error, item.Code = "failed", redactText(uploadErr.Error(), client.token), localErrorCode(uploadErr)
						if outcomeUnknown(uploadErr) {
							item.Status = "unknown"
						}
					}
				}(&result.Objects[index], object)
			}
			wait.Wait()
			meta := &envelopeMeta{Pagination: &paginationMeta{Mode: "none"}}
			if failure := genericPushFailure(result.Objects); failure != nil {
				return &resultError{err: failure, data: result, meta: meta, human: result.human, hint: genericRerunHint}
			}
			if env.globals.dryRun || noSeal {
				return env.render(result, meta, result.human)
			}
			seal := map[string]any{"files": expectedFiles(id, uploads)}
			if checksums {
				seal["checksumsFile"] = genericChecksumsFile
			}
			body, _ := json.Marshal(seal)
			var sealed adminopenapi.RawVersion
			if _, err = client.sendJSON(ctx, http.MethodPost, id.versionPath()+":seal", requestOptions{idempotent: true, body: body}, &sealed); err != nil {
				return &resultError{err: err, data: result, meta: meta, human: result.human, hint: "the files are uploaded and the version is open; fix the difference and run notarxctl generic seal, or re-run this command"}
			}
			result.State = string(sealed.State)
			if sealed.ManifestDigest != nil {
				result.ManifestDigest = *sealed.ManifestDigest
			}
			return env.render(result, meta, result.human)
		},
	}
}

const genericRerunHint = "re-run the same notarxctl generic push command: files already stored with the same bytes answer as unchanged, and nothing already stored is ever replaced"

// genericPushFailure classifies the uploads that did not succeed. A refusal because the version already holds other
// bytes at a path (or is sealed or tombstoned) is a conflict and exits 7, apart from the partial failure of an
// interrupted batch (exit 3).
func genericPushFailure(results []objectResult) error {
	var conflicts, failed int
	var conflictCode string
	for _, result := range results {
		switch result.Status {
		case "conflict":
			conflicts++
			conflictCode = result.Code
		case "failed", "unknown":
			failed++
		}
	}
	switch {
	case conflicts > 0:
		return &APIError{Code: conflictCode, HTTPStatus: http.StatusConflict, Message: fmt.Sprintf("%d of %d files conflict with what the version already holds", conflicts, len(results)), Hint: "a published file is never replaced: use a new version, or while the version is open and its repository is not strict delete the file first"}
	case failed > 0:
		return &PartialError{Message: fmt.Sprintf("%d of %d files were not confirmed", failed, len(results)), Hint: genericRerunHint}
	}
	return nil
}

func expectedFiles(id genericIdentity, objects []localObject) []map[string]string {
	files := make([]map[string]string, 0, len(objects))
	for _, object := range objects {
		files = append(files, map[string]string{"path": strings.TrimPrefix(object.remote, id.prefix()+"/"), "sha256": strings.TrimPrefix(object.digest, "sha256:")})
	}
	return files
}

func base64Std(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

// genericPullResult is what pull reports per file and for the manifest.
type genericPullResult struct {
	Name             string           `json:"name"`
	Version          string           `json:"version"`
	State            string           `json:"state"`
	ManifestDigest   string           `json:"manifestDigest,omitempty"`
	ManifestVerified bool             `json:"manifestVerified"`
	To               string           `json:"to"`
	Files            []pullFileResult `json:"files"`
}

type pullFileResult struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	Status string `json:"status"` // downloaded, unchanged, failed
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"`
}

func (r genericPullResult) human(w io.Writer) error {
	rows := [][]string{{"STATUS", "PATH", "SIZE", "DIGEST"}}
	for _, file := range r.Files {
		status := file.Status
		if file.Error != "" {
			status += ": " + file.Error
		}
		size := file.Size
		rows = append(rows, []string{status, file.Path, formatBytes(&size), shortDigest(file.Digest)})
	}
	if err := writeColumns(w, rows); err != nil {
		return err
	}
	line := fmt.Sprintf("%s@%s is %s", r.Name, r.Version, r.State)
	if r.ManifestVerified {
		line += ", manifest " + r.ManifestDigest + " verified"
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// safeRelativePath reports whether a server-supplied file path stays inside the destination.
func safeRelativePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// download streams a GET into sink without the response-size cap of the JSON client.
func (c *apiClient) download(ctx context.Context, requestPath string, sink func(io.Reader) error) error {
	requestURL := c.resolveURL(requestPath, nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return &UsageError{Message: "cannot build HTTP request: " + redactText(err.Error(), c.token)}
	}
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return c.contextError(ctx, requestURL)
		}
		return c.transportError(err, requestURL)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, clientMaxBody))
		return parseAPIError(&apiResponse{StatusCode: response.StatusCode, Header: response.Header, Body: body}, c.token)
	}
	return sink(response.Body)
}

// pullFile downloads one file next to its destination, verifies size and sha256 and only then moves it into place.
func pullFile(ctx context.Context, client *apiClient, target, destination string, file adminopenapi.RawVersionFile) (status string, err error) {
	if existing, digest, statErr := localDigest(destination); statErr == nil && existing == file.Size && digest == file.Sha256 {
		return "unchanged", nil
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "failed", &UsageError{Message: fmt.Sprintf("cannot create the directory of %s", destination), Hint: "check the destination permissions"}
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".notarx-pull-*")
	if err != nil {
		return "failed", &UsageError{Message: fmt.Sprintf("cannot write next to %s", destination), Hint: "check the destination permissions"}
	}
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(temp.Name())
		}
	}()
	hash := sha256.New()
	var written int64
	if err = client.download(ctx, target, func(body io.Reader) error {
		var copyErr error
		written, copyErr = io.Copy(io.MultiWriter(temp, hash), body)
		return copyErr
	}); err != nil {
		return "failed", err
	}
	if written != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.Sha256 {
		return "failed", &VerificationError{Message: fmt.Sprintf("%s does not match the size and sha256 the version lists", file.Path), Hint: "the bytes were discarded; retry, and report a repeated failure"}
	}
	if err = temp.Close(); err != nil {
		return "failed", err
	}
	if err = os.Chmod(temp.Name(), 0o644); err != nil {
		return "failed", err
	}
	if err = os.Rename(temp.Name(), destination); err != nil {
		return "failed", err
	}
	committed = true
	return "downloaded", nil
}

func localDigest(file string) (int64, string, error) {
	handle, err := os.Open(file)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = handle.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, handle)
	return size, hex.EncodeToString(hash.Sum(nil)), err
}

func genericPullCommand() command {
	var id genericIdentity
	var files stringList
	var to string
	var allowOpen bool
	var parallel int
	return command{
		name:     "generic pull",
		synopsis: "download and verify the files of a version",
		usage:    "notarxctl generic pull --repo ID --name N --version V [--file PATH]... [--to DIR] [--allow-open] [--parallel N] [--json]",
		setFlags: func(fs *flag.FlagSet) {
			id.flags(fs)
			fs.Var(&files, "file", "download only this file of the version (repeatable)")
			fs.StringVar(&to, "to", ".", "destination directory")
			fs.BoolVar(&allowOpen, "allow-open", false, "also download a version that is not sealed yet")
			fs.IntVar(&parallel, "parallel", 4, "downloads in flight (1-16)")
		},
		meta: commandMeta{
			Resource:  "artifact",
			Scenarios: []string{"ci"},
			Stage:     "m2",
			Endpoints: []string{
				"GET " + repositoriesPath + "/{repositoryId}",
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}",
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}/manifest",
				"GET /raw/{repository}/{path}",
			},
			MinimumRole:  "repositories:read",
			OutputSchema: "GenericPullResult",
			DryRun:       "unsupported",
			Retry:        true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := id.validate(true); err != nil {
				return err
			}
			if parallel < 1 || parallel > maxPublishParallel {
				return &UsageError{Message: "--parallel must be 1-16", Hint: "for example --parallel 4"}
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, env.globals.timeout)
			defer cancel()
			repo, err := genericTarget(ctx, client, id.repo)
			if err != nil {
				return err
			}
			var version adminopenapi.RawVersion
			if err = client.getJSON(ctx, id.versionPath(), nil, &version); err != nil {
				return err
			}
			if version.State != adminopenapi.RawVersionStateSealed && !allowOpen {
				return &UsageError{Message: fmt.Sprintf("%s@%s is %s, not sealed", id.name, id.version, version.State), Hint: "wait for the publisher to seal it, or pass --allow-open to download an unsealed version"}
			}
			listed := []adminopenapi.RawVersionFile{}
			if version.Files != nil {
				listed = *version.Files
			}
			selected := listed
			if len(files) > 0 {
				selected = nil
				for _, wanted := range files {
					index := slices.IndexFunc(listed, func(file adminopenapi.RawVersionFile) bool { return file.Path == wanted })
					if index < 0 {
						return &UsageError{Message: fmt.Sprintf("%s is not a file of %s@%s", wanted, id.name, id.version), Hint: "list the files with notarxctl generic show"}
					}
					selected = append(selected, listed[index])
				}
			}
			result := genericPullResult{Name: id.name, Version: id.version, State: string(version.State), To: to, Files: make([]pullFileResult, len(selected))}
			if version.ManifestDigest != nil {
				result.ManifestDigest = *version.ManifestDigest
			}
			var wait sync.WaitGroup
			slots := make(chan struct{}, parallel)
			for index, file := range selected {
				result.Files[index] = pullFileResult{Path: file.Path, Size: file.Size, Digest: "sha256:" + file.Sha256}
				wait.Add(1)
				go func(item *pullFileResult, file adminopenapi.RawVersionFile) {
					defer wait.Done()
					slots <- struct{}{}
					defer func() { <-slots }()
					if !safeRelativePath(file.Path) {
						item.Status, item.Code, item.Error = "failed", "verification_failed", "the server listed a path that would leave the destination"
						return
					}
					destination := filepath.Join(to, filepath.FromSlash(file.Path))
					status, err := pullFile(ctx, client, rawObjectPath(repo.Name, id.prefix()+"/"+file.Path), destination, file)
					item.Status = status
					if err != nil {
						item.Error, item.Code = redactText(err.Error(), client.token), localErrorCode(err)
						var verification *VerificationError
						if errors.As(err, &verification) {
							item.Code = "verification_failed"
						}
					}
				}(&result.Files[index], file)
			}
			wait.Wait()
			meta := &envelopeMeta{Pagination: &paginationMeta{Mode: "none"}}
			if failure := genericPullFailure(result.Files); failure != nil {
				return &resultError{err: failure, data: result, meta: meta, human: result.human, hint: errorHint(failure)}
			}
			if version.State == adminopenapi.RawVersionStateSealed {
				if err = verifyManifest(ctx, client, id, version, selected); err != nil {
					return &resultError{err: err, data: result, meta: meta, human: result.human, hint: errorHint(err)}
				}
				result.ManifestVerified = true
			}
			return env.render(result, meta, result.human)
		},
	}
}

func genericPullFailure(files []pullFileResult) error {
	verification, failed := 0, 0
	for _, file := range files {
		switch {
		case file.Status != "failed":
		case file.Code == "verification_failed":
			verification++
		default:
			failed++
		}
	}
	switch {
	case verification > 0:
		return &VerificationError{Message: fmt.Sprintf("%d of %d files failed verification and were discarded", verification, len(files)), Hint: "nothing that failed verification was left in the destination; retry, and report a repeated failure"}
	case failed > 0:
		return &PartialError{Message: fmt.Sprintf("%d of %d files were not downloaded", failed, len(files)), Hint: "re-run the same command: files already verified in place are reported as unchanged"}
	}
	return nil
}

// verifyManifest fetches the canonical manifest of a sealed version and checks that its bytes hash to the digest the
// version reports and that every downloaded file is in it with the same sha256 and size, so a server cannot serve a
// detail and a manifest that disagree.
func verifyManifest(ctx context.Context, client *apiClient, id genericIdentity, version adminopenapi.RawVersion, downloaded []adminopenapi.RawVersionFile) error {
	if version.ManifestDigest == nil {
		return &VerificationError{Message: "the sealed version reports no manifest digest", Hint: "report this to the repository operator"}
	}
	response, err := client.do(ctx, http.MethodGet, id.versionPath()+"/manifest", requestOptions{idempotent: true})
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return parseAPIError(response, client.token)
	}
	sum := sha256.Sum256(response.Body)
	if "sha256:"+hex.EncodeToString(sum[:]) != *version.ManifestDigest {
		return &VerificationError{Message: "the manifest does not hash to the digest the version reports", Hint: "the files were downloaded but the release identity is not confirmed; report this to the repository operator"}
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Files   []struct {
			Path   string `json:"path"`
			Sha256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"files"`
	}
	if err = json.Unmarshal(response.Body, &manifest); err != nil || manifest.Name != id.name || manifest.Version != id.version {
		return &VerificationError{Message: "the manifest does not describe the requested version", Hint: "report this to the repository operator"}
	}
	for _, file := range downloaded {
		if !slices.ContainsFunc(manifest.Files, func(entry struct {
			Path   string `json:"path"`
			Sha256 string `json:"sha256"`
			Size   int64  `json:"size"`
		}) bool {
			return entry.Path == file.Path && entry.Sha256 == file.Sha256 && entry.Size == file.Size
		}) {
			return &VerificationError{Message: fmt.Sprintf("%s is not in the manifest with the downloaded sha256 and size", file.Path), Hint: "report this to the repository operator"}
		}
	}
	return nil
}

func genericListCommand() command {
	var id genericIdentity
	var state string
	var properties stringList
	var olderThan int
	packages := &cursorList[adminopenapi.RawPackage]{noun: "packages", render: func(w io.Writer, items []adminopenapi.RawPackage) error {
		if len(items) == 0 {
			return emptyLine(w, "packages")
		}
		rows := [][]string{{"NAME", "LATEST", "VERSIONS"}}
		for _, item := range items {
			rows = append(rows, []string{item.Name, optional(item.LatestVersion), fmt.Sprint(item.VersionCount)})
		}
		return writeColumns(w, rows)
	}}
	versions := &cursorList[adminopenapi.RawVersion]{noun: "versions", render: func(w io.Writer, items []adminopenapi.RawVersion) error {
		if len(items) == 0 {
			return emptyLine(w, "versions")
		}
		rows := [][]string{{"VERSION", "STATE", "FILES", "SIZE", "MANIFEST", "LAST WRITE"}}
		for _, item := range items {
			size := item.SizeBytes
			rows = append(rows, []string{item.Version, string(item.State), fmt.Sprint(item.FileCount), formatBytes(&size), shortDigest(optional(item.ManifestDigest)), item.LastWriteAt.UTC().Format("2006-01-02 15:04")})
		}
		return writeColumns(w, rows)
	}}
	packages.request = func(*commandContext) (string, url.Values, error) {
		if err := requireRepo(id.repo); err != nil {
			return "", nil, err
		}
		return repositorySubPath(id.repo, "packages"), url.Values{}, nil
	}
	versions.request = func(*commandContext) (string, url.Values, error) {
		if err := id.validate(false); err != nil {
			return "", nil, err
		}
		values := url.Values{}
		if state != "" {
			if state != "open" && state != "sealed" {
				return "", nil, &UsageError{Message: fmt.Sprintf("invalid --state %q", state), Hint: "use open or sealed"}
			}
			values.Set("state", state)
		}
		for _, raw := range properties {
			if key, _, ok := strings.Cut(raw, "="); !ok || !genericPropertyKeyExpr.MatchString(key) {
				return "", nil, &UsageError{Message: fmt.Sprintf("invalid property filter %q", raw), Hint: "use --property key=value"}
			}
			key, value, _ := strings.Cut(raw, "=")
			values.Add("property", key+":"+value)
		}
		if olderThan != 0 {
			if olderThan < 1 || state != "open" {
				return "", nil, &UsageError{Message: "--older-than-days needs --state open and a positive number", Hint: "for example --state open --older-than-days 7"}
			}
			values.Set("olderThanDays", fmt.Sprint(olderThan))
		}
		return repositorySubPath(id.repo, genericVersionsPrefix+url.PathEscape(id.name)+"/versions"), values, nil
	}
	scope := func(json.RawMessage) map[string]any {
		scope := map[string]any{"repositoryId": id.repo}
		if id.name != "" {
			scope["name"] = id.name
		}
		return scope
	}
	packages.scope, versions.scope = scope, scope
	return command{
		name:     "generic list",
		synopsis: "list the packages of a versioned repository, or the versions of one package",
		usage:    "notarxctl generic list --repo ID [--name N [--state open|sealed] [--property k=v]... [--older-than-days N]] [--page-size N] [--cursor TOKEN | --all] [--json]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&id.repo, "repo", "", "versioned Raw repository ID")
			fs.StringVar(&id.name, "name", "", "list the versions of this package instead of the packages")
			fs.StringVar(&state, "state", "", "only open or sealed versions (with --name)")
			fs.Var(&properties, "property", "only versions with this property key=value (with --name, repeatable)")
			fs.IntVar(&olderThan, "older-than-days", 0, "only open versions created more than N days ago (with --name --state open)")
			packages.setFlags(fs)
		},
		meta: commandMeta{
			Resource: "artifact", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{
				"GET " + repositoriesPath + "/{repositoryId}/packages",
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions",
			},
			MinimumRole: "repositories:read", OutputSchema: "RawPackagePage", Pagination: true, DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if id.name == "" {
				if state != "" || len(properties) > 0 || olderThan != 0 {
					return &UsageError{Message: "--state, --property and --older-than-days need --name", Hint: "pass --name to list the versions of one package"}
				}
				return packages.run(ctx, env)
			}
			versions.pageSize, versions.maxPages, versions.cursor, versions.all = packages.pageSize, packages.maxPages, packages.cursor, packages.all
			return versions.run(ctx, env)
		},
	}
}

func genericShowCommand() command {
	var id genericIdentity
	var manifest, checksums bool
	return command{
		name:     "generic show",
		synopsis: "show one version, its manifest or its checksums",
		usage:    "notarxctl generic show --repo ID --name N --version V [--manifest | --checksums] [--json]",
		setFlags: func(fs *flag.FlagSet) {
			id.flags(fs)
			fs.BoolVar(&manifest, "manifest", false, "print the canonical manifest of a sealed version")
			fs.BoolVar(&checksums, "checksums", false, "print sha256sum -c compatible checksums")
		},
		meta: commandMeta{
			Resource: "artifact", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}",
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}/manifest",
				"GET " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}/checksums",
			},
			MinimumRole: "repositories:read", OutputSchema: "RawVersion", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := id.validate(true); err != nil {
				return err
			}
			if manifest && checksums {
				return &UsageError{Message: "--manifest and --checksums cannot be combined", Hint: "pick one document"}
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			switch {
			case manifest:
				response, err := client.do(ctx, http.MethodGet, id.versionPath()+"/manifest", requestOptions{idempotent: true})
				if err != nil {
					return err
				}
				if response.StatusCode >= 300 {
					return parseAPIError(response, client.token)
				}
				return env.render(json.RawMessage(response.Body), nil, func(w io.Writer) error {
					_, err := w.Write(append(bytes.TrimRight(response.Body, "\n"), '\n'))
					return err
				})
			case checksums:
				response, err := client.do(ctx, http.MethodGet, id.versionPath()+"/checksums", requestOptions{idempotent: true})
				if err != nil {
					return err
				}
				if response.StatusCode >= 300 {
					return parseAPIError(response, client.token)
				}
				return env.render(map[string]string{"checksums": string(response.Body)}, nil, func(w io.Writer) error {
					_, err := w.Write(response.Body)
					return err
				})
			}
			var version adminopenapi.RawVersion
			if err = client.getJSON(ctx, id.versionPath(), nil, &version); err != nil {
				return err
			}
			return env.render(version, nil, func(w io.Writer) error { return renderGenericVersion(w, version) })
		},
	}
}

func renderGenericVersion(w io.Writer, version adminopenapi.RawVersion) error {
	rows := [][]string{
		{"NAME", version.Name},
		{"VERSION", version.Version},
		{"STATE", string(version.State)},
		{"FILES", fmt.Sprint(version.FileCount)},
		{"MANIFEST", optional(version.ManifestDigest)},
		{"CREATED", version.CreatedAt.UTC().Format("2006-01-02 15:04:05")},
		{"LAST WRITE", version.LastWriteAt.UTC().Format("2006-01-02 15:04:05")},
	}
	keys := make([]string, 0, len(version.Properties))
	for key := range version.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rows = append(rows, []string{"PROPERTY " + key, version.Properties[key]})
	}
	if err := writeColumns(w, rows); err != nil {
		return err
	}
	if version.Files == nil || len(*version.Files) == 0 {
		return nil
	}
	files := [][]string{{"FILE", "SIZE", "SHA256"}}
	for _, file := range *version.Files {
		size := file.Size
		files = append(files, []string{file.Path, formatBytes(&size), file.Sha256})
	}
	_, _ = fmt.Fprintln(w)
	return writeColumns(w, files)
}

func genericSealCommand() command {
	var id genericIdentity
	var expect, properties stringList
	var checksumsFile string
	return command{
		name:     "generic seal",
		synopsis: "seal an open version, optionally verifying an expected file list or a checksums file",
		usage:    "notarxctl generic seal --repo ID --name N --version V [--expect PATH=SHA256]... [--checksums-file PATH] [--property k=v]... [--json]",
		setFlags: func(fs *flag.FlagSet) {
			id.flags(fs)
			fs.Var(&expect, "expect", "an expected file PATH=SHA256 (repeatable); the stored set must equal the list")
			fs.StringVar(&checksumsFile, "checksums-file", "", "a stored file, usually SHA256SUMS, whose sha256sum entries must match the stored files")
			fs.Var(&properties, "property", "replace the version properties with these key=value pairs before they freeze (repeatable)")
		},
		meta: commandMeta{
			Resource: "artifact", Scenarios: []string{"ci"}, Stage: "m2",
			Endpoints:   []string{"POST " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}:seal"},
			Mutating:    true,
			MinimumRole: "repositories:write", OutputSchema: "RawVersion", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := id.validate(true); err != nil {
				return err
			}
			body := map[string]any{}
			if len(expect) > 0 {
				files := []map[string]string{}
				for _, raw := range expect {
					file, digest, ok := strings.Cut(raw, "=")
					if !ok || file == "" || !genericSHA256Pattern.MatchString(digest) {
						return &UsageError{Message: fmt.Sprintf("invalid --expect %q", raw), Hint: "use --expect PATH=SHA256 with a 64 digit hex sha256"}
					}
					files = append(files, map[string]string{"path": file, "sha256": strings.ToLower(digest)})
				}
				body["files"] = files
			}
			if checksumsFile != "" {
				body["checksumsFile"] = checksumsFile
			}
			if len(properties) > 0 {
				props, err := parseGenericProperties(properties)
				if err != nil {
					return err
				}
				body["properties"] = props
			}
			payload, _ := json.Marshal(body)
			client, err := env.client()
			if err != nil {
				return err
			}
			var version adminopenapi.RawVersion
			if _, err = client.sendJSON(ctx, http.MethodPost, id.versionPath()+":seal", requestOptions{idempotent: true, body: payload}, &version); err != nil {
				return err
			}
			return env.render(version, nil, func(w io.Writer) error { return renderGenericVersion(w, version) })
		},
	}
}

func genericPropertiesCommand() command {
	var id genericIdentity
	var properties stringList
	var clear bool
	return command{
		name:     "generic properties",
		synopsis: "replace the properties of an open version",
		usage:    "notarxctl generic properties --repo ID --name N --version V (--property k=v... | --clear) [--json]",
		setFlags: func(fs *flag.FlagSet) {
			id.flags(fs)
			fs.Var(&properties, "property", "a version property key=value (repeatable); the whole map is replaced")
			fs.BoolVar(&clear, "clear", false, "remove every property")
		},
		meta: commandMeta{
			Resource: "artifact", Scenarios: []string{"ci"}, Stage: "m2",
			Endpoints:   []string{"PUT " + repositoriesPath + "/{repositoryId}/packages/{name}/versions/{version}/properties"},
			Mutating:    true,
			MinimumRole: "repositories:write", OutputSchema: "RawVersion", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := id.validate(true); err != nil {
				return err
			}
			if clear == (len(properties) > 0) {
				return &UsageError{Message: "pass either --property or --clear", Hint: "the properties are replaced as a whole, so an empty map needs --clear"}
			}
			props, err := parseGenericProperties(properties)
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"properties": props})
			client, err := env.client()
			if err != nil {
				return err
			}
			var version adminopenapi.RawVersion
			if _, err = client.sendJSON(ctx, http.MethodPut, id.versionPath()+"/properties", requestOptions{idempotent: true, body: payload}, &version); err != nil {
				return err
			}
			return env.render(version, nil, func(w io.Writer) error { return renderGenericVersion(w, version) })
		},
	}
}
