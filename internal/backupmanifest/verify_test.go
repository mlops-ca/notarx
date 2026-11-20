package backupmanifest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ptr(n int64) *int64 { return &n }

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeFixtureFile(t *testing.T, dir, name string, body []byte) File {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return File{Path: name, Size: ptr(int64(len(body))), SHA256: digest(body)}
}

func fixture(t *testing.T) (string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	m := Manifest{
		SchemaVersion: 1, BackupID: "synthetic-backup", State: "complete", StartedAt: start, CompletedAt: start.Add(time.Minute),
		Gateway:  GatewayIdentity{Version: "synthetic-v1", Revision: strings.Repeat("a", 40), ImageDigest: "sha256:" + strings.Repeat("b", 64)},
		Database: DatabaseExport{Format: "pg-custom-v1", Dialect: DialectPostgres, File: writeFixtureFile(t, dir, "database.dump", []byte("PGDMP-synthetic-not-a-restorable-dump"))},
		Schema:   SchemaExport{Format: "notarx-migrations-tsv-v1", File: writeFixtureFile(t, dir, "migrations.tsv", []byte("filename\tsha256\n000000_schema_history.sql\t"+strings.Repeat("c", 64)+"\n"))},
		Writers:  WriterEvidence{ScopeID: "synthetic-scope", InventoryDeclaredComplete: true, Writers: []Writer{{ID: "synthetic-api", StopConfirmed: true, StoppedAt: start.Add(-time.Minute), ConfirmedThrough: start.Add(2 * time.Minute)}}},
	}
	first := writeFixtureFile(t, dir, "objects/one.bin", []byte("abc"))
	empty := writeFixtureFile(t, dir, "objects/zero.bin", nil)
	var inventory []byte
	for _, row := range []Object{{Key: "synthetic/a", File: first}, {Key: "synthetic/b", File: empty}} {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		inventory = append(inventory, append(encoded, '\n')...)
	}
	m.Objects = ObjectExport{Format: "s3-bytes-v1", Count: ptr(2), Bytes: ptr(3), Inventory: writeFixtureFile(t, dir, "objects.jsonl", inventory)}
	return dir, m
}

func TestVerifiedBytesDoNotProveConsistentBackup(t *testing.T) {
	dir, m := fixture(t)
	r := Verify(context.Background(), dir, m)
	if r.Status != Unknown || r.Integrity != Verified || r.Consistency != Unknown || r.WriterEvidence != "declared" || r.VerifiedObjects != 2 || r.VerifiedObjectBytes != 3 || r.VerifiedMigrations != 1 {
		t.Fatalf("offline result=%+v", r)
	}
	if !strings.Contains(strings.Join(r.Reasons, ","), "consistency_not_independently_verified") {
		t.Fatal("missing consistency boundary")
	}
}

// A manifest written before ADR 0022 has no dialect and is a PostgreSQL backup, so it must verify as before.
func TestManifestWithoutDialectVerifiesAsPostgres(t *testing.T) {
	dir, m := fixture(t)
	m.Database.Dialect = ""
	raw, err := json.Marshal(m)
	if err != nil || strings.Contains(string(raw), "dialect") {
		t.Fatalf("an empty dialect must not be written: %v %s", err, raw)
	}
	r := Verify(context.Background(), dir, m)
	if r.Integrity != Verified {
		t.Fatalf("a manifest without a dialect no longer verifies: %+v", r)
	}
}

