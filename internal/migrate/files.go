package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// File is one migration file ready to apply.
type File struct {
	Name string
	// Checksum is the SHA-256 of the whole file, as recorded in the ledger.
	Checksum string
	// Body is the file without its "-- +goose Down" section.
	Body string
}

// LoadDir reads the *.sql files of dir in name order.
func LoadDir(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var files []File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.ContainsFunc(name, func(r rune) bool { return !validNameRune(r) }) {
			return nil, fmt.Errorf("unsupported migration filename: %s", name)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		files = append(files, File{Name: name, Checksum: checksum(data), Body: StripDown(string(data))})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no *.sql migration files", dir)
	}
	return files, nil
}

func validNameRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '_' || r == '-'
}
