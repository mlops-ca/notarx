package backupops

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// legacyEnvExample is the env example name of pre-fork archives (spelled in parts:
// the identity gate rejects the literal).
const legacyEnvExample = "artifact" + "-gateway.env.example"

// NativeArchive pins an original local distribution archive approved outside
// the backup bundle. Its SHA256 must come from the operator's trusted release
// provenance, never from the archive, VERSION.txt, or backup manifest itself.
// Verification is offline and does not extract files or execute software.
type NativeArchive struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

const (
	maxNativeArchiveBytes int64 = 512 << 20
	maxNativeTarBytes     int64 = 1 << 30
	maxNativeEntryBytes   int64 = 256 << 20
	maxNativeEntries            = 4096
)

var fullSHA256 = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func verifyNativeArchive(release Release, migrations map[string]string) error {
	a := release.NativeArchive
	if !filepath.IsAbs(a.Path) || !fullSHA256.MatchString(a.SHA256) {
		return errRelease
	}
	info, err := os.Lstat(a.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxNativeArchiveBytes {
		return errRelease
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return errRelease
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errRelease
	}
	sum, n, err := hashReader(io.LimitReader(f, maxNativeArchiveBytes+1))
	if err != nil || n != info.Size() || sum != a.SHA256 {
		return errRelease
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return errRelease
	}
	compressed := bufio.NewReader(f)
	z, err := gzip.NewReader(compressed)
	if err != nil {
		return errRelease
	}
	z.Multistream(false)
	defer func() { _ = z.Close() }()
	// Count decoded bytes too, including headers/padding and ignored entries.
	bounded := &io.LimitedReader{R: z, N: maxNativeTarBytes + 1}
	tr := tar.NewReader(bounded)
	seen := map[string]bool{}
	archivedMigrations := map[string]string{}
	binary, version, seenBinary := "", "", false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil || len(seen) >= maxNativeEntries || h.Format != tar.FormatUSTAR || len(h.Name) > 512 || h.Size < 0 || h.Size > maxNativeEntryBytes {
			return errRelease
		}
		// The publisher uses one flat root with optional './'. Accept no
		// alternate roots, aliases, path traversal, backslashes, or links.
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "" {
			name = "."
		}
		if strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || seen[name] {
			return errRelease
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 || (name != "." && name != "migrations") {
				return errRelease
			}
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Linkname != "" || strings.HasSuffix(h.Name, "/") {
			return errRelease
		}
		switch {
		case name == "VERSION.txt":
			if h.Size > 4096 {
				return errRelease
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				return errRelease
			}
			version = string(body)
		case name == "notarx" || name == "gateway":
			if seenBinary {
				return errRelease
			}
			seenBinary = true
			binary, _, err = hashReader(tr)
			if err != nil {
				return errRelease
			}
		case strings.HasPrefix(name, "migrations/") && migrationFilename.MatchString(strings.TrimPrefix(name, "migrations/")):
			if h.Size > 4<<20 {
				return errRelease
			}
			digest, _, err := hashReader(tr)
			if err != nil {
				return errRelease
			}
			archivedMigrations[strings.TrimPrefix(name, "migrations/")] = strings.TrimPrefix(digest, "sha256:")
		// Pre-fork archives ship "gateway-healthcheck" and the upstream-named env
		// example; notarx archives ship the notarx names.
		case name == "gateway-healthcheck" || name == "notarx-healthcheck" || name == "run-migrations.sh" || name == "notarx.env.example" || name == legacyEnvExample || name == "INSTALL.txt":
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return errRelease
			}
		default:
			return errRelease
		}
	}
	// Consume the gzip trailer (CRC/truncation) and any trailing decoded bytes
	// under the same cap. Multiple gzip members and nonzero tar suffixes fail.
	tail := make([]byte, 4096)
	for {
		n, err := bounded.Read(tail)
		for _, b := range tail[:n] {
			if b != 0 {
				return errRelease
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil || bounded.N == 0 {
			return errRelease
		}
	}
	if bounded.N <= 0 {
		return errRelease
	}
	if _, err := compressed.ReadByte(); err != io.EOF {
		return errRelease
	}
	g := release.Identity
	if binary != g.Artifact.SHA256 || version != "version="+g.Version+"\nrevision="+g.Revision+"\ntarget="+g.Artifact.Platform+"\n" || len(archivedMigrations) != len(migrations) {
		return errRelease
	}
	for name, digest := range migrations {
		if archivedMigrations[name] != digest {
			return errRelease
		}
	}
	return nil
}
