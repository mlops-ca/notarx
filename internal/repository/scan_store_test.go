package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// testScanStoreContract runs against every ScanStore implementation (Memory and PostgreSQL).
func testScanStoreContract(t *testing.T, store ScanStore) {
	t.Helper()
	ctx := context.Background()
	repo := ids.New()
	digest := "sha256:" + ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("sbom upsert keeps identity and lists by kind", func(t *testing.T) {
		first, err := store.PutScanSBOM(ctx, ScanSBOM{RepositoryID: repo, ArtifactDigest: digest, Kind: ScanSBOMKindCycloneDX, Source: ScanSBOMSourceGenerated,
			Generator: "trivy", BodyDigest: "sha256:aa", BodySize: 2, Body: []byte(`{}`), ComponentCount: 1})
		if err != nil || first.ID == "" || first.CreatedAt.IsZero() {
			t.Fatalf("put = %+v, %v", first, err)
		}
		second, err := store.PutScanSBOM(ctx, ScanSBOM{RepositoryID: repo, ArtifactDigest: digest, Kind: ScanSBOMKindCycloneDX, Source: ScanSBOMSourceGenerated,
			BodyDigest: "sha256:bb", BodySize: 9, BodyKey: "meta/sha256/bb", ComponentCount: 7})
		if err != nil || second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
			t.Fatalf("replace must keep id and created_at: %+v, %v", second, err)
		}
		if _, err := store.PutScanSBOM(ctx, ScanSBOM{RepositoryID: repo, ArtifactDigest: digest, Kind: ScanSBOMKindSPDX, Source: ScanSBOMSourceUploaded,
			BodyDigest: "sha256:cc", BodyKey: "meta/sha256/cc"}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetScanSBOM(ctx, repo, digest, ScanSBOMKindCycloneDX, ScanSBOMSourceGenerated)
		if err != nil || got.BodyDigest != "sha256:bb" || got.BodyKey != "meta/sha256/bb" || len(got.Body) != 0 || got.ComponentCount != 7 {
			t.Fatalf("get = %+v, %v", got, err)
		}
		listed, err := store.ListScanSBOMs(ctx, repo, digest)
		if err != nil || len(listed) != 2 || listed[0].Kind != ScanSBOMKindCycloneDX || listed[1].Kind != ScanSBOMKindSPDX {
			t.Fatalf("list = %+v, %v", listed, err)
		}
		if _, err := store.GetScanSBOM(ctx, repo, digest, ScanSBOMKindSPDX, ScanSBOMSourcePromoted); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing sbom err = %v", err)
		}
		for _, bad := range []ScanSBOM{
			{RepositoryID: "nope", ArtifactDigest: digest, Kind: ScanSBOMKindSPDX, Source: ScanSBOMSourceUploaded, BodyDigest: "d", BodyKey: "k"},
			{RepositoryID: repo, ArtifactDigest: digest, Kind: "xml", Source: ScanSBOMSourceUploaded, BodyDigest: "d", BodyKey: "k"},
			{RepositoryID: repo, ArtifactDigest: digest, Kind: ScanSBOMKindSPDX, Source: ScanSBOMSourceUploaded, BodyDigest: "d"},
		} {
			if _, err := store.PutScanSBOM(ctx, bad); !errors.Is(err, ErrScanInvalid) {
				t.Fatalf("%+v accepted: %v", bad, err)
			}
		}
	})

	t.Run("100000 findings are inserted in batches, swapped and swept", func(t *testing.T) {
		const total = 100000
		artifact := "sha256:" + ids.New()
		old := ids.New()
		insertScanFindings(t, store, old, 10)
		if _, err := store.SwapScanResult(ctx, ScanResult{ID: old, RepositoryID: repo, ArtifactDigest: artifact, Scanner: "trivy", Status: ScanStatusVulnerable, High: 10, FindingsTotal: 10, ScannedAt: now}); err != nil {
			t.Fatal(err)
		}
		fresh := ids.New()
		insertScanFindings(t, store, fresh, total)
		if got, err := store.GetScanResult(ctx, repo, artifact); err != nil || got.ID != old {
			t.Fatalf("findings must not be visible before the swap: %+v, %v", got, err)
		}
		swap, err := store.SwapScanResult(ctx, ScanResult{ID: fresh, RepositoryID: repo, ArtifactDigest: artifact, Scanner: "trivy", Status: ScanStatusVulnerable,
			High: total, FindingsTotal: total, ScannedAt: now.Add(time.Minute)})
		if err != nil || swap.PreviousID != old || !swap.HistoryAppended {
			t.Fatalf("swap = %+v, %v", swap, err)
		}
		if _, err := store.SweepScanFindings(ctx, fresh, 10); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("the current id must not be swept: %v", err)
		}
		swept := 0
		for {
			n, err := store.SweepScanFindings(ctx, old, 4)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
			if n > 4 {
				t.Fatalf("batch of %d exceeds the limit", n)
			}
			swept += n
		}
		if swept != 10 {
			t.Fatalf("swept %d, want 10", swept)
		}
		if rows, err := store.ListScanFindings(ctx, old, nil, 100); err != nil || len(rows) != 0 {
			t.Fatalf("old findings = %d, %v", len(rows), err)
		}
		var after *ScanFindingCursor
		seen := 0
		for {
			page, err := store.ListScanFindings(ctx, fresh, after, 10000)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			seen += len(page)
			last := page[len(page)-1].Cursor()
			after = &last
		}
		if seen != total {
			t.Fatalf("paged %d findings, want %d", seen, total)
		}
	})

	t.Run("findings page worst first and refuse oversized batches", func(t *testing.T) {
		id := ids.New()
		score := 9.8
		kev := true
		batch := []ScanFinding{
			{VulnerabilityID: id, Ordinal: 0, VulnID: "CVE-2024-0003", Severity: "low", Status: ScanFindingAffected},
			{VulnerabilityID: id, Ordinal: 1, VulnID: "CVE-2024-0002", Severity: "critical", Status: ScanFindingAffected, CVSSScore: &score, KEV: &kev,
				Description: "d", VEXStatus: "not_affected", VEXJustification: "vulnerable_code_not_present", Component: "pkg", FixedVersion: "2"},
			{VulnerabilityID: id, Ordinal: 2, VulnID: "CVE-2024-0001", Severity: "critical", Status: ScanFindingSuppressed},
		}
		if err := store.InsertScanFindings(ctx, batch); err != nil {
			t.Fatal(err)
		}
		page, err := store.ListScanFindings(ctx, id, nil, 2)
		if err != nil || len(page) != 2 || page[0].VulnID != "CVE-2024-0001" || page[1].VulnID != "CVE-2024-0002" || page[1].SeverityRank != 0 {
			t.Fatalf("page 1 = %+v, %v", page, err)
		}
		if page[1].CVSSScore == nil || *page[1].CVSSScore != 9.8 || page[1].KEV == nil || !*page[1].KEV || page[1].Description != "d" || page[1].VEXStatus != "not_affected" || page[0].KEV != nil {
			t.Fatalf("optional columns were not round tripped: %+v", page)
		}
		next := page[1].Cursor()
		rest, err := store.ListScanFindings(ctx, id, &next, 5)
		if err != nil || len(rest) != 1 || rest[0].VulnID != "CVE-2024-0003" || rest[0].SeverityRank != 3 {
			t.Fatalf("page 2 = %+v, %v", rest, err)
		}
		if err := store.InsertScanFindings(ctx, []ScanFinding{batch[0]}); err == nil {
			t.Fatal("a repeated (vulnerability, ordinal) must be refused")
		}
		oversized := make([]ScanFinding, MaxScanFindingBatch+1)
		for i := range oversized {
			oversized[i] = ScanFinding{VulnerabilityID: ids.New(), Ordinal: i, VulnID: "x", Severity: "low", Status: ScanFindingAffected}
		}
		if err := store.InsertScanFindings(ctx, oversized); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("oversized batch err = %v", err)
		}
		if err := store.InsertScanFindings(ctx, []ScanFinding{{VulnerabilityID: id, Ordinal: 9, VulnID: "x", Severity: "bogus", Status: ScanFindingAffected}}); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("bad severity err = %v", err)
		}
	})

	t.Run("history is appended only on the first scan or when status or counts change", func(t *testing.T) {
		artifact := "sha256:" + ids.New()
		swapScan := func(status string, high int, at time.Time) ScanSwap {
			t.Helper()
			swap, err := store.SwapScanResult(ctx, ScanResult{ID: ids.New(), RepositoryID: repo, ArtifactDigest: artifact, Scanner: "trivy", ScannerVersion: "0.60",
				DBGenerationID: ids.New(), Status: status, High: high, FindingsTotal: high, ScannedAt: at})
			if err != nil {
				t.Fatal(err)
			}
			return swap
		}
		if s := swapScan(ScanStatusVulnerable, 2, now); !s.HistoryAppended || s.PreviousID != "" {
			t.Fatalf("first scan = %+v", s)
		}
		if s := swapScan(ScanStatusVulnerable, 2, now.Add(time.Hour)); s.HistoryAppended || s.PreviousID == "" {
			t.Fatalf("unchanged re-scan = %+v", s)
		}
		if s := swapScan(ScanStatusVulnerable, 3, now.Add(2*time.Hour)); !s.HistoryAppended {
			t.Fatalf("changed counts = %+v", s)
		}
		if s := swapScan(ScanStatusStaleDB, 3, now.Add(3*time.Hour)); !s.HistoryAppended {
			t.Fatalf("changed status = %+v", s)
		}
		history, err := store.ListScanHistory(ctx, repo, artifact, 10)
		if err != nil || len(history) != 3 || history[0].Status != ScanStatusStaleDB || history[2].High != 2 || history[2].DBGenerationID == "" {
			t.Fatalf("history = %+v, %v", history, err)
		}
		current, err := store.GetScanResult(ctx, repo, artifact)
		if err != nil || current.Status != ScanStatusStaleDB || current.High != 3 || current.ScannerVersion != "0.60" {
			t.Fatalf("current = %+v, %v", current, err)
		}
		if _, err := store.GetScanResult(ctx, repo, "sha256:none"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing result err = %v", err)
		}
		if _, err := store.SwapScanResult(ctx, ScanResult{ID: ids.New(), RepositoryID: repo, ArtifactDigest: artifact, Scanner: "trivy", Status: "weird"}); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("bad status err = %v", err)
		}
	})

	t.Run("not scanned results keep their reason", func(t *testing.T) {
		artifact := "sha256:" + ids.New()
		if _, err := store.SwapScanResult(ctx, ScanResult{ID: ids.New(), RepositoryID: repo, ArtifactDigest: artifact, Scanner: "trivy", Status: ScanStatusNotScanned,
			NotScannedReason: "sbom_too_large", Truncated: true, ScannedAt: now}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetScanResult(ctx, repo, artifact)
		if err != nil || got.NotScannedReason != "sbom_too_large" || !got.Truncated || got.DBGenerationID != "" {
			t.Fatalf("result = %+v, %v", got, err)
		}
	})

	t.Run("licenses are paged without a cap", func(t *testing.T) {
		artifact := "sha256:" + ids.New()
		var licenses []ScanLicense
		for i := range 250 {
			licenses = append(licenses, ScanLicense{SPDXID: fmt.Sprintf("LIC-%04d", i), Source: "sbom", Confidence: 0.5})
		}
		licenses = append(licenses, licenses[0])
		if err := store.ReplaceScanLicenses(ctx, repo, artifact, licenses); err != nil {
			t.Fatal(err)
		}
		var after *ScanLicenseCursor
		seen := 0
		for {
			page, err := store.ListScanLicenses(ctx, repo, artifact, after, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			seen += len(page)
			after = &ScanLicenseCursor{SPDXID: page[len(page)-1].SPDXID, Source: page[len(page)-1].Source}
		}
		if seen != 250 {
			t.Fatalf("paged %d licences, want 250", seen)
		}
		if err := store.ReplaceScanLicenses(ctx, repo, artifact, []ScanLicense{{SPDXID: "MIT", Source: "sbom", Confidence: 1}}); err != nil {
			t.Fatal(err)
		}
		if page, err := store.ListScanLicenses(ctx, repo, artifact, nil, 100); err != nil || len(page) != 1 || page[0].SPDXID != "MIT" {
			t.Fatalf("replaced = %+v, %v", page, err)
		}
	})

	t.Run("scanner database generations are recorded once", func(t *testing.T) {
		digestA, digestB := "sha256:"+ids.New(), "sha256:"+ids.New()
		a, err := store.RecordScannerDBGeneration(ctx, ScannerDBGeneration{Kind: ScannerDBJava, OCIDigest: digestA, BuiltAt: now.Add(-time.Hour), ObservedAt: now.Add(-time.Hour), Source: ScannerDBSourceUpstream})
		if err != nil {
			t.Fatal(err)
		}
		again, err := store.RecordScannerDBGeneration(ctx, ScannerDBGeneration{Kind: ScannerDBJava, OCIDigest: digestA, BuiltAt: now, ObservedAt: now, Source: ScannerDBSourceMirror})
		if err != nil || again.ID != a.ID || !again.ObservedAt.Equal(now) || !again.BuiltAt.Equal(a.BuiltAt) {
			t.Fatalf("re-observed = %+v, %v", again, err)
		}
		b, err := store.RecordScannerDBGeneration(ctx, ScannerDBGeneration{Kind: ScannerDBJava, OCIDigest: digestB, BuiltAt: now.Add(24 * time.Hour), Source: ScannerDBSourceFeed})
		if err != nil {
			t.Fatal(err)
		}
		latest, err := store.LatestScannerDBGeneration(ctx, ScannerDBJava)
		if err != nil || latest.ID != b.ID {
			t.Fatalf("latest = %+v, %v", latest, err)
		}
		if _, err := store.LatestScannerDBGeneration(ctx, "nope"); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("bad kind err = %v", err)
		}
	})

	t.Run("vex documents list newest first", func(t *testing.T) {
		vexRepo := ids.New()
		for i, key := range []string{"meta/sha256/1", "meta/sha256/2"} {
			if _, err := store.PutVEXDocument(ctx, VEXDocument{RepositoryID: vexRepo, Format: VEXFormatOpenVEX, BodyDigest: "sha256:v", BodyKey: key, Author: "a",
				CreatedBy: ids.New(), CreatedAt: now.Add(time.Duration(i) * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
		listed, err := store.ListVEXDocuments(ctx, vexRepo)
		if err != nil || len(listed) != 2 || listed[0].BodyKey != "meta/sha256/2" || listed[0].CreatedBy == "" {
			t.Fatalf("vex = %+v, %v", listed, err)
		}
		if _, err := store.PutVEXDocument(ctx, VEXDocument{RepositoryID: vexRepo, Format: "csaf", BodyDigest: "d", BodyKey: "k"}); !errors.Is(err, ErrScanInvalid) {
			t.Fatalf("bad format err = %v", err)
		}
	})
}

func insertScanFindings(t *testing.T, store ScanStore, vulnerabilityID string, total int) {
	t.Helper()
	severities := []string{"critical", "high", "medium", "low", "unknown"}
	for start := 0; start < total; start += MaxScanFindingBatch {
		batch := make([]ScanFinding, 0, MaxScanFindingBatch)
		for i := start; i < min(start+MaxScanFindingBatch, total); i++ {
			batch = append(batch, ScanFinding{VulnerabilityID: vulnerabilityID, Ordinal: i, VulnID: fmt.Sprintf("CVE-2024-%06d", i), Severity: severities[i%len(severities)],
				Status: ScanFindingAffected, Component: "pkg", Version: "1"})
		}
		if err := store.InsertScanFindings(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMemoryScanStoreContract(t *testing.T) {
	testScanStoreContract(t, NewMemoryStore())
}