func TestWriterDeclarationsCannotHideIncompleteScopeOrTimeCoverage(t *testing.T) {
	cases := map[string]func(*Manifest){
		"no writers":                func(m *Manifest) { m.Writers.Writers = nil },
		"scope incomplete":          func(m *Manifest) { m.Writers.InventoryDeclaredComplete = false },
		"scope missing":             func(m *Manifest) { m.Writers.ScopeID = "" },
		"second unconfirmed writer": func(m *Manifest) { m.Writers.Writers = append(m.Writers.Writers, Writer{ID: "synthetic-worker"}) },
		"late stop":                 func(m *Manifest) { m.Writers.Writers[0].StoppedAt = m.StartedAt.Add(time.Second) },
		"early coverage end":        func(m *Manifest) { m.Writers.Writers[0].ConfirmedThrough = m.CompletedAt.Add(-time.Second) },
		"duplicate writer":          func(m *Manifest) { m.Writers.Writers = append(m.Writers.Writers, m.Writers.Writers[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := fixture(t)
			mutate(&m)
			r := Verify(context.Background(), dir, m)
			if r.WriterEvidence == "declared" || r.Status == Verified || r.Consistency != Unknown {
				t.Fatalf("unsupported writer claim accepted: %+v", r)
			}
		})
	}
}

func TestMissingChangedAndInterruptedArtifactsAreInvalid(t *testing.T) {
	cases := map[string]func(*testing.T, string, *Manifest){
		"same length wrong bytes": func(t *testing.T, dir string, _ *Manifest) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "objects/one.bin"), []byte("xyz"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"missing object": func(t *testing.T, dir string, _ *Manifest) {
			t.Helper()
			if err := os.Remove(filepath.Join(dir, "objects/one.bin")); err != nil {
				t.Fatal(err)
			}
		},
		"short object": func(t *testing.T, dir string, _ *Manifest) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "objects/one.bin"), []byte("ab"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"extra object byte": func(t *testing.T, dir string, _ *Manifest) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "objects/one.bin"), []byte("abcd"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"dump mismatch": func(t *testing.T, _ string, m *Manifest) {
			t.Helper()
			m.Database.File.SHA256 = "sha256:" + strings.Repeat("f", 64)
		},
		"inventory mismatch": func(t *testing.T, _ string, m *Manifest) {
			t.Helper()
			m.Objects.Inventory.SHA256 = "sha256:" + strings.Repeat("f", 64)
		},
		"count mismatch": func(t *testing.T, _ string, m *Manifest) { t.Helper(); m.Objects.Count = ptr(3) },
		"bytes mismatch": func(t *testing.T, _ string, m *Manifest) { t.Helper(); m.Objects.Bytes = ptr(4) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := fixture(t)
			mutate(t, dir, &m)
			r := Verify(context.Background(), dir, m)
			if r.Status != Invalid || r.Integrity != Invalid || r.Consistency != Unknown {
				t.Fatalf("damaged backup accepted: %+v", r)
			}
		})
	}
}

func TestUnknownVersionsAndQuantitiesRemainUnknown(t *testing.T) {
	cases := map[string]func(*Manifest){
		"manifest version":     func(m *Manifest) { m.SchemaVersion = 2 },
		"schema format":        func(m *Manifest) { m.Schema.Format = "future-schema-v2" },
		"database dialect":     func(m *Manifest) { m.Database.Dialect = "mysql" },
		"legacy schema format": func(m *Manifest) { m.Schema.Format = "gateway-migrations-tsv-v1" },
		"export format":        func(m *Manifest) { m.Objects.Format = "rustfs-physical-v1" },
		"incomplete set":       func(m *Manifest) { m.State = "incomplete" },
		"missing count":        func(m *Manifest) { m.Objects.Count = nil },
		"missing bytes":        func(m *Manifest) { m.Objects.Bytes = nil },
		"missing size":         func(m *Manifest) { m.Database.File.Size = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := fixture(t)
			mutate(&m)
			if r := Verify(context.Background(), dir, m); r.Status != Unknown || r.Integrity == Verified || r.Consistency != Unknown {
				t.Fatalf("unknown treated as a valid set: %+v", r)
			}
		})
	}
}

func TestArtifactPathsAndPermissionsStayInsidePrivateBundle(t *testing.T) {
	for _, path := range []string{"../synthetic-sensitive-marker", "/synthetic-sensitive-marker", "objects/../database.dump", "objects\\outside"} {
		t.Run(path, func(t *testing.T) {
			dir, m := fixture(t)
			m.Database.File.Path = path
			r := Verify(context.Background(), dir, m)
			encoded, _ := json.Marshal(r)
			if r.Status != Invalid || strings.Contains(string(encoded), "synthetic-sensitive-marker") {
				t.Fatalf("unsafe artifact path result=%s", encoded)
			}
		})
	}
	t.Run("external symlink", func(t *testing.T) {
		dir, m := fixture(t)
		outside := filepath.Join(t.TempDir(), "synthetic-sensitive-marker")
		if err := os.WriteFile(outside, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "objects/one.bin")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "objects/one.bin")); err != nil {
			t.Fatal(err)
		}
		if r := Verify(context.Background(), dir, m); r.Status != Invalid {
			t.Fatalf("symlink accepted: %+v", r)
		}
	})
	t.Run("public artifact", func(t *testing.T) {
		dir, m := fixture(t)
		if err := os.Chmod(filepath.Join(dir, "database.dump"), 0644); err != nil {
			t.Fatal(err)
		}
		if r := Verify(context.Background(), dir, m); r.Status != Invalid {
			t.Fatalf("public dump accepted: %+v", r)
		}
	})
}

