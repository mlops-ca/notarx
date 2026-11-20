// Package huggingface implements the Hugging Face model and dataset repository
// format (ADR 0027). A repository is Hosted, Proxy or Group; the coordinate of an
// artifact is <repo_type>/<org>/<name>@<commit_sha>, a commit being immutable.
// Branches, tags and main are mutable refs resolved to a commit by the store.
package huggingface

import (
	"regexp"
	"strings"
)

// Repo types of the Hub paths. Spaces are not supported (ADR 0027).
const (
	RepoTypeModel   = "model"
	RepoTypeDataset = "dataset"
)

// MaxPathLength bounds a file path of a commit: hf_files.path is VARCHAR(512), so the
// primary key stays inside the 3072-byte MySQL key limit (ADR 0022). A longer path is
// rejected (hosted 400 path_too_long, proxy 502 upstream_path_too_long).
const MaxPathLength = 512

var (
	commitPattern = regexp.MustCompile("^[0-9a-f]{40}$")
	digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,95}$`)
)

// ValidRepoType reports whether t names a supported repo type.
func ValidRepoType(t string) bool { return t == RepoTypeModel || t == RepoTypeDataset }

// ValidCommit reports whether sha is a full 40 character lower-case git commit id.
func ValidCommit(sha string) bool { return commitPattern.MatchString(sha) }

// ValidDigest reports whether sha256 is a 64 character lower-case hexadecimal digest.
func ValidDigest(sha256 string) bool { return digestPattern.MatchString(sha256) }

// ValidNamePart reports whether s is a valid organisation or repository name: the Hub
// alphabet, at most 96 characters, with no repeated separator.
func ValidNamePart(s string) bool {
	return namePattern.MatchString(s) && !strings.Contains(s, "--") && !strings.Contains(s, "..") &&
		!strings.HasSuffix(s, "-") && !strings.HasSuffix(s, ".")
}

// ValidRepoName reports whether name is a Hub repo id of the form org/name.
func ValidRepoName(name string) bool {
	org, repo, ok := strings.Cut(name, "/")
	return ok && ValidNamePart(org) && ValidNamePart(repo)
}

// ValidPath reports whether p is a safe relative file path of a commit: bounded, no
// leading or trailing slash, no empty, dot or dot-dot segment, no backslash or control
// character.
func ValidPath(p string) bool {
	if p == "" || len([]rune(p)) > MaxPathLength || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsRune(p, '\\') {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// Coordinate is a parsed artifact coordinate.
type Coordinate struct {
	RepoType string
	Name     string // org/name
	Commit   string
}

// String renders the canonical coordinate.
func (c Coordinate) String() string { return c.RepoType + "/" + c.Name + "@" + c.Commit }

// ParseCoordinate parses <repo_type>/<org>/<name>@<commit_sha>. Only the commit form is
// a coordinate: a branch, tag or short sha is a ref, not an artifact identity.
func ParseCoordinate(value string) (Coordinate, bool) {
	head, commit, ok := strings.Cut(value, "@")
	if !ok || !ValidCommit(commit) {
		return Coordinate{}, false
	}
	repoType, name, ok := strings.Cut(head, "/")
	if !ok || !ValidRepoType(repoType) || !ValidRepoName(name) {
		return Coordinate{}, false
	}
	return Coordinate{RepoType: repoType, Name: name, Commit: commit}, true
}