func TestVerificationCancellationAndInputImmutability(t *testing.T) {
	dir, m := fixture(t)
	before, _ := json.Marshal(m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Verify(ctx, dir, m)
	after, _ := json.Marshal(m)
	if r.Status != Unknown || r.Integrity == Verified || string(before) != string(after) {
		t.Fatalf("cancelled/mutated result=%+v", r)
	}
}

func TestInventoryRecordsAreBoundedUnambiguousAndComplete(t *testing.T) {
	for name, body := range map[string]string{
		"blank line":                 "\n",
		"malformed":                  `{"key":"synthetic-sensitive-marker"`,
		"duplicate alias":            `{"key":"synthetic-sensitive-marker","Key":"other"}`,
		"Unicode alias":              `{"key":"synthetic-sensitive-marker","Key":"other"}`,
		"unknown field":              `{"unexpected":"synthetic-sensitive-marker"}`,
		"invalid UTF8":               "{\"key\":\"\xff\"}",
		"ill formed escaped Unicode": `{"key":"\ud800"}`,
		"line limit":                 strings.Repeat(" ", (64<<10)+1),
		"null":                       "null",
	} {
		t.Run(name, func(t *testing.T) {
			dir, m := fixture(t)
			m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", []byte(body+"\n"))
			r := Verify(context.Background(), dir, m)
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != Invalid || r.Integrity != Invalid || r.Consistency != Unknown || strings.Contains(string(encoded), "synthetic-sensitive-marker") {
				t.Fatalf("result=%s", encoded)
			}
		})
	}
	for _, order := range [][]string{{"b", "a"}, {"a", "a"}} {
		dir, m := fixture(t)
		file := writeFixtureFile(t, dir, "objects/synthetic.bin", []byte("abc"))
		var body []byte
		for _, key := range order {
			row, err := json.Marshal(Object{Key: key, File: file})
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, append(row, '\n')...)
		}
		m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", body)
		m.Objects.Bytes = ptr(6)
		if r := Verify(context.Background(), dir, m); r.Status != Invalid {
			t.Fatalf("unordered/duplicate keys accepted: %+v", r)
		}
	}
}

func TestMigrationLedgerVersionRowsAndDigestMustVerify(t *testing.T) {
	for _, body := range []string{
		"filename\tsha256\n", "filename\tsha256\n000000_synthetic.sql\tnot-a-sha256\n",
		"future header\n", "filename\tsha256\n" + strings.Repeat("x", (64<<10)+1),
		fmt.Sprintf("filename\tsha256\n000002_second.sql\t%s\n000001_first.sql\t%s\n", strings.Repeat("a", 64), strings.Repeat("b", 64)),
	} {
		dir, m := fixture(t)
		m.Schema.File = writeFixtureFile(t, dir, "migrations.tsv", []byte(body))
		if r := Verify(context.Background(), dir, m); r.Status != Invalid {
			t.Fatalf("invalid ledger accepted: %+v", r)
		}
	}
	dir, m := fixture(t)
	m.Schema.File.SHA256 = "sha256:" + strings.Repeat("f", 64)
	if r := Verify(context.Background(), dir, m); r.Status != Invalid || r.VerifiedMigrations != 0 {
		t.Fatalf("unverified ledger rows counted as verified: %+v", r)
	}
}

func TestEmptyObjectsAndMultipleConfirmedWritersStayUnknown(t *testing.T) {
	dir, m := fixture(t)
	m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", nil)
	m.Objects.Count, m.Objects.Bytes = ptr(0), ptr(0)
	second := m.Writers.Writers[0]
	second.ID = "synthetic-worker"
	m.Writers.Writers = append(m.Writers.Writers, second)
	if r := Verify(context.Background(), dir, m); r.Status != Unknown || r.Integrity != Verified || r.WriterEvidence != "declared" || r.VerifiedObjects != 0 || r.VerifiedObjectBytes != 0 {
		t.Fatalf("empty/multi-writer result=%+v", r)
	}
	m.Writers.Writers[0].ConfirmedThrough = time.Now().Add(time.Hour)
	if r := Verify(context.Background(), dir, m); r.WriterEvidence == "declared" || r.Status != Invalid {
		t.Fatalf("future interval declared confirmed: %+v", r)
	}
}

func TestNestedDirectoryEscapeAndPublicRootAreRejected(t *testing.T) {
	dir, m := fixture(t)
	outside := t.TempDir()
	if err := os.Rename(filepath.Join(dir, "objects"), filepath.Join(outside, "objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "objects"), filepath.Join(dir, "objects")); err != nil {
		t.Fatal(err)
	}
	if r := Verify(context.Background(), dir, m); r.Status != Invalid {
		t.Fatalf("nested escape accepted: %+v", r)
	}
	dir, m = fixture(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if r := Verify(context.Background(), dir, m); r.Status != Invalid {
		t.Fatalf("public root accepted: %+v", r)
	}
}

type cancellingContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *cancellingContext) Err() error {
	if c.checks.Add(1) == 8 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFullObjectBytesAndCancellationDuringVerification(t *testing.T) {
	dir, m := fixture(t)
	body := []byte(strings.Repeat("synthetic", 256<<10))
	file := writeFixtureFile(t, dir, "objects/streamed.bin", body)
	row, err := json.Marshal(Object{Key: "synthetic/large", File: file})
	if err != nil {
		t.Fatal(err)
	}
	m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", append(row, '\n'))
	m.Objects.Count, m.Objects.Bytes = ptr(1), ptr(int64(len(body)))
	if r := Verify(context.Background(), dir, m); r.Integrity != Verified || r.VerifiedObjectBytes != int64(len(body)) || r.Status != Unknown {
		t.Fatalf("streamed result=%+v", r)
	}
	body[len(body)-1] ^= 1
	if err := os.WriteFile(filepath.Join(dir, file.Path), body, 0600); err != nil {
		t.Fatal(err)
	}
	if r := Verify(context.Background(), dir, m); r.Status != Invalid || r.VerifiedObjects != 0 {
		t.Fatalf("tail corruption accepted: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	during := &cancellingContext{Context: ctx, cancel: cancel}
	if r := Verify(during, dir, m); r.Status != Unknown || r.Integrity == Verified || !strings.Contains(strings.Join(r.Reasons, ","), "cancelled") {
		t.Fatalf("mid-check cancellation=%+v", r)
	}
}

func TestMetadataLimitsAndIncompleteObjectQuantitiesAreNotVerified(t *testing.T) {
	for _, schema := range []bool{true, false} {
		dir, m := fixture(t)
		if schema {
			m.Schema.File.Size = ptr((64 << 20) + 1)
		} else {
			m.Objects.Inventory.Size = ptr((64 << 20) + 1)
		}
		if r := Verify(context.Background(), dir, m); r.Status != Invalid || r.Integrity != Invalid {
			t.Fatalf("unbounded metadata accepted: %+v", r)
		}
	}
	for _, size := range []*int64{nil, ptr(-1)} {
		dir, m := fixture(t)
		file := writeFixtureFile(t, dir, "objects/synthetic.bin", nil)
		file.Size = size
		row, err := json.Marshal(Object{Key: "synthetic/zero", File: file})
		if err != nil {
			t.Fatal(err)
		}
		m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", append(row, '\n'))
		m.Objects.Count, m.Objects.Bytes = ptr(1), ptr(0)
		if r := Verify(context.Background(), dir, m); r.Status != Invalid || r.VerifiedObjects != 0 {
			t.Fatalf("missing/negative bytes counted as zero: %+v", r)
		}
	}
}

// fsFixture builds an fs-bytes-v1 bundle with two tar segments. The objects are
// a/1 ("abc") and a/2 (empty) in segment 1, and b/1 ("hello world") in segment 2.
func fsFixture(t *testing.T) (string, Manifest) {
	t.Helper()
	dir, m := fixture(t)
	type entry struct {
		key  string
		body []byte
	}
	var segments []File
	var inventory []byte
	write := func(name string, entries []entry) {
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		var rows []Object
		for _, e := range entries {
			if err := writer.WriteHeader(&tar.Header{Name: e.key, Mode: 0o600, Size: int64(len(e.body)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
				t.Fatal(err)
			}
			offset := int64(buffer.Len())
			if _, err := writer.Write(e.body); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, Object{Key: e.key, File: File{Path: name, Size: ptr(int64(len(e.body))), SHA256: digest(e.body)}, Offset: ptr(offset)})
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		segments = append(segments, writeFixtureFile(t, dir, name, buffer.Bytes()))
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			inventory = append(inventory, append(encoded, '\n')...)
		}
	}
	write("objects/segment-000001.tar", []entry{{"a/1", []byte("abc")}, {"a/2", nil}})
	write("objects/segment-000002.tar", []entry{{"b/1", []byte("hello world")}})
	m.Objects = ObjectExport{Format: FormatFSBytes, Count: ptr(3), Bytes: ptr(14), Segments: segments, Inventory: writeFixtureFile(t, dir, "objects.jsonl", inventory)}
	return dir, m
}

func rewriteInventory(t *testing.T, dir string, m *Manifest, mutate func([]Object) []Object) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "objects.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Object
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var row Object
		if err = json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	var body []byte
	for _, row := range mutate(rows) {
		encoded, marshalErr := json.Marshal(row)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		body = append(body, append(encoded, '\n')...)
	}
	if err = os.Remove(filepath.Join(dir, "objects.jsonl")); err != nil {
		t.Fatal(err)
	}
	m.Objects.Inventory = writeFixtureFile(t, dir, "objects.jsonl", body)
}

func TestFSBytesSegmentsAreVerifiedWholeAndCounted(t *testing.T) {
	dir, m := fsFixture(t)
	r := Verify(context.Background(), dir, m)
	if r.Integrity != Verified || r.VerifiedObjects != 3 || r.VerifiedObjectBytes != 14 {
		t.Fatalf("report=%+v", r)
	}
	if r.Consistency != Unknown {
		t.Fatalf("verified bytes must not prove consistency: %+v", r)
	}
}

func TestFSBytesRejectsDamagedOrUndeclaredSegments(t *testing.T) {
	t.Run("changed segment bytes", func(t *testing.T) {
		dir, m := fsFixture(t)
		path := filepath.Join(dir, "objects/segment-000002.tar")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[600] ^= 0xff
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if r := Verify(context.Background(), dir, m); r.Integrity != Invalid {
			t.Fatalf("a damaged segment verified: %+v", r)
		}
	})
	t.Run("missing segment", func(t *testing.T) {
		dir, m := fsFixture(t)
		if err := os.Remove(filepath.Join(dir, "objects/segment-000001.tar")); err != nil {
			t.Fatal(err)
		}
		if r := Verify(context.Background(), dir, m); r.Integrity != Invalid {
			t.Fatalf("a missing segment verified: %+v", r)
		}
	})
	t.Run("segment list out of order", func(t *testing.T) {
		dir, m := fsFixture(t)
		m.Objects.Segments[0], m.Objects.Segments[1] = m.Objects.Segments[1], m.Objects.Segments[0]
		if r := Verify(context.Background(), dir, m); r.Integrity != Invalid {
			t.Fatalf("unordered segments verified: %+v", r)
		}
	})
	t.Run("segment path outside the pattern", func(t *testing.T) {
		for _, name := range []string{"objects/other.tar", "objects/segment-1.tar", "objects/../database.dump", "segment-000001.tar"} {
			dir, m := fsFixture(t)
			m.Objects.Segments[0].Path = name
			if r := Verify(context.Background(), dir, m); r.Integrity != Invalid {
				t.Fatalf("segment path %q verified: %+v", name, r)
			}
		}
	})
	t.Run("s3 format may not declare segments", func(t *testing.T) {
		dir, m := fixture(t)
		m.Objects.Segments = []File{writeFixtureFile(t, dir, "objects/segment-000001.tar", []byte("x"))}
		if r := Verify(context.Background(), dir, m); r.Integrity == Verified {
			t.Fatalf("s3-bytes-v1 with segments verified: %+v", r)
		}
	})
}

func TestFSBytesInventoryMustStayInsideTheDeclaredSegments(t *testing.T) {
	cases := map[string]func([]Object) []Object{
		"unknown segment":     func(rows []Object) []Object { rows[0].File.Path = "objects/segment-000009.tar"; return rows },
		"missing offset":      func(rows []Object) []Object { rows[0].Offset = nil; return rows },
		"offset in header":    func(rows []Object) []Object { rows[0].Offset = ptr(100); return rows },
		"negative offset":     func(rows []Object) []Object { rows[0].Offset = ptr(-1); return rows },
		"past segment end":    func(rows []Object) []Object { rows[2].Offset = ptr(*rows[2].Offset + 4096); return rows },
		"size past the end":   func(rows []Object) []Object { rows[2].File.Size = ptr(1 << 20); return rows },
		"overlapping objects": func(rows []Object) []Object { rows[1].Offset = rows[0].Offset; rows[1].File.Size = ptr(1); return rows },
		"malformed digest":    func(rows []Object) []Object { rows[0].File.SHA256 = "sha256:abc"; return rows },
		"segments reordered": func(rows []Object) []Object {
			rows[0].File.Path = "objects/segment-000002.tar"
			return rows
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := fsFixture(t)
			rewriteInventory(t, dir, &m, mutate)
			if r := Verify(context.Background(), dir, m); r.Integrity == Verified {
				t.Fatalf("an inconsistent inventory verified: %+v", r)
			}
		})
	}
	t.Run("totals must match", func(t *testing.T) {
		dir, m := fsFixture(t)
		m.Objects.Bytes = ptr(15)
		if r := Verify(context.Background(), dir, m); r.Integrity == Verified {
			t.Fatalf("wrong byte total verified: %+v", r)
		}
	})
}

func TestS3BytesInventoryRejectsOffsets(t *testing.T) {
	dir, m := fixture(t)
	rewriteInventory(t, dir, &m, func(rows []Object) []Object { rows[0].Offset = ptr(512); return rows })
	if r := Verify(context.Background(), dir, m); r.Integrity == Verified {
		t.Fatalf("an s3-bytes-v1 inventory with offsets verified: %+v", r)
	}
}

func TestEmptyFSBytesExportHasNoSegments(t *testing.T) {
	dir, m := fixture(t)
	m.Objects = ObjectExport{Format: FormatFSBytes, Count: ptr(0), Bytes: ptr(0), Inventory: writeFixtureFile(t, dir, "objects-empty.jsonl", nil)}
	if r := Verify(context.Background(), dir, m); r.Integrity != Verified || r.VerifiedObjects != 0 {
		t.Fatalf("an empty filesystem export did not verify: %+v", r)
	}
}

func TestFSBytesRootIDMustBeAWellFormedIdentifier(t *testing.T) {
	dir, m := fsFixture(t)
	m.Objects.RootID = "5e1625e7-9c7b-42b8-9b83-c9a1fc242662"
	if r := Verify(context.Background(), dir, m); r.Integrity != Verified {
		t.Fatalf("a valid root ID was refused: %+v", r)
	}
	for _, bad := range []string{"not-a-uuid", "5E1625E7-9C7B-42B8-9B83-C9A1FC242662", "../etc"} {
		m.Objects.RootID = bad
		if r := Verify(context.Background(), dir, m); r.Integrity == Verified {
			t.Fatalf("root ID %q verified", bad)
		}
	}
	s3dir, s3m := fixture(t)
	s3m.Objects.RootID = "5e1625e7-9c7b-42b8-9b83-c9a1fc242662"
	if r := Verify(context.Background(), s3dir, s3m); r.Integrity == Verified {
		t.Fatalf("an s3-bytes-v1 export may not carry a root ID: %+v", r)
	}
}
